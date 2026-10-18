package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ofk0/piway/internal/manifest"
	"github.com/ofk0/piway/internal/testfs"
	"github.com/ofk0/piway/internal/webhook"
)

const wait = 5 * time.Second

const appManifest = `piway: 1
name: guestbook
build:
  dockerfile: Dockerfile
run:
  port: 3000
`

// fakeSteps writes a manifest on Fetch and blocks Image until released, so
// tests control exactly when a build is running.
type fakeSteps struct {
	mu        sync.Mutex
	manifest  string
	started   chan string       // commit, when Image begins
	release   chan error        // Image returns the next value
	released  []string          // images released, in order
	imageFail error             // returned by Image instead of waiting, if set
	siteFiles map[string]string // written by Fetch next to piway.yaml
	siteFail  error             // returned by SiteBuild, if set
	siteDirs  []string          // directories ReleaseSite served, in order
	auths     []string          // the Auth of each build fetched, in order
	sources   []string          // provider/host of each build fetched, in order
	subdir    string            // Fetch writes piway.yaml here, under dir
}

func newFake() *fakeSteps {
	return &fakeSteps{manifest: appManifest, started: make(chan string, 10), release: make(chan error, 10)}
}

func (f *fakeSteps) Fetch(_ context.Context, b Build, dir string, out io.Writer) error {
	fmt.Fprintf(out, "fetched %s\n", b.Commit)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auths = append(f.auths, b.Auth)
	f.sources = append(f.sources, b.Provider+"/"+b.Host)
	dir = filepath.Join(dir, f.subdir)
	os.MkdirAll(dir, 0o755)
	for name, body := range f.siteFiles {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if strings.HasPrefix(body, "->") {
			os.Symlink(strings.TrimPrefix(body, "->"), p)
			continue
		}
		os.WriteFile(p, []byte(body+" "+b.Commit), 0o644)
	}
	return os.WriteFile(filepath.Join(dir, "piway.yaml"), []byte(f.manifest), 0o644)
}

func (f *fakeSteps) Image(ctx context.Context, _ *manifest.Manifest, _, image string, out io.Writer) error {
	fmt.Fprintf(out, "building %s\n", image)
	if f.imageFail != nil {
		return f.imageFail
	}
	f.started <- image
	select {
	case err := <-f.release:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeSteps) Release(_ context.Context, _ *manifest.Manifest, b Build, out io.Writer) error {
	fmt.Fprintf(out, "released %s\n", b.Image)
	f.mu.Lock()
	f.released = append(f.released, b.Image)
	f.mu.Unlock()
	return nil
}

// SiteBuild writes <static.dir>/built.txt, or fails with siteFail.
func (f *fakeSteps) SiteBuild(_ context.Context, m *manifest.Manifest, dir string, out io.Writer) error {
	fmt.Fprintf(out, "site build in %s\n", m.Static.Build.Image)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.siteFail != nil {
		return f.siteFail
	}
	os.MkdirAll(filepath.Join(dir, m.Static.Dir), 0o755)
	return os.WriteFile(filepath.Join(dir, m.Static.Dir, "built.txt"), []byte("built"), 0o644)
}

func (f *fakeSteps) ReleaseSite(_ context.Context, m *manifest.Manifest, b Build, dir string, out io.Writer) error {
	fmt.Fprintf(out, "released site %s\n", b.Site)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, "site:"+b.Site)
	f.siteDirs = append(f.siteDirs, dir)
	return nil
}

func (f *fakeSteps) releasedImages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.released...)
}

type harness struct {
	p      *Pipeline
	steps  *fakeSteps
	cancel context.CancelFunc
	done   chan struct{}
	dir    string
}

func start(t *testing.T, dir string, opts Options) *harness {
	t.Helper()
	steps := newFake()
	if opts.Steps == nil {
		opts.Steps = steps
	}
	opts.Dir = dir
	p, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{p: p, steps: steps, cancel: cancel, done: make(chan struct{}), dir: dir}
	go func() { p.Run(ctx); close(h.done) }()
	t.Cleanup(h.stop)
	return h
}

func (h *harness) stop() {
	h.cancel()
	<-h.done
}

func push(t *testing.T, p *Pipeline, commit string) string {
	t.Helper()
	if err := p.Deploy(webhook.Request{Repo: "OFK0/guestbook", Branch: "main", Commit: commit, CloneURL: "x"}); err != nil {
		t.Fatal(err)
	}
	list := p.List()
	return list[0].ID // newest first
}

func waitState(t *testing.T, p *Pipeline, id string, want State) Build {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		b, err := p.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if b.State == want {
			return b
		}
		if time.Now().After(deadline) {
			t.Fatalf("build %s is %s (%s), want %s", id, b.State, b.Error, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func started(t *testing.T, f *fakeSteps) string {
	t.Helper()
	select {
	case img := <-f.started:
		return img
	case <-time.After(wait):
		t.Fatal("no build started")
		return ""
	}
}

func TestBuildSucceeds(t *testing.T) {
	h := start(t, t.TempDir(), Options{})
	id := push(t, h.p, "0123456789abcdef")
	if img := started(t, h.steps); img != "piway/guestbook:0123456789ab" {
		t.Errorf("image %q", img)
	}
	if b := waitState(t, h.p, id, Running); b.App != "guestbook" {
		t.Errorf("app %q, want it from piway.yaml", b.App)
	}
	h.steps.release <- nil
	b := waitState(t, h.p, id, Succeeded)
	if b.Image != "piway/guestbook:0123456789ab" || b.Started.IsZero() || b.Finished.IsZero() {
		t.Errorf("build = %+v", b)
	}
	log, _, _, _ := h.p.Follow(id)
	for _, want := range []string{"==> Fetching OFK0/guestbook@main", "fetched 0123", "==> Building", "==> Releasing", "released piway/guestbook", "==> Deployed guestbook"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	// The checkout is removed; the record and log stay.
	if entries, _ := os.ReadDir(filepath.Join(h.dir, "work")); len(entries) != 0 {
		t.Errorf("work dir not cleaned: %v", entries)
	}
}

func TestOneBuildAtATimeAndNewestWins(t *testing.T) {
	h := start(t, t.TempDir(), Options{})
	first := push(t, h.p, "aaaaaaa1")
	started(t, h.steps) // first is building

	second := push(t, h.p, "bbbbbbb2")
	third := push(t, h.p, "ccccccc3")
	// second was queued behind the running build; third replaces it.
	if b := waitState(t, h.p, second, Cancelled); !strings.Contains(b.Error, "superseded by "+third) {
		t.Errorf("second: %+v", b)
	}
	waitState(t, h.p, third, Queued)
	select {
	case img := <-h.steps.started:
		t.Fatalf("%s started while another build was running", img)
	case <-time.After(50 * time.Millisecond):
	}

	h.steps.release <- nil
	waitState(t, h.p, first, Succeeded)
	if img := started(t, h.steps); img != "piway/guestbook:ccccccc3" {
		t.Errorf("next build %q, want the newest push", img)
	}
	h.steps.release <- nil
	waitState(t, h.p, third, Succeeded)
}

func TestCancelRunningAndQueued(t *testing.T) {
	h := start(t, t.TempDir(), Options{})
	running := push(t, h.p, "aaaaaaa1")
	started(t, h.steps)
	if err := h.p.Cancel(running); err != nil {
		t.Fatal(err)
	}
	waitState(t, h.p, running, Cancelled)
	if r := h.steps.releasedImages(); len(r) != 0 {
		t.Errorf("a cancelled build was released: %v", r)
	}

	// Queue two branches' worth: block the worker with a build, then cancel
	// the queued one.
	push(t, h.p, "bbbbbbb2")
	started(t, h.steps)
	if err := h.p.Deploy(webhook.Request{Repo: "OFK0/blog", Branch: "main", Commit: "ddddddd4"}); err != nil {
		t.Fatal(err)
	}
	queued := h.p.List()[0].ID
	if err := h.p.Cancel(queued); err != nil {
		t.Fatal(err)
	}
	waitState(t, h.p, queued, Cancelled)
	if err := h.p.Cancel(queued); err == nil {
		t.Error("cancelling a finished build succeeded")
	}
	if err := h.p.Cancel("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
}

func TestFollowStreamsLiveOutput(t *testing.T) {
	h := start(t, t.TempDir(), Options{})
	id := push(t, h.p, "aaaaaaa1")
	started(t, h.steps)

	past, next, stop, err := h.p.Follow(id)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if !strings.Contains(string(past), "building piway/guestbook") {
		t.Errorf("history so far: %q", past)
	}
	h.steps.release <- nil

	var live strings.Builder
	timeout := time.After(wait)
	for done := false; !done; {
		select {
		case chunk, ok := <-next:
			if !ok {
				done = true
			}
			live.Write(chunk)
		case <-timeout:
			t.Fatal("stream never closed")
		}
	}
	if !strings.Contains(live.String(), "==> Deployed") {
		t.Errorf("live output lacks the end: %q", live.String())
	}
	if strings.Contains(live.String(), "building piway") {
		t.Error("live output repeats history")
	}
}

func TestRestartMarksInterruptedBuilds(t *testing.T) {
	dir := t.TempDir()
	h := start(t, dir, Options{})
	id := push(t, h.p, "aaaaaaa1")
	started(t, h.steps)
	queued := push(t, h.p, "bbbbbbb2")
	// Simulate a crash: stop the worker without finishing the records.
	h.p.mu.Lock()
	h.p.builds[id].State = Running
	h.p.store.save(h.p.builds[id])
	h.p.mu.Unlock()
	h.stop()

	again := start(t, dir, Options{})
	for _, x := range []string{id, queued} {
		b, err := again.p.Get(x)
		if err != nil {
			t.Fatal(err)
		}
		if b.State != Failed && b.State != Cancelled {
			t.Errorf("%s after restart: %s", x, b.State)
		}
	}
	if b, _ := again.p.Get(queued); b.State != Failed || !strings.Contains(b.Error, "interrupted") {
		t.Errorf("queued build after restart: %+v", b)
	}
}

func TestInvalidManifestFails(t *testing.T) {
	h := start(t, t.TempDir(), Options{})
	h.steps.manifest = "piway: 1\nname: Bad_Name\n"
	id := push(t, h.p, "aaaaaaa1")
	b := waitState(t, h.p, id, Failed)
	log, _, _, _ := h.p.Follow(id)
	if !strings.Contains(b.Error, "piway.yaml") || !strings.Contains(string(log), "name") {
		t.Errorf("error %q, log:\n%s", b.Error, log)
	}
}

func TestTimeout(t *testing.T) {
	h := start(t, t.TempDir(), Options{Timeout: 100 * time.Millisecond})
	id := push(t, h.p, "aaaaaaa1")
	started(t, h.steps) // never released
	if b := waitState(t, h.p, id, Failed); !strings.Contains(b.Error, "timed out") {
		t.Errorf("error %q", b.Error)
	}
}

func TestKeepPrunesOldBuilds(t *testing.T) {
	dir := t.TempDir()
	h := start(t, dir, Options{Keep: 2})
	h.steps.imageFail = errors.New("boom")
	var ids []string
	for i := range 4 {
		id := push(t, h.p, fmt.Sprintf("%07d", i))
		waitState(t, h.p, id, Failed)
		ids = append(ids, id)
	}
	if n := len(h.p.List()); n != 2 {
		t.Errorf("%d builds kept, want 2", n)
	}
	if _, err := h.p.Get(ids[0]); !errors.Is(err, ErrNotFound) {
		t.Error("oldest build not pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, "builds", ids[0]+".log")); !os.IsNotExist(err) {
		t.Error("pruned build's log still on disk")
	}
}

func TestIDsAreUniqueAndOrdered(t *testing.T) {
	now := time.Date(2026, 10, 14, 20, 15, 0, 0, time.UTC)
	p, err := New(Options{Dir: t.TempDir(), Steps: newFake(), now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	a, b := p.newID("abcdef0123"), p.newID("abcdef0123")
	if a == b || !(a < b) || !strings.HasPrefix(a, "20261014T201500Z-abcdef0") {
		t.Errorf("ids %q, %q", a, b)
	}
}

func TestRollback(t *testing.T) {
	dir := t.TempDir()
	h := start(t, dir, Options{})
	if _, err := h.p.Rollback("guestbook"); !errors.Is(err, ErrNotFound) {
		t.Errorf("before any deploy: %v, want ErrNotFound", err)
	}

	first := push(t, h.p, "aaaaaaaaaaaa1")
	started(t, h.steps)
	h.steps.release <- nil
	waitState(t, h.p, first, Succeeded)
	if _, err := h.p.Rollback("guestbook"); !errors.Is(err, ErrNoRollback) {
		t.Errorf("with one deploy: %v, want ErrNoRollback", err)
	}

	second := push(t, h.p, "bbbbbbbbbbbb2")
	started(t, h.steps)
	h.steps.release <- nil
	waitState(t, h.p, second, Succeeded)
	if h.p.Live("guestbook") != second {
		t.Errorf("live = %q, want %q", h.p.Live("guestbook"), second)
	}

	// A restart in between: the saved piway.yaml must be enough.
	h.stop()
	h = start(t, dir, Options{})

	rb, err := h.p.Rollback("guestbook")
	if err != nil {
		t.Fatal(err)
	}
	if rb.Kind != KindRollback || rb.RollbackOf != first || rb.Image != "piway/guestbook:aaaaaaaaaaaa" {
		t.Errorf("rollback build = %+v", rb)
	}
	b := waitState(t, h.p, rb.ID, Succeeded)
	select {
	case img := <-h.steps.started:
		t.Errorf("a rollback rebuilt the image %s", img)
	default:
	}
	if r := h.steps.releasedImages(); len(r) != 1 || r[0] != "piway/guestbook:aaaaaaaaaaaa" {
		t.Errorf("released %v, want the first image", r)
	}
	log, _, _, _ := h.p.Follow(b.ID)
	if !strings.Contains(string(log), "==> Rolling guestbook back") || !strings.Contains(string(log), "==> Rolled guestbook back") {
		t.Errorf("log:\n%s", log)
	}
	if h.p.Live("guestbook") != rb.ID {
		t.Errorf("live after rollback = %q", h.p.Live("guestbook"))
	}
}

type fakeRuntime map[string]Container

func (f fakeRuntime) Containers(context.Context) (map[string]Container, error) { return f, nil }

func TestAppsAndRedeploy(t *testing.T) {
	rt := fakeRuntime{"guestbook": {Name: "piway-guestbook-x", State: "running", Image: "piway/guestbook:aaaaaaaaaaaa"}}
	url := func(app string) string { return "https://" + app + ".pi.local:8443" }
	h := start(t, t.TempDir(), Options{Runtime: rt, AppURL: url})
	if apps, _ := h.p.Apps(context.Background()); len(apps) != 0 {
		t.Errorf("apps before any build: %+v", apps)
	}
	if _, err := h.p.Redeploy("guestbook"); !errors.Is(err, ErrNotFound) {
		t.Errorf("redeploy unknown app: %v", err)
	}

	good := push(t, h.p, "aaaaaaaaaaaa1")
	started(t, h.steps)
	h.steps.release <- nil
	waitState(t, h.p, good, Succeeded)
	h.steps.imageFail = errors.New("compile error")
	bad := push(t, h.p, "bbbbbbbbbbbb2")
	waitState(t, h.p, bad, Failed)

	apps, err := h.p.Apps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 {
		t.Fatalf("apps = %+v", apps)
	}
	a := apps[0]
	if a.Name != "guestbook" || a.Repo != "OFK0/guestbook" || a.Port != HostPort("guestbook") || a.URL != "https://guestbook.pi.local:8443" {
		t.Errorf("app = %+v", a)
	}
	if a.Live == nil || a.Live.ID != good || a.Last == nil || a.Last.ID != bad || a.Last.State != Failed {
		t.Errorf("live/last = %+v / %+v; want the good build live and the failed one last", a.Live, a.Last)
	}
	if a.Container == nil || a.Container.State != "running" {
		t.Errorf("container = %+v", a.Container)
	}

	h.steps.imageFail = nil
	rb, err := h.p.Redeploy("guestbook")
	if err != nil {
		t.Fatal(err)
	}
	if rb.Commit != "aaaaaaaaaaaa1" || rb.Pusher != "dashboard" || rb.Kind != "" {
		t.Errorf("redeploy = %+v, want a fresh build of the live commit", rb)
	}
	if img := started(t, h.steps); img != "piway/guestbook:aaaaaaaaaaaa" {
		t.Errorf("redeploy built %q", img)
	}
	h.steps.release <- nil
	waitState(t, h.p, rb.ID, Succeeded)
}

func TestOnFinish(t *testing.T) {
	var mu sync.Mutex
	var got []string
	h := start(t, t.TempDir(), Options{OnFinish: func(b Build) {
		mu.Lock()
		got = append(got, string(b.State)+":"+b.App)
		mu.Unlock()
	}})
	ok := push(t, h.p, "aaaaaaaaaaaa1")
	started(t, h.steps)
	h.steps.release <- nil
	waitState(t, h.p, ok, Succeeded)
	h.steps.imageFail = errors.New("compile error")
	waitState(t, h.p, push(t, h.p, "bbbbbbbbbbbb2"), Failed)
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(got, " ") != "succeeded:guestbook failed:guestbook" {
		t.Errorf("OnFinish saw %v", got)
	}
}

// A full disk does not take the pipeline down: a push that cannot be
// recorded is refused with the reason and leaves nothing half-written, a
// running build whose log cannot be written still finishes, and once space
// is back pushes are accepted and built again.
func TestFullDisk(t *testing.T) {
	testfs.Run(t, 512<<10, func(t *testing.T, dir string) {
		builds := filepath.Join(dir, "builds")
		h := start(t, builds, Options{})
		running := push(t, h.p, "aaaaaaa1")
		started(t, h.steps)

		free := testfs.Fill(t, dir)
		// The log of the running build cannot be written, but it goes on.
		h.p.mu.Lock()
		log := h.p.running.log
		h.p.mu.Unlock()
		output := bytes.Repeat([]byte("more output\n"), 8192)
		if n, err := log.Write(output); err != nil || n != len(output) {
			t.Errorf("a build's log on a full disk: wrote %d of %d, %v; the build would be stopped by it", n, len(output), err)
		}

		err := h.p.Deploy(webhook.Request{Repo: "OFK0/guestbook", Branch: "dev", Commit: "bbbbbbb2", CloneURL: "x"})
		if !errors.Is(err, syscall.ENOSPC) {
			t.Errorf("a push on a full disk: %v, want ENOSPC", err)
		}
		if leftovers, _ := filepath.Glob(filepath.Join(builds, "builds", "*.tmp")); len(leftovers) > 0 {
			t.Errorf("half-written records left behind: %v", leftovers)
		}

		h.steps.release <- nil
		waitState(t, h.p, running, Succeeded)

		free()
		id := push(t, h.p, "ccccccc3")
		started(t, h.steps)
		h.steps.release <- nil
		waitState(t, h.p, id, Succeeded)
	})
}

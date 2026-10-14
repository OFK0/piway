package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ofk0/piway/internal/manifest"
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
	started   chan string // commit, when Image begins
	release   chan error  // Image returns the next value
	released  []string    // images released, in order
	imageFail error       // returned by Image instead of waiting, if set
}

func newFake() *fakeSteps {
	return &fakeSteps{manifest: appManifest, started: make(chan string, 10), release: make(chan error, 10)}
}

func (f *fakeSteps) Fetch(_ context.Context, b Build, dir string, out io.Writer) error {
	fmt.Fprintf(out, "fetched %s\n", b.Commit)
	f.mu.Lock()
	defer f.mu.Unlock()
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

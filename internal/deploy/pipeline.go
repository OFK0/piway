package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/ofk0/piway/internal/manifest"
	"github.com/ofk0/piway/internal/webhook"
)

// Steps does the actual work of a build. The pipeline owns ordering,
// state, logging and cancellation; Steps only runs commands. Each step
// writes its output to out and must stop when ctx is cancelled.
type Steps interface {
	// Fetch puts the build's commit into the empty directory dir.
	Fetch(ctx context.Context, b Build, dir string, out io.Writer) error
	// Image builds the app's image from the checkout in dir.
	Image(ctx context.Context, m *manifest.Manifest, dir, image string, out io.Writer) error
	// Release replaces the app's running container with one from image.
	Release(ctx context.Context, m *manifest.Manifest, b Build, out io.Writer) error
}

// Options configures a Pipeline.
type Options struct {
	Dir     string // builds/ (records, logs) and work/ (checkouts) go here
	Steps   Steps
	Logger  *slog.Logger
	Timeout time.Duration // per build; default 30 minutes
	Keep    int           // finished builds kept with their logs; default 100

	now func() time.Time
}

// Limits.
const (
	maxQueued = 50 // distinct branches waiting at once
)

// Pipeline queues deploy requests and runs them one at a time.
//
// Only the newest request per repository branch matters: a push that
// arrives while an older one is still queued replaces it, and the older one
// is marked cancelled. A build that is already running finishes (or is
// cancelled through Cancel); the newer one runs after it. One build at a
// time across all apps keeps a Pi responsive while it compiles.
type Pipeline struct {
	opts  Options
	store store
	log   *slog.Logger

	mu      sync.Mutex
	builds  map[string]*Build // all known builds by id
	order   []string          // ids, oldest first
	pending map[string]string // key -> id of the queued build
	queue   []string          // keys with a pending build, in arrival order
	running *runningBuild
	wake    chan struct{}
	lastID  string
}

type runningBuild struct {
	id     string
	cancel context.CancelFunc
	log    *buildLog
}

// New loads the build history from opts.Dir. Builds that were queued or
// running when piway stopped are marked failed.
func New(opts Options) (*Pipeline, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Minute
	}
	if opts.Keep <= 0 {
		opts.Keep = 100
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.now == nil {
		opts.now = time.Now
	}
	st := store{dir: filepath.Join(opts.Dir, "builds")}
	for _, d := range []string{st.dir, filepath.Join(opts.Dir, "work")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	p := &Pipeline{
		opts: opts, store: st, log: opts.Logger,
		builds: map[string]*Build{}, pending: map[string]string{},
		wake: make(chan struct{}, 1),
	}
	loaded, err := st.load()
	if err != nil {
		return nil, err
	}
	for _, b := range loaded {
		if !b.State.Done() {
			b.State, b.Error, b.Finished = Failed, "interrupted: piway stopped during the build", opts.now()
			if err := st.save(b); err != nil {
				return nil, err
			}
		}
		p.builds[b.ID] = b
		p.order = append(p.order, b.ID)
	}
	// Checkouts of interrupted builds are useless now.
	os.RemoveAll(filepath.Join(opts.Dir, "work"))
	os.MkdirAll(filepath.Join(opts.Dir, "work"), 0o755)
	return p, nil
}

// Deploy queues a request. It implements webhook.Deployer and returns at
// once.
func (p *Pipeline) Deploy(r webhook.Request) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	b := &Build{
		ID: p.newID(r.Commit), Repo: r.Repo, Branch: r.Branch, Commit: r.Commit,
		Pusher: r.Pusher, CloneURL: r.CloneURL, State: Queued, Queued: p.opts.now(),
	}
	return p.enqueueLocked(b)
}

// enqueueLocked records b and queues it, replacing a pending build with
// the same key. Caller holds p.mu.
func (p *Pipeline) enqueueLocked(b *Build) error {
	key := b.key()
	old, replacing := p.pending[key]
	if !replacing && len(p.queue) >= maxQueued {
		return fmt.Errorf("deploy: %d builds already waiting", len(p.queue))
	}
	if err := p.store.save(b); err != nil {
		return err
	}
	p.builds[b.ID] = b
	p.order = append(p.order, b.ID)
	if replacing {
		p.finish(p.builds[old], Cancelled, "superseded by "+b.ID)
	} else {
		p.queue = append(p.queue, key)
	}
	p.pending[key] = b.ID
	p.log.Info("build queued", "build", b.ID, "repo", b.Repo, "branch", b.Branch, "kind", b.Kind)
	select {
	case p.wake <- struct{}{}:
	default:
	}
	return nil
}

// newID is sortable by queue time and unique: 20261014T201502Z-0123abc.
func (p *Pipeline) newID(commit string) string {
	short := commit
	if len(short) > 7 {
		short = short[:7]
	}
	id := p.opts.now().UTC().Format("20060102T150405Z") + "-" + short
	for n := 2; id <= p.lastID || p.builds[id] != nil; n++ {
		id = fmt.Sprintf("%s-%s.%d", p.opts.now().UTC().Format("20060102T150405Z"), short, n)
	}
	p.lastID = id
	return id
}

// Run works through the queue until ctx is cancelled, which also cancels a
// running build.
func (p *Pipeline) Run(ctx context.Context) {
	for {
		id, ok := p.next()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-p.wake:
				continue
			}
		}
		p.run(ctx, id)
		if ctx.Err() != nil {
			return
		}
	}
}

// next takes the oldest pending build off the queue.
func (p *Pipeline) next() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 {
		return "", false
	}
	key := p.queue[0]
	p.queue = p.queue[1:]
	id := p.pending[key]
	delete(p.pending, key)
	return id, true
}

func (p *Pipeline) run(parent context.Context, id string) {
	ctx, cancel := context.WithTimeout(parent, p.opts.Timeout)
	defer cancel()

	p.mu.Lock()
	b := p.builds[id]
	log, err := openLog(p.store.logPath(id))
	if err != nil {
		p.finish(b, Failed, "open log: "+err.Error())
		p.mu.Unlock()
		return
	}
	b.State, b.Started = Running, p.opts.now()
	p.store.save(b)
	p.running = &runningBuild{id: id, cancel: cancel, log: log}
	snapshot := *b
	p.mu.Unlock()

	p.log.Info("build started", "build", id, "repo", b.Repo, "commit", b.Commit)
	app, image, err := p.steps(ctx, snapshot, log)

	p.mu.Lock()
	defer p.mu.Unlock()
	b.App, b.Image = app, image
	switch {
	case err == nil && b.Kind == KindRollback:
		fmt.Fprintf(log, "\n==> Rolled %s back to %s\n", b.App, b.Commit)
		p.finish(b, Succeeded, "")
	case err == nil:
		fmt.Fprintf(log, "\n==> Deployed %s from %s\n", b.App, b.Commit)
		p.finish(b, Succeeded, "")
	case errors.Is(ctx.Err(), context.Canceled):
		fmt.Fprintf(log, "\n==> Cancelled\n")
		p.finish(b, Cancelled, "cancelled")
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		fmt.Fprintf(log, "\n==> Timed out after %v\n", p.opts.Timeout)
		p.finish(b, Failed, fmt.Sprintf("timed out after %v", p.opts.Timeout))
	default:
		fmt.Fprintf(log, "\n==> Failed: %v\n", err)
		p.finish(b, Failed, err.Error())
	}
	p.running = nil
	log.close()
	p.log.Info("build finished", "build", id, "state", b.State, "err", b.Error)
}

// steps runs the build and reports what it learned along the way.
func (p *Pipeline) steps(ctx context.Context, b Build, out io.Writer) (app, image string, err error) {
	if b.Kind == KindRollback {
		m, err := manifest.Load(p.store.manifestPath(b.RollbackOf))
		if err != nil {
			return b.App, b.Image, fmt.Errorf("piway.yaml of %s: %w", b.RollbackOf, err)
		}
		fmt.Fprintf(out, "==> Rolling %s back to %s (build %s, commit %s)\n", m.Name, b.Image, b.RollbackOf, b.Commit)
		if err := p.opts.Steps.Release(ctx, m, b, out); err != nil {
			return b.App, b.Image, fmt.Errorf("release: %w", err)
		}
		return b.App, b.Image, nil
	}

	dir := filepath.Join(p.opts.Dir, "work", b.ID)
	defer os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}

	fmt.Fprintf(out, "==> Fetching %s@%s (%s)\n", b.Repo, b.Branch, b.Commit)
	if err := p.opts.Steps.Fetch(ctx, b, dir, out); err != nil {
		return "", "", fmt.Errorf("fetch: %w", err)
	}

	m, err := manifest.Load(filepath.Join(dir, "piway.yaml"))
	if err != nil {
		fmt.Fprintf(out, "%v\n", err)
		return "", "", errors.New("piway.yaml is missing or invalid")
	}
	app = m.Name
	p.mu.Lock()
	p.builds[b.ID].App = app
	p.mu.Unlock()
	if raw, err := os.ReadFile(filepath.Join(dir, "piway.yaml")); err == nil {
		os.WriteFile(p.store.manifestPath(b.ID), raw, 0o644)
	}
	if m.Static != nil {
		return app, "", errors.New("static sites need the Caddy integration (#24), which is not there yet")
	}

	image = "piway/" + m.Name + ":" + shortCommit(b.Commit)
	fmt.Fprintf(out, "\n==> Building %s\n", image)
	if err := p.opts.Steps.Image(ctx, m, dir, image, out); err != nil {
		return app, "", fmt.Errorf("build: %w", err)
	}

	b.App, b.Image = app, image
	fmt.Fprintf(out, "\n==> Releasing %s\n", image)
	if err := p.opts.Steps.Release(ctx, m, b, out); err != nil {
		return app, image, fmt.Errorf("release: %w", err)
	}
	return app, image, nil
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// finish records a final state and prunes old builds. Caller holds p.mu.
func (p *Pipeline) finish(b *Build, s State, msg string) {
	b.State, b.Error, b.Finished = s, msg, p.opts.now()
	if err := p.store.save(b); err != nil {
		p.log.Warn("save build", "build", b.ID, "err", err)
	}
	var done []string
	for _, id := range p.order {
		if p.builds[id].State.Done() {
			done = append(done, id)
		}
	}
	for _, id := range done[:max(0, len(done)-p.opts.Keep)] {
		p.store.remove(id)
		delete(p.builds, id)
		p.order = slices.DeleteFunc(p.order, func(x string) bool { return x == id })
	}
}

// ErrNoRollback is returned when an app has no earlier successful build
// to roll back to.
var ErrNoRollback = errors.New("deploy: nothing to roll back to")

// Live returns the id of the build an app is running: its latest
// successful build or rollback, or "" if none.
func (p *Pipeline) Live(app string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if b := p.liveLocked(app); b != nil {
		return b.ID
	}
	return ""
}

func (p *Pipeline) liveLocked(app string) *Build {
	for i := len(p.order) - 1; i >= 0; i-- {
		if b := p.builds[p.order[i]]; b.App == app && b.State == Succeeded {
			return b
		}
	}
	return nil
}

// Rollback queues a release of the image the app ran before its current
// one, without rebuilding. It goes through the same health check as a
// deploy, so a broken old version never replaces a working one.
func (p *Pipeline) Rollback(app string) (Build, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cur := p.liveLocked(app)
	if cur == nil {
		return Build{}, ErrNotFound
	}
	var target *Build
	for i := len(p.order) - 1; i >= 0; i-- {
		b := p.builds[p.order[i]]
		if b.App == app && b.State == Succeeded && b.Image != cur.Image && b.Image != "" {
			if _, err := os.Stat(p.store.manifestPath(b.ID)); err == nil {
				target = b
				break
			}
		}
	}
	if target == nil {
		return Build{}, ErrNoRollback
	}
	rb := &Build{
		ID: p.newID(target.Commit), App: app, Repo: target.Repo, Branch: target.Branch,
		Commit: target.Commit, CloneURL: target.CloneURL, Image: target.Image,
		Kind: KindRollback, RollbackOf: target.ID, State: Queued, Queued: p.opts.now(),
	}
	if err := p.enqueueLocked(rb); err != nil {
		return Build{}, err
	}
	return *rb, nil
}

// Cancel stops a queued or running build.
func (p *Pipeline) Cancel(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	b, ok := p.builds[id]
	if !ok {
		return ErrNotFound
	}
	switch {
	case b.State == Queued:
		key := b.key()
		delete(p.pending, key)
		p.queue = slices.DeleteFunc(p.queue, func(k string) bool { return k == key })
		p.finish(b, Cancelled, "cancelled")
	case p.running != nil && p.running.id == id:
		p.running.cancel() // run() records the outcome
	default:
		return fmt.Errorf("deploy: build %s already %s", id, b.State)
	}
	return nil
}

// List returns builds, newest first.
func (p *Pipeline) List() []Build {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Build, 0, len(p.order))
	for i := len(p.order) - 1; i >= 0; i-- {
		out = append(out, *p.builds[p.order[i]])
	}
	return out
}

// Get returns one build.
func (p *Pipeline) Get(id string) (Build, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	b, ok := p.builds[id]
	if !ok {
		return Build{}, ErrNotFound
	}
	return *b, nil
}

// Follow returns a build's log so far and, while it runs, a channel of
// what follows; the channel is closed when the build ends (immediately for
// a finished one). stop releases the subscription.
func (p *Pipeline) Follow(id string) (past []byte, next <-chan []byte, stop func(), err error) {
	p.mu.Lock()
	_, ok := p.builds[id]
	var live *buildLog
	if ok && p.running != nil && p.running.id == id {
		live = p.running.log
	}
	p.mu.Unlock()
	if !ok {
		return nil, nil, nil, ErrNotFound
	}
	if live != nil {
		past, ch, stop := live.subscribe(p.store.logPath(id))
		return past, ch, stop, nil
	}
	// Finished, or still queued and silent: nothing more will arrive on
	// this subscription. A client watching a queued build follows again
	// once Get reports it running.
	past, _ = os.ReadFile(p.store.logPath(id))
	ch := make(chan []byte)
	close(ch)
	return past, ch, func() {}, nil
}

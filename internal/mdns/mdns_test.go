package mdns

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAvahi records what is published, like the avahi daemon would.
type fakeAvahi struct {
	mu        sync.Mutex
	host      string
	down      bool
	collides  map[string]bool // names another device already uses
	published map[string]*fakeGroup
	aliases   int
}

type fakeGroup struct {
	a      *fakeAvahi
	name   string
	target string
	state  State
	freed  bool
}

func newFake() *fakeAvahi {
	return &fakeAvahi{host: "raspberrypi.local", collides: map[string]bool{}, published: map[string]*fakeGroup{}}
}

func (f *fakeAvahi) HostName() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return "", errors.New("org.freedesktop.Avahi was not provided by any .service files")
	}
	return f.host, nil
}

func (f *fakeAvahi) Alias(name, target string) (Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errors.New("avahi is down")
	}
	f.aliases++
	g := &fakeGroup{a: f, name: name, target: target, state: Established}
	if f.collides[name] {
		g.state = Collision
	} else {
		f.published[name] = g
	}
	return g, nil
}

// restart drops every group, as avahi does when it restarts.
func (f *fakeAvahi) restart() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, g := range f.published {
		g.freed = true
	}
	f.published = map[string]*fakeGroup{}
}

func (g *fakeGroup) State() (State, error) {
	g.a.mu.Lock()
	defer g.a.mu.Unlock()
	if g.freed {
		return 0, errors.New("no such object")
	}
	return g.state, nil
}

func (g *fakeGroup) Free() error {
	g.a.mu.Lock()
	defer g.a.mu.Unlock()
	if g.freed {
		return errors.New("no such object")
	}
	g.freed = true
	if g.a.published[g.name] == g {
		delete(g.a.published, g.name)
	}
	return nil
}

// names lists what is published as "name>target".
func (f *fakeAvahi) names() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n, g := range f.published {
		out = append(out, n+">"+g.target)
	}
	slices.Sort(out)
	return strings.Join(out, " ")
}

func TestSyncPublishesAndWithdraws(t *testing.T) {
	f := newFake()
	p := New(f, "local", nil)
	if err := p.Sync([]string{"photos", "blog"}); err != nil {
		t.Fatal(err)
	}
	if got, want := f.names(), "blog.local>raspberrypi.local photos.local>raspberrypi.local"; got != want {
		t.Errorf("published %q, want %q", got, want)
	}
	if err := p.Sync([]string{"photos", "blog"}); err != nil || f.aliases != 2 {
		t.Errorf("an unchanged set published again: %d aliases, err %v", f.aliases, err)
	}
	if err := p.Sync([]string{"photos"}); err != nil {
		t.Fatal(err)
	}
	if got := f.names(); got != "photos.local>raspberrypi.local" {
		t.Errorf("after removing blog: %q", got)
	}
	p.Close()
	if got := f.names(); got != "" {
		t.Errorf("after Close: %q", got)
	}
}

func TestRepublishesAfterAvahiRestarts(t *testing.T) {
	f := newFake()
	p := New(f, "pi.local", nil)
	p.Sync([]string{"photos"})
	f.restart()
	if err := p.Sync([]string{"photos"}); err != nil {
		t.Fatal(err)
	}
	if got := f.names(); got != "photos.pi.local>raspberrypi.local" {
		t.Errorf("after avahi restarted: %q", got)
	}
}

func TestFollowsHostNameChange(t *testing.T) {
	f := newFake()
	p := New(f, "local", nil)
	p.Sync([]string{"photos"})
	// Another device took the Pi's name; avahi renamed the Pi.
	f.mu.Lock()
	f.host = "raspberrypi-2.local"
	f.mu.Unlock()
	if err := p.Sync([]string{"photos"}); err != nil {
		t.Fatal(err)
	}
	if got := f.names(); got != "photos.local>raspberrypi-2.local" {
		t.Errorf("after the rename: %q", got)
	}
}

func TestCollisionIsReportedOnceAndIsolated(t *testing.T) {
	f := newFake()
	f.collides["printer.local"] = true
	var logs bytes.Buffer
	p := New(f, "local", slog.New(slog.NewTextHandler(&logs, nil)))
	for range 3 {
		if err := p.Sync([]string{"printer", "photos"}); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.names(); got != "photos.local>raspberrypi.local" {
		t.Errorf("published %q: a collision must not affect other names", got)
	}
	if n := strings.Count(logs.String(), "mdns name not published"); n != 1 {
		t.Errorf("collision logged %d times, want once:\n%s", n, logs.String())
	}
	if f.aliases != 2 {
		t.Errorf("%d publish attempts, want 2: a collision is not retried every sync", f.aliases)
	}
}

func TestAvahiDown(t *testing.T) {
	f := newFake()
	f.down = true
	p := New(f, "local", nil)
	if err := p.Sync([]string{"photos"}); err == nil {
		t.Fatal("no error with avahi down")
	}
	f.mu.Lock()
	f.down = false
	f.mu.Unlock()
	if err := p.Sync([]string{"photos"}); err != nil || f.names() != "photos.local>raspberrypi.local" {
		t.Errorf("after avahi came back: %q, %v", f.names(), err)
	}
}

func TestPublishes(t *testing.T) {
	for domain, want := range map[string]bool{
		"local": true, "pi.local": true, "a.b.local": true,
		"localhost": false, "pi.internal": false, "example.com": false, "notlocal": false,
	} {
		if got := Publishes(domain); got != want {
			t.Errorf("Publishes(%q) = %v, want %v", domain, got, want)
		}
	}
}

func TestWireName(t *testing.T) {
	b, err := wireName("raspberrypi.local.")
	if err != nil || string(b) != "\x0braspberrypi\x05local\x00" {
		t.Errorf("wireName = %q, %v", b, err)
	}
	for _, bad := range []string{"", "a..local", strings.Repeat("x", 64) + ".local"} {
		if _, err := wireName(bad); err == nil {
			t.Errorf("wireName(%q) accepted", bad)
		}
	}
}

func TestRunWithdrawsOnStop(t *testing.T) {
	f := newFake()
	p := New(f, "local", nil)
	changed := make(chan struct{}, 1)
	var mu sync.Mutex
	apps := []string{"photos"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx, changed, func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(apps) }, time.Hour)
		close(done)
	}()
	waitFor := func(want string) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if f.names() == want {
				return
			}
		}
		t.Fatalf("published %q, want %q", f.names(), want)
	}
	waitFor("photos.local>raspberrypi.local")
	mu.Lock()
	apps = append(apps, "blog")
	mu.Unlock()
	changed <- struct{}{}
	waitFor("blog.local>raspberrypi.local photos.local>raspberrypi.local")
	cancel()
	<-done
	if got := f.names(); got != "" {
		t.Errorf("still published after Run returned: %q", got)
	}
}

func TestSkipsThePisOwnName(t *testing.T) {
	f := newFake()
	var logs bytes.Buffer
	p := New(f, "local", slog.New(slog.NewTextHandler(&logs, nil)))
	p.Sync([]string{"raspberrypi", "photos"})
	p.Sync([]string{"raspberrypi", "photos"})
	if got := f.names(); got != "photos.local>raspberrypi.local" {
		t.Errorf("published %q: raspberrypi.local as a CNAME of itself would loop", got)
	}
	if n := strings.Count(logs.String(), "the Pi's own name"); n != 1 {
		t.Errorf("logged %d times, want once:\n%s", n, logs.String())
	}
}

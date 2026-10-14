package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ofk0/piway/internal/collector/mem"
)

// fakeCollector writes Memory.Total = calls so tests can see which round produced a value.
type fakeCollector struct {
	name   string
	calls  atomic.Int64
	closed atomic.Bool
	fn     func(ctx context.Context, n int64, s *Snapshot) error
}

func (f *fakeCollector) Name() string { return f.name }
func (f *fakeCollector) Close() error { f.closed.Store(true); return nil }
func (f *fakeCollector) Collect(ctx context.Context, _ time.Time, s *Snapshot) error {
	return f.fn(ctx, f.calls.Add(1), s)
}

func memFake() *fakeCollector {
	return &fakeCollector{name: "memory", fn: func(_ context.Context, n int64, s *Snapshot) error {
		s.Memory = &mem.Info{Total: uint64(n)}
		return nil
	}}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// waitFor polls until cond holds; the agent publishes asynchronously.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 2s")
		}
		time.Sleep(time.Millisecond)
	}
}

func startAgent(t *testing.T, interval time.Duration, cs ...Collector) *Agent {
	t.Helper()
	a, err := New(interval, discardLogger(), cs...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return a
}

func TestNewRejectsNonPositiveInterval(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		if _, err := New(d, discardLogger()); err == nil {
			t.Errorf("New(%v) succeeded, want error", d)
		}
	}
}

func TestRunPublishesImmediatelyThenOnTicks(t *testing.T) {
	// A long interval proves the first snapshot does not wait for a tick.
	a := startAgent(t, time.Hour, memFake())
	waitFor(t, func() bool { return a.Latest() != nil })

	s := a.Latest()
	if s.Seq != 1 || s.Memory == nil || s.Memory.Total != 1 {
		t.Fatalf("first snapshot = %+v, want seq 1 from round 1", s)
	}

	b := startAgent(t, 5*time.Millisecond, memFake())
	waitFor(t, func() bool { s := b.Latest(); return s != nil && s.Seq >= 3 })
}

func TestOnPublishSeesEverySnapshotInOrder(t *testing.T) {
	a, err := New(time.Millisecond, discardLogger(), memFake())
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan *Snapshot, 100)
	a.OnPublish(func(s *Snapshot) {
		if a.Latest() != s {
			t.Error("OnPublish called before Latest returns the snapshot")
		}
		got <- s
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()

	for want := uint64(1); want <= 3; want++ {
		select {
		case s := <-got:
			if s.Seq != want {
				t.Fatalf("seq = %d, want %d", s.Seq, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("snapshot %d not published", want)
		}
	}
	cancel()
	<-done
}

func TestFailingCollectorDoesNotStopOthers(t *testing.T) {
	failing := &fakeCollector{name: "broken", fn: func(context.Context, int64, *Snapshot) error {
		return errors.New("disk on fire")
	}}
	panicking := &fakeCollector{name: "panicky", fn: func(context.Context, int64, *Snapshot) error {
		var m map[string]int
		m["boom"] = 1 // nil map write panics
		return nil
	}}
	good := memFake()

	a := startAgent(t, 5*time.Millisecond, failing, panicking, good)
	waitFor(t, func() bool { s := a.Latest(); return s != nil && s.Seq >= 3 })

	s := a.Latest()
	if s.Memory == nil {
		t.Error("healthy collector produced no data")
	}
	if s.Errors["broken"] != "disk on fire" {
		t.Errorf(`Errors["broken"] = %q`, s.Errors["broken"])
	}
	if !strings.HasPrefix(s.Errors["panicky"], "panic: assignment to entry in nil map") {
		t.Errorf(`Errors["panicky"] = %q, want a recovered panic`, s.Errors["panicky"])
	}
	if panicking.calls.Load() < 3 {
		t.Error("panicking collector was not called again in later rounds")
	}
}

func TestCollectorsRunConcurrently(t *testing.T) {
	// Each collector waits for the other to start. Run sequentially, the first
	// one would time out; run in parallel, both meet.
	var arrived atomic.Int32
	both := make(chan struct{})
	rendezvous := func(context.Context, int64, *Snapshot) error {
		if arrived.Add(1) == 2 {
			close(both)
		}
		select {
		case <-both:
			return nil
		case <-time.After(time.Second):
			return errors.New("collectors ran sequentially")
		}
	}
	a := startAgent(t, time.Hour,
		&fakeCollector{name: "a", fn: rendezvous},
		&fakeCollector{name: "b", fn: rendezvous})
	waitFor(t, func() bool { return a.Latest() != nil })

	if errs := a.Latest().Errors; len(errs) > 0 {
		t.Errorf("Errors = %v", errs)
	}
}

func TestRoundIsBoundedByInterval(t *testing.T) {
	slow := &fakeCollector{name: "slow", fn: func(ctx context.Context, _ int64, _ *Snapshot) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	a := startAgent(t, 20*time.Millisecond, slow)
	waitFor(t, func() bool { return a.Latest() != nil })

	if got := a.Latest().Errors["slow"]; got != context.DeadlineExceeded.Error() {
		t.Errorf(`Errors["slow"] = %q, want deadline exceeded`, got)
	}
}

func TestLatestIsSafeForConcurrentReaders(t *testing.T) {
	// Meaningful under `go test -race` (make test-race, run in CI).
	a := startAgent(t, time.Millisecond, memFake(), &fakeCollector{name: "network", fn: func(context.Context, int64, *Snapshot) error {
		return nil
	}})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) * 2 {
		wg.Go(func() {
			var last uint64
			for ctx.Err() == nil {
				s := a.Latest()
				if s == nil {
					continue
				}
				if s.Seq < last {
					t.Errorf("seq went backwards: %d after %d", s.Seq, last)
					return
				}
				last = s.Seq
				_ = s.Memory.Total
				_ = len(s.Errors)
			}
		})
	}
	wg.Wait()
}

func TestErrorsAreLoggedOnTransitionOnly(t *testing.T) {
	var buf syncBuffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	var fail atomic.Bool
	fail.Store(true)
	flaky := &fakeCollector{name: "flaky", fn: func(context.Context, int64, *Snapshot) error {
		if fail.Load() {
			return errors.New("nope")
		}
		return nil
	}}
	a, err := New(time.Millisecond, log, flaky)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for range 5 {
		a.publish(ctx)
	}
	fail.Store(false)
	for range 5 {
		a.publish(ctx)
	}

	out := buf.String()
	if n := strings.Count(out, "collector failed"); n != 1 {
		t.Errorf("logged failure %d times, want 1:\n%s", n, out)
	}
	if n := strings.Count(out, "collector recovered"); n != 1 {
		t.Errorf("logged recovery %d times, want 1:\n%s", n, out)
	}
}

func TestCloseClosesAllCollectors(t *testing.T) {
	x, y := memFake(), memFake()
	a, err := New(time.Second, discardLogger(), x, y)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if !x.closed.Load() || !y.closed.Load() {
		t.Error("not every collector was closed")
	}
}

func TestDefaultCollectorsOnThisMachine(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("built-in collectors read Linux procfs")
	}
	a := startAgent(t, 50*time.Millisecond, OpenDefault(discardLogger())...)
	waitFor(t, func() bool { s := a.Latest(); return s != nil && s.Seq >= 2 })

	s := a.Latest()
	if len(s.Errors) > 0 {
		t.Errorf("collector errors: %v", s.Errors)
	}
	if s.CPU == nil || s.Memory == nil || s.Disk == nil {
		t.Errorf("missing data after two rounds: cpu=%v memory=%v disk=%v", s.CPU, s.Memory, s.Disk)
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

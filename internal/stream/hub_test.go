package stream

import (
	"context"
	"testing"
	"time"
)

// wait bounds every blocking step, so a broken Hub fails the test instead
// of hanging it.
const wait = time.Second

func startHub(t *testing.T, queueLen int) (*Hub, context.CancelFunc) {
	t.Helper()
	h := NewHub(queueLen)
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(wait):
			t.Error("Run did not return after cancel")
		}
	})
	return h, cancel
}

// call runs f and fails the test if it does not return in time.
func call(t *testing.T, what string, f func()) {
	t.Helper()
	finished := make(chan struct{})
	go func() {
		f()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(wait):
		t.Fatalf("%s blocked", what)
	}
}

func subscribe(t *testing.T, h *Hub) *client {
	t.Helper()
	var c *client
	call(t, "subscribe", func() { c = h.subscribe() })
	if c == nil {
		t.Fatal("subscribe returned nil")
	}
	return c
}

func recv(t *testing.T, c *client) string {
	t.Helper()
	select {
	case msg, ok := <-c.send:
		if !ok {
			t.Fatal("send queue closed, want a message")
		}
		return string(msg)
	case <-time.After(wait):
		t.Fatal("no message received")
		return ""
	}
}

// closed reports whether c's queue gets closed, draining any queued messages.
func closed(c *client) bool {
	deadline := time.After(wait)
	for {
		select {
		case _, ok := <-c.send:
			if !ok {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func TestBroadcastReachesEveryClient(t *testing.T) {
	h, _ := startHub(t, 4)
	a, b := subscribe(t, h), subscribe(t, h)

	call(t, "Broadcast", func() { h.Broadcast([]byte("one")) })
	call(t, "Broadcast", func() { h.Broadcast([]byte("two")) })

	for name, c := range map[string]*client{"a": a, "b": b} {
		if got := recv(t, c); got != "one" {
			t.Errorf("%s first = %q, want one", name, got)
		}
		if got := recv(t, c); got != "two" {
			t.Errorf("%s second = %q, want two", name, got)
		}
	}
}

func TestLateSubscriberGetsLastMessageFirst(t *testing.T) {
	h, _ := startHub(t, 4)
	early := subscribe(t, h)
	call(t, "Broadcast", func() { h.Broadcast([]byte("old")) })
	call(t, "Broadcast", func() { h.Broadcast([]byte("latest")) })
	recv(t, early)
	recv(t, early) // after this, both have certainly been fanned out

	late := subscribe(t, h)
	call(t, "Broadcast", func() { h.Broadcast([]byte("new")) })
	if got := recv(t, late); got != "latest" {
		t.Errorf("late subscriber first got %q, want the replayed latest", got)
	}
	if got := recv(t, late); got != "new" {
		t.Errorf("late subscriber then got %q, want new", got)
	}
}

func TestFirstSubscriberGetsNothingBeforeBroadcast(t *testing.T) {
	h, _ := startHub(t, 4)
	c := subscribe(t, h)
	select {
	case msg := <-c.send:
		t.Fatalf("got %q before any broadcast", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSlowClientIsEvictedWithoutBlockingOthers(t *testing.T) {
	const queueLen = 2
	h, _ := startHub(t, queueLen)
	slow, fast := subscribe(t, h), subscribe(t, h)

	// slow never reads. One message more than its queue holds must evict it,
	// and neither Broadcast nor fast may notice.
	for i := range queueLen + 1 {
		call(t, "Broadcast", func() { h.Broadcast([]byte{byte('a' + i)}) })
		if got, want := recv(t, fast), string(rune('a'+i)); got != want {
			t.Fatalf("fast got %q, want %q", got, want)
		}
	}

	if !closed(slow) {
		t.Fatal("slow client was not evicted")
	}
	if slow.reason != reasonSlow {
		t.Errorf("reason = %v, want reasonSlow", slow.reason)
	}

	// fast keeps working after the eviction.
	call(t, "Broadcast", func() { h.Broadcast([]byte("after")) })
	if got := recv(t, fast); got != "after" {
		t.Errorf("fast got %q after eviction, want after", got)
	}

	// The handler of the evicted client will still unsubscribe it.
	call(t, "unsubscribe of evicted client", func() { h.unsubscribe(slow) })
}

func TestUnsubscribeClosesQueue(t *testing.T) {
	h, _ := startHub(t, 4)
	c := subscribe(t, h)

	call(t, "unsubscribe", func() { h.unsubscribe(c) })
	if !closed(c) {
		t.Fatal("queue not closed after unsubscribe")
	}
	// A second unsubscribe must not panic on a double close.
	call(t, "second unsubscribe", func() { h.unsubscribe(c) })

	// The Hub keeps serving others.
	other := subscribe(t, h)
	call(t, "Broadcast", func() { h.Broadcast([]byte("x")) })
	recv(t, other)
}

func TestShutdownClosesAllClients(t *testing.T) {
	h, cancel := startHub(t, 4)
	a, b := subscribe(t, h), subscribe(t, h)

	cancel()

	for name, c := range map[string]*client{"a": a, "b": b} {
		if !closed(c) {
			t.Errorf("client %s not closed on shutdown", name)
		} else if c.reason != reasonShutdown {
			t.Errorf("client %s reason = %v, want reasonShutdown", name, c.reason)
		}
	}
	select {
	case <-h.done:
	case <-time.After(wait):
		t.Fatal("Run did not return")
	}
}

func TestCallsAfterShutdownDoNotHang(t *testing.T) {
	h, cancel := startHub(t, 4)
	c := subscribe(t, h)
	cancel()
	<-h.done

	call(t, "Broadcast after shutdown", func() { h.Broadcast([]byte("x")) })
	call(t, "unsubscribe after shutdown", func() { h.unsubscribe(c) })

	late := subscribe(t, h)
	if !closed(late) {
		t.Error("subscribe after shutdown should return an already-closed client")
	}
}

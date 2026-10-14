package stream

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type wsEnv struct {
	hub    *Hub
	srv    *Server
	url    string
	cancel context.CancelFunc
}

func startServer(t *testing.T, queueLen int) *wsEnv {
	t.Helper()
	hub := NewHub(queueLen)
	ctx, cancel := context.WithCancel(context.Background())
	go hub.Run(ctx)

	srv := NewServer(hub, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		cancel()
		<-hub.done
		srv.Wait()
		ts.Close()
	})
	return &wsEnv{hub, srv, "ws" + strings.TrimPrefix(ts.URL, "http"), cancel}
}

func dial(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func read(t *testing.T, conn *websocket.Conn) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	typ, msg, err := conn.Read(ctx)
	if err == nil && typ != websocket.MessageText {
		t.Errorf("message type = %v, want text", typ)
	}
	return string(msg), err
}

// broadcastUntilReceived broadcasts msg until conn has read it. Dial returns
// before the server side has subscribed, so a single early broadcast could
// be missed.
func broadcastUntilReceived(t *testing.T, e *wsEnv, conn *websocket.Conn, msg string) {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		m, _ := read(t, conn)
		got <- m
	}()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		e.hub.Broadcast([]byte(msg))
		select {
		case m := <-got:
			if m != msg {
				t.Fatalf("got %q, want %q", m, msg)
			}
			return
		case <-tick.C:
		}
	}
}

func TestStreamDeliversBroadcasts(t *testing.T) {
	e := startServer(t, 4)
	a, b := dial(t, e.url), dial(t, e.url)

	broadcastUntilReceived(t, e, a, `{"seq":1}`)
	// b may also have received some copies of seq 1 meanwhile; drain them.
	for {
		m, err := read(t, b)
		if err != nil {
			t.Fatalf("b read: %v", err)
		}
		if m == `{"seq":1}` {
			break
		}
	}
}

func TestStreamReplaysLatestOnConnect(t *testing.T) {
	e := startServer(t, 4)
	first := dial(t, e.url)
	broadcastUntilReceived(t, e, first, "latest")

	late := dial(t, e.url)
	m, err := read(t, late)
	if err != nil || m != "latest" {
		t.Fatalf("late client got %q, %v; want the latest message immediately", m, err)
	}
}

func TestStreamClosesGoingAwayOnShutdown(t *testing.T) {
	e := startServer(t, 4)
	conn := dial(t, e.url)
	broadcastUntilReceived(t, e, conn, "hello")

	e.cancel()

	for {
		_, err := read(t, conn)
		if err == nil {
			continue // a broadcast that raced with the shutdown
		}
		if got := websocket.CloseStatus(err); got != websocket.StatusGoingAway {
			t.Fatalf("close status = %v (err %v), want GoingAway", got, err)
		}
		break
	}

	done := make(chan struct{})
	go func() { e.srv.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(wait):
		t.Fatal("Server.Wait did not return after shutdown")
	}
}

func TestStreamEvictsSlowClientWithTryAgainLater(t *testing.T) {
	const queueLen = 1
	e := startServer(t, queueLen)
	slow := dial(t, e.url)
	slow.SetReadLimit(2 << 20)
	broadcastUntilReceived(t, e, slow, "warmup")

	// Large messages fill the TCP buffers quickly, so the server's writer
	// blocks and the Hub-side queue overflows while slow is not reading.
	big := []byte(strings.Repeat("x", 1<<20))
	deadline := time.Now().Add(5 * time.Second)
	for e.hub.clientCount() > 0 {
		if time.Now().After(deadline) {
			t.Fatal("slow client never evicted")
		}
		e.hub.Broadcast(big)
	}

	// Drain whatever was in flight; the connection must end with 1013.
	for {
		_, err := read(t, slow)
		if err == nil {
			continue
		}
		if got := websocket.CloseStatus(err); got != websocket.StatusTryAgainLater {
			t.Fatalf("close status = %v (err %v), want TryAgainLater", got, err)
		}
		return
	}
}

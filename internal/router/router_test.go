package router

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func backend(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s %s %s", body, r.Host, r.Header.Get("X-Forwarded-For"))
	}))
}

func get(t *testing.T, port int) (int, string) {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/x", port))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func newRouter(t *testing.T) *Router {
	r := New(slog.New(slog.DiscardHandler))
	t.Cleanup(func() { r.Close() })
	return r
}

func TestProxiesAndSwitches(t *testing.T) {
	blue, green := backend("blue"), backend("green")
	defer blue.Close()
	defer green.Close()
	r := newRouter(t)
	port := freePort(t)

	if err := r.Set("web", port, blue.URL); err != nil {
		t.Fatal(err)
	}
	code, body := get(t, port)
	if code != 200 || body[:4] != "blue" {
		t.Fatalf("got %d %q", code, body)
	}
	if want := fmt.Sprintf("127.0.0.1:%d", port); !strings.Contains(body, want) {
		t.Errorf("Host not preserved: %q", body)
	}
	if err := r.Set("web", port, green.URL); err != nil {
		t.Fatal(err)
	}
	if _, body := get(t, port); body[:5] != "green" {
		t.Errorf("after switch: %q", body)
	}
	if r.Target("web") != green.URL {
		t.Errorf("target %q", r.Target("web"))
	}
}

// The point of the router: switching under load drops no requests.
func TestSwitchUnderLoadDropsNothing(t *testing.T) {
	blue, green := backend("blue"), backend("green")
	defer blue.Close()
	defer green.Close()
	r := newRouter(t)
	port := freePort(t)
	if err := r.Set("web", port, blue.URL); err != nil {
		t.Fatal(err)
	}

	var failed, served atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	client := &http.Client{Timeout: 2 * time.Second}
	for range 8 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
				if err != nil || resp.StatusCode != 200 {
					failed.Add(1)
				} else {
					served.Add(1)
				}
				if resp != nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
			}
		})
	}
	time.Sleep(100 * time.Millisecond)
	for i := range 20 {
		next := green.URL
		if i%2 == 1 {
			next = blue.URL
		}
		if err := r.Set("web", port, next); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	if failed.Load() != 0 {
		t.Errorf("%d of %d requests failed across 20 switches", failed.Load(), failed.Load()+served.Load())
	}
	t.Logf("%d requests, 20 switches, 0 failed", served.Load())
}

func TestBadGatewayWhenTargetDown(t *testing.T) {
	gone := backend("x")
	gone.Close()
	r := newRouter(t)
	port := freePort(t)
	if err := r.Set("web", port, gone.URL); err != nil {
		t.Fatal(err)
	}
	if code, body := get(t, port); code != http.StatusBadGateway || !strings.Contains(body, "web is not responding") {
		t.Errorf("got %d %q", code, body)
	}
}

func TestSetErrors(t *testing.T) {
	r := newRouter(t)
	port := freePort(t)
	if err := r.Set("web", port, "not a url"); err == nil {
		t.Error("bad target accepted")
	}
	if err := r.Set("web", port, "http://127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	if err := r.Set("web", port+1, "http://127.0.0.1:1"); err == nil {
		t.Error("moving an app to another port accepted")
	}
	if err := r.Set("other", port, "http://127.0.0.1:1"); err == nil {
		t.Error("two apps on one port accepted")
	}
	r.Remove("web")
	if r.Target("web") != "" {
		t.Error("route still there after Remove")
	}
}

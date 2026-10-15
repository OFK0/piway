package router

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func upstream(body string) *httptest.Server {
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
	blue, green := upstream("blue"), upstream("green")
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
	blue, green := upstream("blue"), upstream("green")
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
	gone := upstream("x")
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

func TestOnChangeAndPorts(t *testing.T) {
	blue, green := upstream("blue"), upstream("green")
	defer blue.Close()
	defer green.Close()
	r := newRouter(t)
	var changes atomic.Int32
	r.OnChange(func() {
		changes.Add(1)
		r.Ports() // must not deadlock: called without the lock held
	})
	port := freePort(t)

	if err := r.Set("web", port, blue.URL); err != nil {
		t.Fatal(err)
	}
	if got := r.Ports(); len(got) != 1 || got["web"] != port {
		t.Errorf("Ports = %v, want web:%d", got, port)
	}
	if err := r.Set("web", port, green.URL); err != nil {
		t.Fatal(err)
	}
	if n := changes.Load(); n != 1 {
		t.Errorf("%d changes after a new route and a switch, want 1", n)
	}
	r.Remove("web")
	r.Remove("web")
	if n := changes.Load(); n != 2 || len(r.Ports()) != 0 {
		t.Errorf("%d changes, ports %v after removing; want 2 and none", n, r.Ports())
	}
}

func getPath(t *testing.T, port int, path string) (int, string) {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func site(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestServesSites(t *testing.T) {
	dir := site(t, map[string]string{"index.html": "home", "css/app.css": "body{}"})
	r := newRouter(t)
	port := freePort(t)
	if err := r.SetSite("blog", port, dir, false); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"/": "home", "/css/app.css": "body{}"} {
		if code, body := getPath(t, port, path); code != 200 || body != want {
			t.Errorf("GET %s = %d %q, want 200 %q", path, code, body, want)
		}
	}
	if code, _ := getPath(t, port, "/posts/1"); code != http.StatusNotFound {
		t.Errorf("missing path without spa: %d, want 404", code)
	}
	if code, _ := getPath(t, port, "/../../etc/passwd"); code != http.StatusNotFound {
		t.Errorf("path outside the site: %d, want 404", code)
	}
	if r.Target("blog") != dir {
		t.Errorf("Target = %q, want the directory", r.Target("blog"))
	}
}

func TestSPAFallback(t *testing.T) {
	dir := site(t, map[string]string{"index.html": "app shell", "app.js": "js"})
	r := newRouter(t)
	port := freePort(t)
	if err := r.SetSite("web", port, dir, true); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"/posts/1": "app shell", "/app.js": "js", "/": "app shell"} {
		if code, body := getPath(t, port, path); code != 200 || body != want {
			t.Errorf("GET %s = %d %q, want 200 %q", path, code, body, want)
		}
	}
}

func TestSwitchBetweenContainerAndSite(t *testing.T) {
	blue := upstream("blue")
	defer blue.Close()
	dir := site(t, map[string]string{"index.html": "static"})
	r := newRouter(t)
	var changes atomic.Int32
	r.OnChange(func() { changes.Add(1) })
	port := freePort(t)

	if err := r.Set("app", port, blue.URL); err != nil {
		t.Fatal(err)
	}
	if err := r.SetSite("app", port, dir, false); err != nil {
		t.Fatal(err)
	}
	if code, body := getPath(t, port, "/"); code != 200 || body != "static" {
		t.Errorf("after switching to the site: %d %q", code, body)
	}
	if err := r.Set("app", port, blue.URL); err != nil {
		t.Fatal(err)
	}
	if _, body := get(t, port); !strings.HasPrefix(body, "blue") {
		t.Errorf("after switching back: %q", body)
	}
	if changes.Load() != 1 {
		t.Errorf("%d changes, want 1: switching versions is not a new route", changes.Load())
	}
	if err := r.SetSite("app", port, "relative/dir", false); err == nil {
		t.Error("accepted a relative site directory")
	}
}

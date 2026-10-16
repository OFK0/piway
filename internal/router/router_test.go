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

// getWith fetches path with headers and returns the status, the headers
// and the body.
func getWith(t *testing.T, port int, path string, header map[string]string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

func TestSPAFallback(t *testing.T) {
	dir := site(t, map[string]string{"index.html": "app shell", "app.js": "js"})
	r := newRouter(t)
	port := freePort(t)
	if err := r.SetSite("web", port, dir, true); err != nil {
		t.Fatal(err)
	}
	html := map[string]string{"Accept": "text/html,application/xhtml+xml,*/*;q=0.8"}
	for _, tc := range []struct {
		path   string
		header map[string]string
		code   int
		body   string
	}{
		// Pages: client-side routes get the shell.
		{"/posts/1", nil, 200, "app shell"},
		{"/posts/1", html, 200, "app shell"},
		{"/", nil, 200, "app shell"},
		{"/app.js", nil, 200, "js"},
		// A navigation to a route that looks like a file (/docs/v1.2).
		{"/docs/v1.2", html, 200, "app shell"},
		// Missing files are not pages (#144).
		{"/assets/old.js", nil, 404, ""},
		{"/missing.png", map[string]string{"Accept": "image/avif,image/webp,*/*"}, 404, ""},
		{"/missing.css", map[string]string{"Accept": "text/css,*/*;q=0.1"}, 404, ""},
		// An API client that wants JSON is not served HTML either.
		{"/api/things", map[string]string{"Accept": "application/json"}, 404, ""},
	} {
		code, _, body := getWith(t, port, tc.path, tc.header)
		if code != tc.code || (tc.code == 200 && body != tc.body) {
			t.Errorf("GET %s (Accept %q) = %d %q, want %d %q", tc.path, tc.header["Accept"], code, body, tc.code, tc.body)
		}
	}
}

func TestSiteCacheHeaders(t *testing.T) {
	dir := site(t, map[string]string{
		"index.html":                 "shell",
		"assets/index-BIy_QpcH.js":   "js",
		"assets/index-CnLye_A9.css":  "css",
		"assets/main.3f2a9c1d.js":    "js",
		"css/normalize-document.css": "css",
		"app.js":                     "js",
		"favicon.ico":                "icon",
		"img/photo-2024-05-01.jpg":   "jpg",
	})
	r := newRouter(t)
	port := freePort(t)
	if err := r.SetSite("web", port, dir, true); err != nil {
		t.Fatal(err)
	}
	const forever = "public, max-age=31536000, immutable"
	for path, want := range map[string]string{
		"/assets/index-BIy_QpcH.js":   forever,
		"/assets/index-CnLye_A9.css":  forever,
		"/assets/main.3f2a9c1d.js":    forever,
		"/css/normalize-document.css": "no-cache", // a long word, not a hash
		"/app.js":                     "no-cache",
		"/favicon.ico":                "no-cache",
		"/":                           "no-cache",
		"/index.html":                 "no-cache",
		"/some/route":                 "no-cache", // the fallback
	} {
		_, h, _ := getWith(t, port, path, nil)
		if got := h.Get("Cache-Control"); got != want {
			t.Errorf("GET %s: Cache-Control %q, want %q", path, got, want)
		}
	}
	// A hashed name that is not there is a 404, and never "immutable".
	if code, h, _ := getWith(t, port, "/assets/gone-Zx9Q2mPa.js", nil); code != 404 || strings.Contains(h.Get("Cache-Control"), "immutable") {
		t.Errorf("missing hashed file: %d, Cache-Control %q", code, h.Get("Cache-Control"))
	}
	// A revalidation answers 304 for a file that did not change.
	_, h, _ := getWith(t, port, "/app.js", nil)
	if code, _, _ := getWith(t, port, "/app.js", map[string]string{"If-Modified-Since": h.Get("Last-Modified")}); code != http.StatusNotModified {
		t.Errorf("conditional GET: %d, want 304", code)
	}
}

func TestHashed(t *testing.T) {
	for name, want := range map[string]bool{
		"assets/index-BIy_QpcH.js": true, "main.3f2a9c1d.js": true, "chunk-5KJ2N8QX.mjs": true, "vendor.abcdef12.css": true,
		"normalize-document.css": false, "jquery.min.js": false, "app.js": false, "bootstrap-bundle.js": false,
		"readme-for-everyone.txt": false, "photo-2024-05-01.jpg": false, "a-b.js": false,
	} {
		if got := hashed(name); got != want {
			t.Errorf("hashed(%q) = %v, want %v", name, got, want)
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

// TestProxyReusesConnections: steady concurrent traffic must reuse its
// connections to the app. With the default transport (2 idle per host) it
// opened a new one for most requests, and the sockets left in TIME_WAIT
// used up the ports Docker draws an app's host port from.
func TestProxyReusesConnections(t *testing.T) {
	var conns atomic.Int64
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) }))
	backend.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	backend.Start()
	defer backend.Close()

	r := newRouter(t)
	port := freePort(t)
	if err := r.Set("web", port, backend.URL); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 32}}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
				if err != nil {
					t.Error(err)
					return
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
	if n := conns.Load(); n > 40 {
		t.Errorf("%d connections to the app for 800 requests from 16 clients; want them reused (at most about 16)", n)
	}
}

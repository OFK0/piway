package caddy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCaddy is the part of Caddy's admin API that Proxy uses.
type fakeCaddy struct {
	mu     sync.Mutex
	config []byte // as last loaded, "null" when empty
	loads  int
	fail   string // if set, every request fails with this error
}

func newFake(t *testing.T, config string) (*fakeCaddy, *httptest.Server) {
	f := &fakeCaddy{config: []byte(config)}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeCaddy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": f.fail})
		return
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/config/":
		w.Write(f.config)
	case r.Method == "POST" && r.URL.Path == "/load":
		b, _ := io.ReadAll(r.Body)
		if !json.Valid(b) || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		f.config = b
		f.loads++
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeCaddy) state() (map[string]any, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var m map[string]any
	json.Unmarshal(f.config, &m)
	return m, f.loads
}

func newProxy(t *testing.T, admin string, tls bool) *Proxy {
	t.Helper()
	p, err := New(Options{Admin: admin, Domain: "pi.local", Listen: ":8443", TLS: tls})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// path walks nested objects.
func path(m map[string]any, keys ...string) any {
	var v any = m
	for _, k := range keys {
		o, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = o[k]
	}
	return v
}

// hosts lists each route's host and upstream as "host>dial", and the
// catch-all as "*>404".
func hosts(t *testing.T, cfg map[string]any) []string {
	t.Helper()
	routes, _ := path(cfg, "apps", "http", "servers", ServerName, "routes").([]any)
	var out []string
	for _, r := range routes {
		b, _ := json.Marshal(r)
		var rt struct {
			Match  []struct{ Host []string }
			Handle []struct {
				Upstreams  []struct{ Dial string }
				StatusCode int `json:"status_code"`
			}
		}
		json.Unmarshal(b, &rt)
		switch {
		case len(rt.Match) == 1 && len(rt.Handle) == 1 && len(rt.Handle[0].Upstreams) == 1:
			out = append(out, rt.Match[0].Host[0]+">"+rt.Handle[0].Upstreams[0].Dial)
		case len(rt.Match) == 0 && len(rt.Handle) == 1 && rt.Handle[0].StatusCode == 404:
			out = append(out, "*>404")
		default:
			t.Errorf("unexpected route %s", b)
		}
	}
	return out
}

func TestSyncFromEmptyConfig(t *testing.T) {
	f, srv := newFake(t, "null")
	p := newProxy(t, srv.URL, true)
	if err := p.Sync(context.Background(), map[string]int{"photos": 23000, "blog": 23001}); err != nil {
		t.Fatal(err)
	}
	cfg, loads := f.state()
	got := strings.Join(hosts(t, cfg), " ")
	want := "blog.pi.local>127.0.0.1:23001 photos.pi.local>127.0.0.1:23000 *>404"
	if loads != 1 || got != want {
		t.Errorf("%d loads, routes %q; want 1 and %q", loads, got, want)
	}
	if l := path(cfg, "apps", "http", "servers", ServerName, "listen"); l.([]any)[0] != ":8443" {
		t.Errorf("listen = %v", l)
	}
	if v := path(cfg, "apps", "http", "servers", ServerName, "automatic_https", "disable_redirects"); v != true {
		t.Error("redirects not disabled: they need port 80")
	}
	if v := path(cfg, "apps", "pki", "certificate_authorities", "local", "install_trust"); v != false {
		t.Error("Caddy would try to install its root into the system trust store")
	}
}

func TestSyncKeepsTheRestOfTheConfig(t *testing.T) {
	f, srv := newFake(t, `{
		"admin": {"listen": "localhost:2019"},
		"apps": {
			"http": {"servers": {"other": {"listen": [":9000"]}}},
			"pki": {"certificate_authorities": {"local": {"install_trust": true}}}
		}
	}`)
	p := newProxy(t, srv.URL, true)
	if err := p.Sync(context.Background(), map[string]int{"photos": 23000}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := f.state()
	if path(cfg, "admin", "listen") != "localhost:2019" || path(cfg, "apps", "http", "servers", "other") == nil {
		t.Errorf("other settings lost: %v", cfg)
	}
	if path(cfg, "apps", "pki", "certificate_authorities", "local", "install_trust") != true {
		t.Error("overrode the user's own trust setting")
	}
}

func TestSyncLoadsOnlyChanges(t *testing.T) {
	f, srv := newFake(t, "null")
	p := newProxy(t, srv.URL, true)
	ctx := context.Background()
	apps := map[string]int{"photos": 23000, "blog": 23001}
	for range 3 {
		if err := p.Sync(ctx, apps); err != nil {
			t.Fatal(err)
		}
	}
	if _, loads := f.state(); loads != 1 {
		t.Errorf("%d loads for an unchanged set, want 1", loads)
	}

	delete(apps, "blog")
	if err := p.Sync(ctx, apps); err != nil {
		t.Fatal(err)
	}
	cfg, loads := f.state()
	if got := strings.Join(hosts(t, cfg), " "); loads != 2 || got != "photos.pi.local>127.0.0.1:23000 *>404" {
		t.Errorf("after removing blog: %d loads, routes %q", loads, got)
	}
}

func TestPlainHTTP(t *testing.T) {
	f, srv := newFake(t, "null")
	p := newProxy(t, srv.URL, false)
	if err := p.Sync(context.Background(), map[string]int{"photos": 23000}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := f.state()
	if path(cfg, "apps", "http", "servers", ServerName, "automatic_https", "disable") != true {
		t.Error("automatic HTTPS still on")
	}
	if path(cfg, "apps", "pki") != nil {
		t.Error("touched the PKI app without TLS")
	}
}

func TestSyncErrors(t *testing.T) {
	f, srv := newFake(t, "null")
	f.fail = "loading new config: http app module: start: listen tcp :8443: bind: address already in use"
	p := newProxy(t, srv.URL, true)
	err := p.Sync(context.Background(), map[string]int{"photos": 23000})
	if err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Errorf("err = %v, want Caddy's message", err)
	}

	srv.Close()
	if err := p.Sync(context.Background(), nil); err == nil {
		t.Error("no error with Caddy down")
	}
}

func TestURL(t *testing.T) {
	tests := []struct {
		listen string
		tls    bool
		want   string
	}{
		{":8443", true, "https://photos.pi.local:8443"},
		{":443", true, "https://photos.pi.local"},
		{":80", false, "http://photos.pi.local"},
		{"0.0.0.0:8000", false, "http://photos.pi.local:8000"},
	}
	for _, tt := range tests {
		p, err := New(Options{Admin: "localhost:2019", Domain: "pi.local", Listen: tt.listen, TLS: tt.tls})
		if err != nil {
			t.Fatal(err)
		}
		if got := p.URL("photos"); got != tt.want {
			t.Errorf("URL with %s tls=%v = %q, want %q", tt.listen, tt.tls, got, tt.want)
		}
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := New(Options{Admin: "localhost:2019", Listen: ":8443"}); err == nil {
		t.Error("accepted an empty domain")
	}
	if _, err := New(Options{Admin: "localhost:2019", Domain: "pi.local", Listen: "8443"}); err == nil {
		t.Error("accepted a listen address without a colon")
	}
}

func TestRunRecoversAndFollowsChanges(t *testing.T) {
	f, srv := newFake(t, "null")
	f.fail = "starting"
	p := newProxy(t, srv.URL, true)

	var mu sync.Mutex
	apps := map[string]int{"photos": 23000}
	current := func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]int{}
		for k, v := range apps {
			out[k] = v
		}
		return out
	}
	changed := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx, changed, current, 10*time.Millisecond)
		close(done)
	}()
	defer func() { cancel(); <-done }()

	waitFor := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			cfg, _ := f.state()
			if strings.Join(hosts(t, cfg), " ") == want {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		cfg, _ := f.state()
		t.Fatalf("routes %q, want %q", hosts(t, cfg), want)
	}

	time.Sleep(30 * time.Millisecond) // a few failed attempts
	f.mu.Lock()
	f.fail = ""
	f.mu.Unlock()
	waitFor("photos.pi.local>127.0.0.1:23000 *>404") // retried on the ticker

	mu.Lock()
	apps["blog"] = 23001
	mu.Unlock()
	changed <- struct{}{}
	waitFor("blog.pi.local>127.0.0.1:23001 photos.pi.local>127.0.0.1:23000 *>404")
}

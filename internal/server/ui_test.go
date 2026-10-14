package server

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// A dashboard build in miniature: what Vite emits, with a hashed asset.
var testUI = fstest.MapFS{
	"index.html":            {Data: []byte("<!doctype html><title>piway</title>")},
	"favicon.svg":           {Data: []byte("<svg/>")},
	"assets/index-abc12.js": {Data: []byte("console.log('piway')")},
	// Large and repetitive, so it is worth compressing.
	"assets/app-def34.js":          {Data: []byte(strings.Repeat("export const metric = 'cpu';\n", 200))},
	"assets/plex-latin-0a1b.woff2": {Data: []byte(strings.Repeat("font", 200))},
}

func newUIServer(t *testing.T) http.Handler {
	t.Helper()
	h, err := New(stubSource{}, http.NotFoundHandler(), nil, http.NotFoundHandler(), testUI)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestDashboardRoutes(t *testing.T) {
	h := newUIServer(t)
	index := string(testUI["index.html"].Data)

	tests := []struct {
		name, method, path string
		status             int
		body               string // prefix, empty to skip
		contentType        string // prefix, empty to skip
		cache              string
	}{
		{"root is index", "GET", "/", 200, index, "text/html", cacheRevalidate},
		{"client route falls back to index", "GET", "/design-system", 200, index, "text/html", cacheRevalidate},
		{"nested client route", "GET", "/apps/photo-gallery/logs", 200, index, "text/html", cacheRevalidate},
		{"index.html by name", "GET", "/index.html", 200, index, "text/html", cacheRevalidate},
		{"hashed asset is immutable", "GET", "/assets/index-abc12.js", 200, "console.log", "text/javascript", cacheImmutable},
		{"unhashed file revalidates", "GET", "/favicon.svg", 200, "<svg/>", "image/svg+xml", cacheRevalidate},
		{"missing asset is 404, not index", "GET", "/assets/index-old99.js", 404, "", "", ""},
		{"missing file with extension", "GET", "/robots.txt", 404, "", "", ""},
		{"outside path is just a client route", "GET", "/etc/passwd", 200, index, "text/html", cacheRevalidate},
		{"HEAD has no body", "HEAD", "/", 200, "", "text/html", cacheRevalidate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, h, tt.method, tt.path)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d; body %q", rec.Code, tt.status, rec.Body)
			}
			if tt.body != "" && !strings.HasPrefix(rec.Body.String(), tt.body) {
				t.Errorf("body = %q, want prefix %q", rec.Body, tt.body)
			}
			if tt.method == "HEAD" && rec.Body.Len() != 0 {
				t.Errorf("HEAD body = %q, want empty", rec.Body)
			}
			if ct := rec.Header().Get("Content-Type"); tt.contentType != "" && !strings.HasPrefix(ct, tt.contentType) {
				t.Errorf("Content-Type = %q, want %q", ct, tt.contentType)
			}
			if got := rec.Header().Get("Cache-Control"); got != tt.cache && tt.status == 200 {
				t.Errorf("Cache-Control = %q, want %q", got, tt.cache)
			}
			if tt.status == 200 && rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Error("missing X-Content-Type-Options: nosniff")
			}
		})
	}
}

func TestDashboardPathTraversal(t *testing.T) {
	// The mux redirects unclean paths to their clean form first.
	if rec := serve(t, newUIServer(t), "GET", "/../../etc/passwd"); rec.Code != http.StatusTemporaryRedirect {
		t.Errorf("mux status = %d, want 307 to the cleaned path", rec.Code)
	}

	// The handler itself must not rely on that: called directly with an
	// unclean path, it still only ever serves files from its fs.FS.
	h, err := uiHandler(testUI)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/../../etc/passwd", "/assets/../../../go.mod", "/..%2f..%2fetc/passwd"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.URL.Path = p
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), "root:") || strings.Contains(rec.Body.String(), "module ") {
			t.Errorf("%s leaked a file from outside the dashboard", p)
		}
		if rec.Code == 200 && rec.Body.String() != string(testUI["index.html"].Data) {
			t.Errorf("%s served %q, want index.html or an error", p, rec.Body)
		}
	}
}

func TestDashboardETagRevalidation(t *testing.T) {
	h := newUIServer(t)

	first := serve(t, h, "GET", "/design-system")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on index.html")
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304 for a matching ETag", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("304 has a body: %q", rec.Body)
	}

	// Every file gets its own ETag.
	if other := serve(t, h, "GET", "/favicon.svg").Header().Get("ETag"); other == etag {
		t.Errorf("favicon and index share ETag %s", etag)
	}
}

func TestAPIRoutesNeverFallBackToDashboard(t *testing.T) {
	h := newUIServer(t)

	rec := serve(t, h, "GET", "/api/v2/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body errorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body.Error == "" {
		t.Errorf("want a JSON error body, got %q (%v)", rec.Body, err)
	}

	// Known endpoints keep their method checks with a dashboard mounted at /.
	if rec := serve(t, h, "POST", "/api/v1/snapshot"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/v1/snapshot = %d, want 405", rec.Code)
	}
	if rec := serve(t, h, "GET", "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("/healthz = %d, want 200", rec.Code)
	}
}

func TestDashboardMissing(t *testing.T) {
	h := newServer(t, stubSource{}) // built without -tags embedui

	rec := serve(t, h, "GET", "/")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "make build") {
		t.Errorf("body should say how to build the dashboard: %q", rec.Body)
	}
	if rec := serve(t, h, "GET", "/api/v1/snapshot"); rec.Code == http.StatusNotFound {
		t.Error("API should still work without a dashboard")
	}
}

func TestDashboardWithoutIndexIsAnError(t *testing.T) {
	_, err := New(stubSource{}, http.NotFoundHandler(), nil, http.NotFoundHandler(), fstest.MapFS{"assets/a.js": {}})
	if err == nil {
		t.Fatal("New accepted a dashboard without index.html")
	}
}

func get(t *testing.T, h http.Handler, path string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDashboardGzip(t *testing.T) {
	h := newUIServer(t)
	want := string(testUI["assets/app-def34.js"].Data)

	gz := get(t, h, "/assets/app-def34.js", map[string]string{"Accept-Encoding": "br, gzip"})
	if gz.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", gz.Header().Get("Content-Encoding"))
	}
	if gz.Body.Len() >= len(want)/2 {
		t.Errorf("compressed body is %d bytes for %d raw", gz.Body.Len(), len(want))
	}
	zr, err := gzip.NewReader(gz.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(zr); string(got) != want {
		t.Error("gzip body does not decompress to the file")
	}
	if !strings.Contains(gz.Header().Get("Vary"), "Accept-Encoding") {
		t.Error("missing Vary: Accept-Encoding")
	}
	if !strings.HasPrefix(gz.Header().Get("Content-Type"), "text/javascript") {
		t.Errorf("Content-Type = %q, want the file's type, not gzip", gz.Header().Get("Content-Type"))
	}

	plain := get(t, h, "/assets/app-def34.js", nil)
	if plain.Header().Get("Content-Encoding") != "" || plain.Body.String() != want {
		t.Error("client without Accept-Encoding should get the raw file")
	}
	if plain.Header().Get("ETag") == gz.Header().Get("ETag") {
		t.Error("raw and gzip responses share an ETag")
	}

	// A 304 is per encoding.
	if rec := get(t, h, "/assets/app-def34.js", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": gz.Header().Get("ETag")}); rec.Code != http.StatusNotModified {
		t.Errorf("revalidating the gzip copy = %d, want 304", rec.Code)
	}

	refused := get(t, h, "/assets/app-def34.js", map[string]string{"Accept-Encoding": "gzip;q=0, identity"})
	if refused.Header().Get("Content-Encoding") != "" {
		t.Error("gzip;q=0 must not get gzip")
	}

	font := get(t, h, "/assets/plex-latin-0a1b.woff2", map[string]string{"Accept-Encoding": "gzip"})
	if font.Header().Get("Content-Encoding") != "" || font.Header().Get("Vary") != "" {
		t.Error("fonts are already compressed and must be served as-is")
	}
	tiny := get(t, h, "/favicon.svg", map[string]string{"Accept-Encoding": "gzip"})
	if tiny.Header().Get("Content-Encoding") != "" {
		t.Error("tiny files should not be gzipped")
	}
}

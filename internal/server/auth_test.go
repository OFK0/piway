package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/ofk0/piway/internal/agent"
	"github.com/ofk0/piway/internal/auth"
)

// authServer has auth on and no password set: only loopback gets in.
func authServer(t *testing.T) http.Handler {
	t.Helper()
	a, err := auth.New(auth.Options{PasswordFile: filepath.Join(t.TempDir(), "password")})
	if err != nil {
		t.Fatal(err)
	}
	hooks := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	ui := fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}
	h, err := New(Deps{Snapshots: stubSource{&agent.Snapshot{}}, Webhooks: hooks, UI: ui, Auth: a})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func request(h http.Handler, method, path, remote string, header map[string]string) int {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remote
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code
}

func TestWhatNeedsAuth(t *testing.T) {
	h := authServer(t)
	const lan, local = "192.0.2.7:4000", "127.0.0.1:4000"
	tests := []struct {
		method, path string
		lan          int
	}{
		{"GET", "/api/v1/snapshot", http.StatusForbidden},
		{"GET", "/api/v1/series?metric=x", http.StatusForbidden},
		{"GET", "/api/v1/builds", http.StatusForbidden},
		{"POST", "/api/v1/apps/x/rollback", http.StatusForbidden},
		{"GET", "/api/v1/nope", http.StatusForbidden}, // no hint about what exists
		{"GET", "/metrics", http.StatusForbidden},
		{"GET", "/healthz", http.StatusOK},
		{"GET", "/api/v1/session", http.StatusOK},
		{"POST", "/api/v1/webhooks/github", http.StatusAccepted},
		{"GET", "/", http.StatusOK},
		{"GET", "/apps/photos", http.StatusOK},
	}
	for _, tt := range tests {
		if got := request(h, tt.method, tt.path, lan, nil); got != tt.lan {
			t.Errorf("%s %s from the LAN: %d, want %d", tt.method, tt.path, got, tt.lan)
		}
	}
	if got := request(h, "GET", "/api/v1/snapshot", local, nil); got != http.StatusOK {
		t.Errorf("snapshot from loopback: %d", got)
	}
	if got := request(h, "POST", "/", local, nil); got != http.StatusMethodNotAllowed {
		t.Errorf("POST /: %d, want 405", got)
	}
}

func TestCrossSiteRequestsAreRejected(t *testing.T) {
	h := authServer(t)
	const local = "127.0.0.1:4000"
	evil := map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}
	if got := request(h, "POST", "/api/v1/apps/x/rollback", local, evil); got != http.StatusForbidden {
		t.Errorf("cross-site POST: %d, want 403", got)
	}
	if got := request(h, "POST", "/api/v1/login", local, evil); got != http.StatusForbidden {
		t.Errorf("cross-site login: %d, want 403", got)
	}
	same := map[string]string{"Sec-Fetch-Site": "same-origin"}
	if got := request(h, "POST", "/api/v1/apps/x/rollback", local, same); got == http.StatusForbidden {
		t.Error("same-origin POST rejected")
	}
}

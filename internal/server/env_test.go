package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ofk0/piway/internal/agent"
	"github.com/ofk0/piway/internal/auth"
	"github.com/ofk0/piway/internal/deploy"
	"github.com/ofk0/piway/internal/mcp"
)

func TestEnvValuesOnlyForSessions(t *testing.T) {
	a, err := auth.New(auth.Options{PasswordFile: filepath.Join(t.TempDir(), "password")})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeBuilds{secrets: map[string]string{"API_KEY": "s3cr3t"}}
	h, err := New(Deps{Snapshots: stubSource{&agent.Snapshot{}}, Builds: f, Auth: a})
	if err != nil {
		t.Fatal(err)
	}
	const local = "127.0.0.1:4000" // no password: the Pi itself is signed in
	do := func(method, path, body string, header map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = local
		r.Header.Set("Content-Type", "application/json")
		for k, v := range header {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// An API token, made from the session.
	w := do("POST", "/api/v1/tokens", `{"name":"ci"}`, nil)
	var tok struct {
		Token string `json:"token"`
	}
	json.Unmarshal(w.Body.Bytes(), &tok)
	if tok.Token == "" {
		t.Fatalf("token: %d %s", w.Code, w.Body)
	}
	bearer := map[string]string{"Authorization": "Bearer " + tok.Token}
	viaMCP := map[string]string{"User-Agent": mcp.UserAgent}

	w = do("GET", "/api/v1/apps/guestbook/env", "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "s3cr3t") || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("session: %d %s %q", w.Code, w.Body, w.Header().Get("Cache-Control"))
	}
	for name, hdr := range map[string]map[string]string{"token": bearer, "mcp": viaMCP} {
		w = do("GET", "/api/v1/apps/guestbook/env", "", hdr)
		var env deploy.AppEnv
		json.Unmarshal(w.Body.Bytes(), &env)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "s3cr3t") || env.ValuesShown || !env.Secrets[0].Set {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
		if w := do("PUT", "/api/v1/apps/guestbook/secrets/API_KEY", `{"value":"x"}`, hdr); w.Code != http.StatusForbidden {
			t.Errorf("%s may set a secret: %d", name, w.Code)
		}
		if w := do("DELETE", "/api/v1/apps/guestbook/secrets/API_KEY", "", hdr); w.Code != http.StatusForbidden {
			t.Errorf("%s may remove a secret: %d", name, w.Code)
		}
	}

	w = do("PUT", "/api/v1/apps/guestbook/secrets/API_KEY", `{"value":"new"}`, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"value":"new"`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("set: %d %s", w.Code, w.Body)
	}
	if w := do("PUT", "/api/v1/apps/guestbook/secrets/API_KEY", `{"value":"a\nb"}`, nil); w.Code != http.StatusBadRequest {
		t.Errorf("multi-line: %d", w.Code)
	}
	if w := do("PUT", "/api/v1/apps/guestbook/secrets/API_KEY", `{}`, nil); w.Code != http.StatusBadRequest {
		t.Errorf("no value: %d", w.Code)
	}
	if w := do("PUT", "/api/v1/apps/nope/secrets/API_KEY", `{"value":"x"}`, nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown app: %d", w.Code)
	}
	if w := do("DELETE", "/api/v1/apps/guestbook/secrets/API_KEY", "", nil); w.Code != http.StatusOK || f.secrets["API_KEY"] != "" {
		t.Errorf("remove: %d %v", w.Code, f.secrets)
	}
	if w := do("GET", "/api/v1/apps/nope/env", "", nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown app env: %d", w.Code)
	}
}

func TestEnvWithoutDeploys(t *testing.T) {
	h, err := New(Deps{Snapshots: stubSource{}})
	if err != nil {
		t.Fatal(err)
	}
	if w := call(h, "GET", "/api/v1/apps/guestbook/env", ""); w.Code != http.StatusServiceUnavailable {
		t.Errorf("no deploys: %d", w.Code)
	}
}

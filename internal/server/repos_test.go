package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ofk0/piway/internal/webhook"
)

func reposServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "apps.yaml")
	store, err := webhook.OpenStore(file, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Deps{Snapshots: stubSource{}, Repos: store, WebhookSecretSet: true})
	if err != nil {
		t.Fatal(err)
	}
	return h, file
}

func call(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestReposAddListRemove(t *testing.T) {
	h, file := reposServer(t)

	if w := call(h, "POST", "/api/v1/repos", `{"repo":"OFK0/guestbook"}`); w.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), "branch: main") {
		t.Errorf("branch should default to main:\n%s", b)
	}
	if w := call(h, "POST", "/api/v1/repos", `{"repo":"ofk0/GUESTBOOK","branch":"main"}`); w.Code != http.StatusConflict {
		t.Errorf("duplicate: %d, want 409", w.Code)
	}
	if w := call(h, "POST", "/api/v1/repos", `{"repo":"not-a-repo"}`); w.Code != http.StatusBadRequest {
		t.Errorf("bad name: %d, want 400", w.Code)
	}

	w := call(h, "GET", "/api/v1/repos", "")
	var list struct {
		Repos   []repoEntry
		Webhook struct {
			Path       string
			Configured bool
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Repos) != 1 || list.Repos[0].Repo != "OFK0/guestbook" || !list.Webhook.Configured || list.Webhook.Path != webhookPath {
		t.Errorf("list = %+v", list)
	}

	if w := call(h, "DELETE", "/api/v1/repos", ""); w.Code != http.StatusBadRequest {
		t.Errorf("delete without repo: %d, want 400", w.Code)
	}
	if w := call(h, "DELETE", "/api/v1/repos?repo=OFK0/guestbook", ""); w.Code != http.StatusOK {
		t.Errorf("delete: %d %s", w.Code, w.Body)
	}
	if w := call(h, "DELETE", "/api/v1/repos?repo=OFK0/guestbook", ""); w.Code != http.StatusNotFound {
		t.Errorf("delete again: %d, want 404", w.Code)
	}
}

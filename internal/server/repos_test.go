package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ofk0/piway/internal/gitauth"
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

// fakeAccess is a RepoAccess in memory.
type fakeAccess struct {
	keys    map[string]string
	token   string
	testErr error
	tested  []gitauth.Source
}

func (f *fakeAccess) GenerateKey(repo string) (string, error) {
	if f.keys[repo] == "" {
		f.keys[repo] = "ssh-ed25519 AAAA " + repo
	}
	return f.keys[repo], nil
}

func (f *fakeAccess) PublicKey(repo string) (string, error) {
	if k := f.keys[repo]; k != "" {
		return k, nil
	}
	return "", &gitauth.Error{Kind: gitauth.NoKey, Repo: repo}
}

func (f *fakeAccess) RemoveKey(repo string) error { delete(f.keys, repo); return nil }
func (f *fakeAccess) HasToken() bool              { return f.token != "" }
func (f *fakeAccess) SetToken(tok string) error {
	if tok == "" {
		return errors.New("the token is empty")
	}
	f.token = tok
	return nil
}
func (f *fakeAccess) RemoveToken() error { f.token = ""; return nil }
func (f *fakeAccess) Test(_ context.Context, src gitauth.Source) error {
	f.tested = append(f.tested, src)
	return f.testErr
}

func TestReposPrivate(t *testing.T) {
	file := filepath.Join(t.TempDir(), "apps.yaml")
	store, err := webhook.OpenStore(file, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	access := &fakeAccess{keys: map[string]string{}}
	h, err := New(Deps{Snapshots: stubSource{}, Repos: store, RepoAccess: access})
	if err != nil {
		t.Fatal(err)
	}

	w := call(h, "POST", "/api/v1/repos", `{"repo":"OFK0/secret","auth":"deploy-key"}`)
	var added repoEntry
	json.Unmarshal(w.Body.Bytes(), &added)
	if w.Code != http.StatusCreated || added.PublicKey != "ssh-ed25519 AAAA OFK0/secret" ||
		added.DeployKeyURL != "https://github.com/OFK0/secret/settings/keys/new" || added.Auth != "deploy-key" {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	if w := call(h, "POST", "/api/v1/repos", `{"repo":"OFK0/secret","branch":"dev"}`); w.Code != http.StatusBadRequest {
		t.Errorf("another branch with another auth: %d, want 400", w.Code)
	}
	if w := call(h, "POST", "/api/v1/repos", `{"repo":"OFK0/other","auth":"password"}`); w.Code != http.StatusBadRequest {
		t.Errorf("unknown auth: %d, want 400", w.Code)
	}
	call(h, "POST", "/api/v1/repos", `{"repo":"OFK0/secret","branch":"dev","auth":"deploy-key"}`)

	w = call(h, "GET", "/api/v1/repos", "")
	var list struct {
		Repos   []repoEntry
		Private struct {
			Available bool
			TokenSet  bool `json:"token_set"`
		}
	}
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Repos) != 2 || list.Repos[1].PublicKey == "" || !list.Private.Available || list.Private.TokenSet {
		t.Errorf("list: %s", w.Body)
	}
	if strings.Contains(w.Body.String(), "PRIVATE KEY") {
		t.Error("a private key in the API")
	}

	// Test access: the result names the problem.
	access.testErr = &gitauth.Error{Kind: gitauth.KeyRejected, Repo: "OFK0/secret", Detail: "Permission denied (publickey)."}
	w = call(h, "POST", "/api/v1/repos/test", `{"repo":"ofk0/secret"}`)
	var res accessResult
	json.Unmarshal(w.Body.Bytes(), &res)
	if w.Code != http.StatusOK || res.OK || res.Kind != gitauth.KeyRejected || !strings.Contains(res.Message, "Deploy keys") {
		t.Errorf("test: %d %s", w.Code, w.Body)
	}
	if len(access.tested) != 1 || access.tested[0].Auth != gitauth.DeployKey {
		t.Errorf("tested %+v", access.tested)
	}
	access.testErr = nil
	if w := call(h, "POST", "/api/v1/repos/test", `{"repo":"OFK0/secret"}`); !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Errorf("test ok: %s", w.Body)
	}
	if w := call(h, "POST", "/api/v1/repos/test", `{"repo":"OFK0/nope"}`); w.Code != http.StatusNotFound {
		t.Errorf("test of an unlisted repo: %d", w.Code)
	}

	// The key stays while a branch uses it, and goes with the last one.
	call(h, "DELETE", "/api/v1/repos?repo=OFK0/secret&branch=dev", "")
	if access.keys["OFK0/secret"] == "" {
		t.Error("key removed while main still deploys")
	}
	w = call(h, "DELETE", "/api/v1/repos?repo=OFK0/secret", "")
	if access.keys["OFK0/secret"] != "" || !strings.Contains(w.Body.String(), `"key_removed":true`) {
		t.Errorf("remove: %s; keys %v", w.Body, access.keys)
	}

	// The token.
	if w := call(h, "PUT", "/api/v1/repos/token", `{"token":""}`); w.Code != http.StatusBadRequest {
		t.Errorf("empty token: %d", w.Code)
	}
	if w := call(h, "PUT", "/api/v1/repos/token", `{"token":"github_pat_x"}`); w.Code != http.StatusNoContent || access.token != "github_pat_x" {
		t.Errorf("set token: %d", w.Code)
	}
	if w := call(h, "GET", "/api/v1/repos", ""); strings.Contains(w.Body.String(), "github_pat_x") || !strings.Contains(w.Body.String(), `"token_set":true`) {
		t.Errorf("list after token: %s", w.Body)
	}
	if w := call(h, "DELETE", "/api/v1/repos/token", ""); w.Code != http.StatusNoContent || access.token != "" {
		t.Errorf("remove token: %d", w.Code)
	}
}

func TestReposPrivateUnavailable(t *testing.T) {
	h, _ := reposServer(t)
	if w := call(h, "POST", "/api/v1/repos", `{"repo":"OFK0/secret","auth":"token"}`); w.Code != http.StatusBadRequest {
		t.Errorf("private without access: %d", w.Code)
	}
}

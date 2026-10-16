package webhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const glSHA = "1234567890abcdef1234567890abcdef12345678"

func glBody(path, ref, after, checkout, httpURL string) []byte {
	b, _ := json.Marshal(map[string]any{
		"object_kind": "push", "ref": ref, "after": after, "checkout_sha": checkout,
		"user_username": "ada", "user_name": "Ada",
		"project": map[string]string{"path_with_namespace": path, "git_http_url": httpURL},
	})
	return b
}

type glDelivery struct {
	token, event, uuid string
	body               []byte
}

func glSend(t *testing.T, h http.Handler, d glDelivery) (int, response) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/webhooks/gitlab", bytes.NewReader(d.body))
	if d.token != "" {
		req.Header.Set("X-Gitlab-Token", d.token)
	}
	req.Header.Set("X-Gitlab-Event", d.event)
	if d.uuid != "" {
		req.Header.Set("X-Gitlab-Event-UUID", d.uuid)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp response
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

func glHandler(d Deployer, targets ...Target) *Handler {
	return New(secret, StaticTargets(targets), d, slog.New(slog.DiscardHandler))
}

func TestGitLabPushDeploys(t *testing.T) {
	rec := &recorder{}
	h := glHandler(rec,
		Target{Repo: "Group/Sub/App", Branch: "main", Provider: "gitlab"},
		Target{Repo: "grp/self", Branch: "main", Provider: "gitlab", Host: "git.example.com", Path: "apps/web"})

	body := glBody("group/sub/app", "refs/heads/main", glSHA, "", "https://gitlab.com/group/sub/app.git")
	code, resp := glSend(t, h.GitLab(), glDelivery{secret, "Push Hook", "u1", body})
	want := Request{Repo: "Group/Sub/App", Branch: "main", Commit: glSHA, CloneURL: "https://gitlab.com/group/sub/app.git",
		Pusher: "ada", Delivery: "u1", Provider: "gitlab"}
	if code != http.StatusAccepted || resp.Status != "queued" || len(rec.reqs) != 1 || rec.reqs[0] != want {
		t.Errorf("%d %+v, deploys %+v; want 202 queued and %+v", code, resp, rec.reqs, want)
	}

	// Self-hosted: matched by host, and the app's directory comes along.
	rec.reqs = nil
	body = glBody("grp/self", "refs/heads/main", glSHA, glSHA, "https://git.example.com:8443/grp/self.git")
	if code, _ := glSend(t, h.GitLab(), glDelivery{secret, "Push Hook", "u2", body}); code != http.StatusAccepted ||
		len(rec.reqs) != 1 || rec.reqs[0].Path != "apps/web" || rec.reqs[0].Host != "git.example.com" {
		t.Errorf("self-hosted: %d %+v", code, rec.reqs)
	}
}

func TestGitLabTokenRequired(t *testing.T) {
	rec := &recorder{}
	h := glHandler(rec, Target{Repo: "grp/app", Branch: "main", Provider: "gitlab"})
	body := glBody("grp/app", "refs/heads/main", glSHA, "", "https://gitlab.com/grp/app.git")
	for name, token := range map[string]string{"none": "", "wrong": "not-the-secret", "prefix": secret[:10], "longer": secret + "x"} {
		if code, _ := glSend(t, h.GitLab(), glDelivery{token, "Push Hook", "x-" + name, body}); code != http.StatusUnauthorized {
			t.Errorf("%s token: %d, want 401", name, code)
		}
	}
	// A GitHub signature is not a GitLab token.
	req := httptest.NewRequest("POST", "/api/v1/webhooks/gitlab", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sign(secret, body))
	req.Header.Set("X-Gitlab-Event", "Push Hook")
	rr := httptest.NewRecorder()
	h.GitLab().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized || len(rec.reqs) != 0 {
		t.Errorf("a signature instead of a token: %d, deploys %v", rr.Code, rec.reqs)
	}
	// Without a secret nothing deploys at all.
	off := New("", StaticTargets{{Repo: "grp/app", Branch: "main", Provider: "gitlab"}}, rec, slog.New(slog.DiscardHandler))
	if code, _ := glSend(t, off.GitLab(), glDelivery{"anything", "Push Hook", "", body}); code != http.StatusServiceUnavailable {
		t.Errorf("no webhook_secret: %d, want 503", code)
	}
}

func TestGitLabPushesThatDoNotDeploy(t *testing.T) {
	rec := &recorder{}
	h := glHandler(rec,
		Target{Repo: "grp/app", Branch: "main", Provider: "gitlab"},
		Target{Repo: "grp/self", Branch: "main", Provider: "gitlab", Host: "git.example.com"},
		Target{Repo: "o/gh", Branch: "main"})
	url := "https://gitlab.com/grp/app.git"
	for name, d := range map[string]glDelivery{
		"another branch":   {secret, "Push Hook", "", glBody("grp/app", "refs/heads/dev", glSHA, "", url)},
		"a tag":            {secret, "Tag Push Hook", "", glBody("grp/app", "refs/tags/v1", glSHA, "", url)},
		"a tag ref":        {secret, "Push Hook", "", glBody("grp/app", "refs/tags/v1", glSHA, "", url)},
		"a deleted branch": {secret, "Push Hook", "", glBody("grp/app", "refs/heads/main", zeros, "", url)},
		"an unknown repo":  {secret, "Push Hook", "", glBody("grp/other", "refs/heads/main", glSHA, "", url)},
		"another server":   {secret, "Push Hook", "", glBody("grp/self", "refs/heads/main", glSHA, "", "https://elsewhere.example.org/grp/self.git")},
		"a GitHub target":  {secret, "Push Hook", "", glBody("o/gh", "refs/heads/main", glSHA, "", "https://gitlab.com/o/gh.git")},
		"a merge request":  {secret, "Merge Request Hook", "", glBody("grp/app", "refs/heads/main", glSHA, "", url)},
	} {
		if code, resp := glSend(t, h.GitLab(), d); code != http.StatusOK || resp.Status != "ignored" {
			t.Errorf("%s: %d %+v, want 200 ignored", name, code, resp)
		}
	}
	if len(rec.reqs) != 0 {
		t.Errorf("deployed %+v", rec.reqs)
	}
	if code, _ := glSend(t, h.GitLab(), glDelivery{secret, "Push Hook", "", []byte("not json")}); code != http.StatusBadRequest {
		t.Errorf("invalid payload: %d, want 400", code)
	}

	// The GitHub endpoint does not deploy GitLab targets either.
	gh := &recorder{}
	hh := New(secret, StaticTargets{{Repo: "grp/app", Branch: "main", Provider: "gitlab"}}, gh, slog.New(slog.DiscardHandler))
	body := pushBody("grp/app", "refs/heads/main", "abc")
	if code, resp := send(t, hh, delivery{"push", "d1", sign(secret, body), body}); code != http.StatusOK || resp.Status != "ignored" || len(gh.reqs) != 0 {
		t.Errorf("a GitHub push for a GitLab repository: %d %+v %v", code, resp, gh.reqs)
	}
}

func TestGitLabReplayAndRetry(t *testing.T) {
	rec := &recorder{}
	h := glHandler(rec, Target{Repo: "grp/app", Branch: "main", Provider: "gitlab"})
	body := glBody("grp/app", "refs/heads/main", glSHA, "", "https://gitlab.com/grp/app.git")
	glSend(t, h.GitLab(), glDelivery{secret, "Push Hook", "same", body})
	if code, resp := glSend(t, h.GitLab(), glDelivery{secret, "Push Hook", "same", body}); code != http.StatusOK || resp.Status != "duplicate" || len(rec.reqs) != 1 {
		t.Errorf("a replay: %d %+v, deploys %d", code, resp, len(rec.reqs))
	}
	// A delivery whose deploy failed can be sent again.
	rec.err = errors.New("queue full")
	if code, _ := glSend(t, h.GitLab(), glDelivery{secret, "Push Hook", "retry", body}); code != http.StatusServiceUnavailable {
		t.Fatalf("a failing deployer: %d", code)
	}
	rec.err = nil
	if code, resp := glSend(t, h.GitLab(), glDelivery{secret, "Push Hook", "retry", body}); code != http.StatusAccepted {
		t.Errorf("the retry: %d %+v", code, resp)
	}
}

func TestGitLabTargets(t *testing.T) {
	for name, src := range map[string]string{
		"nested group": "deploy:\n  - {repo: grp/sub/app, branch: main, provider: gitlab}\n",
		"self-hosted":  "deploy:\n  - {repo: grp/app, branch: main, provider: gitlab, host: git.example.com, auth: deploy-key}\n",
		"explicit":     "deploy:\n  - {repo: o/app, branch: main, provider: github}\n",
	} {
		if got, err := LoadTargets(writeApps(t, src)); err != nil || len(got) != 1 {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
	for name, src := range map[string]string{
		"a nested path on GitHub":   "deploy:\n  - {repo: o/sub/app, branch: main}\n",
		"a bad provider":            "deploy:\n  - {repo: o/app, branch: main, provider: bitbucket}\n",
		"a host without gitlab":     "deploy:\n  - {repo: o/app, branch: main, host: git.example.com}\n",
		"a host with a scheme":      "deploy:\n  - {repo: g/app, branch: main, provider: gitlab, host: https://git.example.com}\n",
		"a host with a port":        "deploy:\n  - {repo: g/app, branch: main, provider: gitlab, host: git.example.com:2222}\n",
		"a token for GitLab":        "deploy:\n  - {repo: g/app, branch: main, provider: gitlab, auth: token}\n",
		"a traversing GitLab path":  "deploy:\n  - {repo: g/../app, branch: main, provider: gitlab}\n",
		"one name on two providers": "deploy:\n  - {repo: g/app, branch: main}\n  - {repo: g/app, branch: dev, provider: gitlab}\n",
		"one name on two servers":   "deploy:\n  - {repo: g/app, branch: main, provider: gitlab}\n  - {repo: g/app, branch: dev, provider: gitlab, host: git.example.com}\n",
	} {
		if _, err := LoadTargets(writeApps(t, src)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestStoreGitLab(t *testing.T) {
	s, p := openStore(t, "deploy: []\n")
	if err := s.Add(Target{Repo: "grp/sub/app", Branch: "main", Provider: "gitlab", Host: "git.example.com", Auth: "deploy-key"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	for _, want := range []string{"provider: gitlab", "host: git.example.com", "auth: deploy-key"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("file lacks %q:\n%s", want, b)
		}
	}
	got := s.Targets()
	if len(got) != 1 || !got[0].IsGitLab() || got[0].Host != "git.example.com" {
		t.Errorf("targets %+v", got)
	}
	if tg, ok := TargetOf(s.Targets(), "GRP/sub/APP"); !ok || tg.Provider != "gitlab" {
		t.Errorf("TargetOf = %+v %v", tg, ok)
	}
	if src := got[0].Source("https://x/y.git"); src.Provider != "gitlab" || src.Host != "git.example.com" || src.Auth != "deploy-key" || src.URL != "https://x/y.git" {
		t.Errorf("Source = %+v", src)
	}
}

package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const secret = "test-secret-0123456789"

type recorder struct {
	reqs []Request
	err  error
}

func (r *recorder) Deploy(req Request) error {
	r.reqs = append(r.reqs, req)
	return r.err
}

func sign(key string, body []byte) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func pushBody(repo, ref, after string) []byte {
	b, _ := json.Marshal(map[string]any{
		"ref":        ref,
		"after":      after,
		"deleted":    false,
		"repository": map[string]string{"full_name": repo, "clone_url": "https://github.com/" + repo + ".git"},
		"pusher":     map[string]string{"name": "octocat"},
	})
	return b
}

type delivery struct {
	event, id, sig string
	body           []byte
}

func send(t *testing.T, h http.Handler, d delivery) (int, response) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/webhooks/github", bytes.NewReader(d.body))
	req.Header.Set("X-GitHub-Event", d.event)
	req.Header.Set("X-GitHub-Delivery", d.id)
	if d.sig != "" {
		req.Header.Set("X-Hub-Signature-256", d.sig)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp response
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

func newHandler(d Deployer) *Handler {
	return New(secret, StaticTargets{{Repo: "OFK0/guestbook", Branch: "main"}}, d, slog.New(slog.DiscardHandler))
}

func TestPushDeploys(t *testing.T) {
	rec := &recorder{}
	body := pushBody("OFK0/guestbook", "refs/heads/main", "abc123")
	code, resp := send(t, newHandler(rec), delivery{"push", "d1", sign(secret, body), body})

	if code != http.StatusAccepted || resp.Status != "queued" {
		t.Fatalf("got %d %+v, want 202 queued", code, resp)
	}
	want := Request{Repo: "OFK0/guestbook", Branch: "main", Commit: "abc123",
		CloneURL: "https://github.com/OFK0/guestbook.git", Pusher: "octocat", Delivery: "d1"}
	if len(rec.reqs) != 1 || rec.reqs[0] != want {
		t.Errorf("deploys = %+v, want %+v", rec.reqs, want)
	}
}

func TestSignatureRequired(t *testing.T) {
	body := pushBody("OFK0/guestbook", "refs/heads/main", "abc")
	tampered := bytes.Replace(body, []byte("abc"), []byte("evil"), 1)
	tests := map[string]string{
		"missing":          "",
		"no prefix":        strings.TrimPrefix(sign(secret, body), "sha256="),
		"sha1 header":      "sha1=" + strings.TrimPrefix(sign(secret, body), "sha256="),
		"not hex":          "sha256=zz",
		"truncated":        sign(secret, body)[:30],
		"wrong secret":     sign("another-secret-0123456789", body),
		"body changed":     sign(secret, body), // sent with the tampered body below
		"empty after hash": "sha256=",
	}
	for name, sig := range tests {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{}
			b := body
			if name == "body changed" {
				b = tampered
			}
			code, resp := send(t, newHandler(rec), delivery{"push", "d-" + name, sig, b})
			if code != http.StatusUnauthorized || resp.Status != "rejected" {
				t.Errorf("got %d %+v, want 401 rejected", code, resp)
			}
			if len(rec.reqs) != 0 {
				t.Errorf("deployed despite an invalid signature: %+v", rec.reqs)
			}
		})
	}
}

func TestReplayIsNotRedeployed(t *testing.T) {
	rec := &recorder{}
	h := newHandler(rec)
	body := pushBody("OFK0/guestbook", "refs/heads/main", "abc")
	d := delivery{"push", "same-id", sign(secret, body), body}

	if code, _ := send(t, h, d); code != http.StatusAccepted {
		t.Fatalf("first delivery: %d", code)
	}
	code, resp := send(t, h, d)
	if code != http.StatusOK || resp.Status != "duplicate" {
		t.Errorf("replay: %d %+v, want 200 duplicate", code, resp)
	}
	if len(rec.reqs) != 1 {
		t.Errorf("%d deploys, want 1", len(rec.reqs))
	}
}

func TestForgedRequestsDoNotPoisonTheReplayCache(t *testing.T) {
	rec := &recorder{}
	h := newHandler(rec)
	body := pushBody("OFK0/guestbook", "refs/heads/main", "abc")
	// An attacker who guesses the next delivery id sends it unsigned first.
	send(t, h, delivery{"push", "future-id", "sha256=00", body})
	if code, _ := send(t, h, delivery{"push", "future-id", sign(secret, body), body}); code != http.StatusAccepted {
		t.Errorf("the real delivery got %d, want 202", code)
	}
}

func TestReplayCacheForgetsOldest(t *testing.T) {
	c := newRecent(2)
	for _, id := range []string{"a", "b", "c"} {
		if !c.add(id) {
			t.Fatalf("%s reported as seen", id)
		}
	}
	if c.add("c") || c.add("b") {
		t.Error("recent ids not remembered")
	}
	if !c.add("a") {
		t.Error("oldest id still remembered beyond capacity")
	}
}

func TestPushesThatDoNotDeploy(t *testing.T) {
	tests := []struct {
		name, repo, ref string
		deleted         bool
		reason          string
	}{
		{"other branch", "OFK0/guestbook", "refs/heads/feature", false, "not configured"},
		{"other repo", "OFK0/blog", "refs/heads/main", false, "not configured"},
		{"tag", "OFK0/guestbook", "refs/tags/v1.0.0", false, "not a branch push"},
		{"branch deleted", "OFK0/guestbook", "refs/heads/main", true, "deleted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{}
			var ev map[string]any
			json.Unmarshal(pushBody(tt.repo, tt.ref, "abc"), &ev)
			ev["deleted"] = tt.deleted
			body, _ := json.Marshal(ev)
			code, resp := send(t, newHandler(rec), delivery{"push", "d", sign(secret, body), body})
			if code != http.StatusOK || resp.Status != "ignored" || !strings.Contains(resp.Reason, tt.reason) {
				t.Errorf("got %d %+v, want 200 ignored (%s)", code, resp, tt.reason)
			}
			if len(rec.reqs) != 0 {
				t.Errorf("deployed: %+v", rec.reqs)
			}
		})
	}
}

func TestRepoMatchIsCaseInsensitive(t *testing.T) {
	rec := &recorder{}
	body := pushBody("ofk0/GuestBook", "refs/heads/main", "abc")
	if code, _ := send(t, newHandler(rec), delivery{"push", "d", sign(secret, body), body}); code != http.StatusAccepted {
		t.Errorf("got %d, want 202: GitHub owner and repo names are case-insensitive", code)
	}
}

func TestPingAndOtherEvents(t *testing.T) {
	h := newHandler(&recorder{})
	body := []byte(`{"zen":"Keep it logically awesome."}`)
	if code, resp := send(t, h, delivery{"ping", "p", sign(secret, body), body}); code != 200 || resp.Status != "pong" {
		t.Errorf("ping: %d %+v", code, resp)
	}
	if code, resp := send(t, h, delivery{"issues", "i", sign(secret, body), body}); code != 200 || resp.Status != "ignored" {
		t.Errorf("issues: %d %+v", code, resp)
	}
}

func TestBodyLimit(t *testing.T) {
	body := bytes.Repeat([]byte("x"), maxBody+1)
	code, _ := send(t, newHandler(&recorder{}), delivery{"push", "big", sign(secret, body), body})
	if code != http.StatusRequestEntityTooLarge {
		t.Errorf("got %d, want 413", code)
	}
}

func TestDisabledWithoutSecret(t *testing.T) {
	rec := &recorder{}
	h := New("", StaticTargets{{Repo: "OFK0/guestbook", Branch: "main"}}, rec, slog.New(slog.DiscardHandler))
	body := pushBody("OFK0/guestbook", "refs/heads/main", "abc")
	// Even a body "signed" with the empty secret must not deploy.
	code, _ := send(t, h, delivery{"push", "d", sign("", body), body})
	if code != http.StatusServiceUnavailable || len(rec.reqs) != 0 {
		t.Errorf("got %d with %d deploys, want 503 and none", code, len(rec.reqs))
	}
}

func TestDeployerFailure(t *testing.T) {
	rec := &recorder{err: errors.New("queue full")}
	body := pushBody("OFK0/guestbook", "refs/heads/main", "abc")
	code, resp := send(t, newHandler(rec), delivery{"push", "d", sign(secret, body), body})
	if code != http.StatusServiceUnavailable || !strings.Contains(resp.Reason, "queue full") {
		t.Errorf("got %d %+v, want 503 with the reason", code, resp)
	}
}

func TestFormEncodedPayload(t *testing.T) {
	// GitHub's default content type: the JSON is in a "payload" form field,
	// and the signature covers the form-encoded body.
	rec := &recorder{}
	payload := pushBody("OFK0/guestbook", "refs/heads/main", "abc123")
	body := []byte("payload=" + url.QueryEscape(string(payload)))
	req := httptest.NewRequest("POST", "/api/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "form-1")
	req.Header.Set("X-Hub-Signature-256", sign(secret, body))
	w := httptest.NewRecorder()
	newHandler(rec).ServeHTTP(w, req)
	if w.Code != http.StatusAccepted || len(rec.reqs) != 1 || rec.reqs[0].Commit != "abc123" {
		t.Errorf("got %d, deploys %+v; want 202 and the commit", w.Code, rec.reqs)
	}

	// Signing the inner JSON instead of the form body must fail.
	req = httptest.NewRequest("POST", "/api/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", sign(secret, payload))
	w = httptest.NewRecorder()
	newHandler(&recorder{}).ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("signature over the inner JSON accepted: %d", w.Code)
	}
}

func TestDeployerFailureCanBeRetried(t *testing.T) {
	rec := &recorder{err: errors.New("queue full")}
	h := newHandler(rec)
	body := pushBody("OFK0/guestbook", "refs/heads/main", "abc")
	d := delivery{"push", "retry-id", sign(secret, body), body}
	if code, _ := send(t, h, d); code != http.StatusServiceUnavailable {
		t.Fatalf("first delivery: %d, want 503", code)
	}
	rec.err = nil
	if code, resp := send(t, h, d); code != http.StatusAccepted {
		t.Fatalf("retry: %d %+v, want 202 queued", code, resp)
	}
	if code, resp := send(t, h, d); code != http.StatusOK || resp.Status != "duplicate" {
		t.Errorf("after success: %d %+v, want 200 duplicate", code, resp)
	}
	if len(rec.reqs) != 2 {
		t.Errorf("%d queue attempts, want 2", len(rec.reqs))
	}
}

func TestReplayCacheRetrySurvivesOldSlotEviction(t *testing.T) {
	c := newRecent(3)
	c.add("retry")
	c.remove("retry")
	c.add("retry")
	c.add("other")
	c.add("new") // wraps over the failed attempt's old slot
	if c.add("retry") {
		t.Error("successful retry forgotten when the failed slot was evicted")
	}
}

type blockingDeployer struct {
	entered chan struct{}
	release chan struct{}
}

func (d *blockingDeployer) Deploy(Request) error {
	close(d.entered)
	<-d.release
	return nil
}

func TestConcurrentReplayIsNotQueued(t *testing.T) {
	d := &blockingDeployer{entered: make(chan struct{}), release: make(chan struct{})}
	h := newHandler(d)
	body := pushBody("OFK0/guestbook", "refs/heads/main", "abc")
	del := delivery{"push", "in-flight", sign(secret, body), body}
	finished := make(chan int, 1)
	go func() { code, _ := send(t, h, del); finished <- code }()
	defer func() {
		close(d.release)
		select {
		case code := <-finished:
			if code != http.StatusAccepted {
				t.Errorf("first delivery: %d, want 202", code)
			}
		case <-time.After(time.Second):
			t.Error("first delivery did not finish")
		}
	}()
	select {
	case <-d.entered:
	case <-time.After(time.Second):
		t.Fatal("first delivery did not reach Deploy")
	}
	if code, resp := send(t, h, del); code != http.StatusOK || resp.Status != "duplicate" {
		t.Errorf("concurrent replay: %d %+v, want 200 duplicate", code, resp)
	}
}

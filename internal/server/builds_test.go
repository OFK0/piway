package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ofk0/piway/internal/deploy"
)

type fakeBuilds struct {
	builds    []deploy.Build
	past      string
	live      chan []byte
	cancelled string
	cancelErr error
}

func (f *fakeBuilds) List() []deploy.Build { return f.builds }

func (f *fakeBuilds) Get(id string) (deploy.Build, error) {
	for _, b := range f.builds {
		if b.ID == id {
			return b, nil
		}
	}
	return deploy.Build{}, deploy.ErrNotFound
}

func (f *fakeBuilds) Cancel(id string) error {
	if _, err := f.Get(id); err != nil {
		return err
	}
	f.cancelled = id
	return f.cancelErr
}

func (f *fakeBuilds) Follow(id string) ([]byte, <-chan []byte, func(), error) {
	if _, err := f.Get(id); err != nil {
		return nil, nil, nil, err
	}
	return []byte(f.past), f.live, func() {}, nil
}

func (f *fakeBuilds) Rollback(app string) (deploy.Build, error) {
	switch app {
	case "guestbook":
		return deploy.Build{ID: "rb1", App: app, Kind: deploy.KindRollback, State: deploy.Queued}, nil
	case "fresh":
		return deploy.Build{}, deploy.ErrNoRollback
	}
	return deploy.Build{}, deploy.ErrNotFound
}

func (f *fakeBuilds) Redeploy(app string) (deploy.Build, error) {
	if app != "guestbook" {
		return deploy.Build{}, deploy.ErrNotFound
	}
	return deploy.Build{ID: "rd1", App: app, State: deploy.Queued, Pusher: "dashboard"}, nil
}

func (f *fakeBuilds) Apps(context.Context) ([]deploy.App, error) {
	return []deploy.App{{Name: "guestbook", Port: 25000, Container: &deploy.Container{State: "running"}}}, nil
}

func (f *fakeBuilds) Restart(app string) (deploy.Build, error) {
	if app != "guestbook" {
		return deploy.Build{}, deploy.ErrNotFound
	}
	return deploy.Build{ID: "rs1", App: app, Kind: deploy.KindRestart, State: deploy.Queued}, nil
}

func (f *fakeBuilds) DeployNow(_ context.Context, r deploy.DeployRequest, pusher string) (deploy.Build, error) {
	switch {
	case r.Commit == "bad":
		return deploy.Build{}, deploy.ErrBadCommit
	case r.Repo == "OFK0/offline":
		return deploy.Build{}, fmt.Errorf("%w: git ls-remote: could not resolve host", deploy.ErrNoHead)
	case r.App == "guestbook" || r.Repo == "OFK0/guestbook":
		return deploy.Build{ID: "d1", Repo: "OFK0/guestbook", Branch: "main", Commit: r.Commit, Pusher: pusher, State: deploy.Queued}, nil
	}
	return deploy.Build{}, deploy.ErrUnknownTarget
}

func (f *fakeBuilds) Targets() []deploy.Target {
	return []deploy.Target{{Repo: "OFK0/guestbook", Branch: "main", App: "guestbook"}}
}

func (f *fakeBuilds) Logs(_ context.Context, app string, lines int) (string, error) {
	switch app {
	case "guestbook":
		return fmt.Sprintf("last %d lines\n", lines), nil
	case "blog":
		return "", deploy.ErrNotRunning
	}
	return "", deploy.ErrNotFound
}

func buildsServer(t *testing.T, b Builds) http.Handler {
	t.Helper()
	h, err := New(Deps{Snapshots: stubSource{}, Builds: b})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestBuildsAPI(t *testing.T) {
	f := &fakeBuilds{
		builds: []deploy.Build{{ID: "b2", App: "guestbook", State: deploy.Running}, {ID: "b1", State: deploy.Succeeded}},
		past:   "==> Fetching\n",
	}
	h := buildsServer(t, f)

	rec := serve(t, h, "GET", "/api/v1/builds")
	var list struct{ Builds []deploy.Build }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Builds) != 2 || list.Builds[0].ID != "b2" {
		t.Errorf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := serve(t, h, "GET", "/api/v1/builds/b2"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"app":"guestbook"`) {
		t.Errorf("get: %d %s", rec.Code, rec.Body)
	}
	if rec := serve(t, h, "GET", "/api/v1/builds/nope"); rec.Code != 404 {
		t.Errorf("unknown build: %d", rec.Code)
	}
	rec = serve(t, h, "GET", "/api/v1/builds/b1/log")
	if rec.Code != 200 || rec.Body.String() != "==> Fetching\n" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("log: %d %q %s", rec.Code, rec.Body, rec.Header().Get("Content-Type"))
	}
	if rec := serve(t, h, "POST", "/api/v1/builds/b2/cancel"); rec.Code != 202 || f.cancelled != "b2" {
		t.Errorf("cancel: %d, cancelled %q", rec.Code, f.cancelled)
	}
	f.cancelErr = errors.New("deploy: build b1 already succeeded")
	if rec := serve(t, h, "POST", "/api/v1/builds/b1/cancel"); rec.Code != 409 {
		t.Errorf("cancel finished build: %d", rec.Code)
	}
	if rec := serve(t, h, "POST", "/api/v1/builds/nope/cancel"); rec.Code != 404 {
		t.Errorf("cancel unknown: %d", rec.Code)
	}
}

func TestBuildsDisabled(t *testing.T) {
	h := buildsServer(t, nil)
	if rec := serve(t, h, "GET", "/api/v1/builds"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("without Docker: %d, want 503", rec.Code)
	}
}

func TestBuildLogStream(t *testing.T) {
	f := &fakeBuilds{builds: []deploy.Build{{ID: "b1", State: deploy.Running}}, past: "history\n", live: make(chan []byte, 2)}
	ts := httptest.NewServer(buildsServer(t, f))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/api/v1/builds/b1/log/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	f.live <- []byte("step 1\n")
	f.live <- []byte("step 2\n")
	close(f.live) // the build ends

	var got strings.Builder
	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				t.Fatalf("closed with %v, want a normal closure", err)
			}
			break
		}
		got.Write(msg)
	}
	if got.String() != "history\nstep 1\nstep 2\n" {
		t.Errorf("streamed %q", got.String())
	}
}

func TestRollbackAPI(t *testing.T) {
	h := buildsServer(t, &fakeBuilds{})
	if rec := serve(t, h, "POST", "/api/v1/apps/guestbook/rollback"); rec.Code != 202 || !strings.Contains(rec.Body.String(), `"kind":"rollback"`) {
		t.Errorf("rollback: %d %s", rec.Code, rec.Body)
	}
	if rec := serve(t, h, "POST", "/api/v1/apps/fresh/rollback"); rec.Code != 409 {
		t.Errorf("nothing to roll back: %d", rec.Code)
	}
	if rec := serve(t, h, "POST", "/api/v1/apps/nope/rollback"); rec.Code != 404 {
		t.Errorf("unknown app: %d", rec.Code)
	}
}

func TestAppsAPI(t *testing.T) {
	h := buildsServer(t, &fakeBuilds{})
	rec := serve(t, h, "GET", "/api/v1/apps")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"guestbook"`) || !strings.Contains(rec.Body.String(), `"state":"running"`) {
		t.Errorf("apps: %d %s", rec.Code, rec.Body)
	}
	if rec := serve(t, h, "POST", "/api/v1/apps/guestbook/redeploy"); rec.Code != 202 || !strings.Contains(rec.Body.String(), `"pusher":"dashboard"`) {
		t.Errorf("redeploy: %d %s", rec.Code, rec.Body)
	}
	if rec := serve(t, h, "POST", "/api/v1/apps/nope/redeploy"); rec.Code != 404 {
		t.Errorf("redeploy unknown: %d", rec.Code)
	}
}

func TestAppOperationsAPI(t *testing.T) {
	h := buildsServer(t, &fakeBuilds{})
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	tests := []struct {
		method, path, body string
		code               int
		contains           string
	}{
		{"POST", "/api/v1/apps/guestbook/restart", "", 202, `"kind":"restart"`},
		{"POST", "/api/v1/apps/nope/restart", "", 404, ""},
		{"POST", "/api/v1/apps/guestbook/deploy", "", 202, `"pusher":"api"`},
		{"POST", "/api/v1/apps/guestbook/deploy", `{"commit":"bad"}`, 400, "40-character"},
		{"POST", "/api/v1/apps/guestbook/deploy", `{nope`, 400, ""},
		{"POST", "/api/v1/apps/nope/deploy", "", 404, ""},
		{"POST", "/api/v1/deploys", `{"repo":"OFK0/guestbook","branch":"main"}`, 202, `"id":"d1"`},
		{"POST", "/api/v1/deploys", `{"repo":"OFK0/offline","branch":"main"}`, 502, "could not resolve host"},
		{"POST", "/api/v1/deploys", `{"repo":"OFK0/guestbook"}`, 400, ""},
		{"GET", "/api/v1/targets", "", 200, `"app":"guestbook"`},
		{"GET", "/api/v1/apps/guestbook/logs", "", 200, "last 100 lines"},
		{"GET", "/api/v1/apps/guestbook/logs?lines=7", "", 200, "last 7 lines"},
		{"GET", "/api/v1/apps/guestbook/logs?lines=0", "", 400, ""},
		{"GET", "/api/v1/apps/blog/logs", "", 409, ""},
		{"GET", "/api/v1/apps/nope/logs", "", 404, ""},
	}
	for _, tt := range tests {
		w := do(tt.method, tt.path, tt.body)
		if w.Code != tt.code || !strings.Contains(w.Body.String(), tt.contains) {
			t.Errorf("%s %s %s: %d %s, want %d containing %q", tt.method, tt.path, tt.body, w.Code, w.Body, tt.code, tt.contains)
		}
	}
}

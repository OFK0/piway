package server

import (
	"context"
	"encoding/json"
	"errors"
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

package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/ofk0/piway/internal/deploy"
)

// Builds is the build pipeline as the API sees it. *deploy.Pipeline
// implements it.
type Builds interface {
	List() []deploy.Build
	Get(id string) (deploy.Build, error)
	Cancel(id string) error
	Follow(id string) (past []byte, next <-chan []byte, stop func(), err error)
	Rollback(app string) (deploy.Build, error)
	Redeploy(app string) (deploy.Build, error)
	Apps(ctx context.Context) ([]deploy.App, error)
}

func buildRoutes(mux *http.ServeMux, b Builds) {
	h := func(f func(http.ResponseWriter, *http.Request, Builds)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if b == nil {
				writeError(w, http.StatusServiceUnavailable, "deploys are disabled (no Docker daemon, see docs/DOCKER.md)")
				return
			}
			f(w, r, b)
		}
	}
	mux.HandleFunc("GET /api/v1/builds", h(listBuilds))
	mux.HandleFunc("GET /api/v1/builds/{id}", h(getBuild))
	mux.HandleFunc("GET /api/v1/builds/{id}/log", h(buildLog))
	mux.HandleFunc("GET /api/v1/builds/{id}/log/stream", h(streamBuildLog))
	mux.HandleFunc("POST /api/v1/builds/{id}/cancel", h(cancelBuild))
	mux.HandleFunc("GET /api/v1/apps", h(listApps))
	mux.HandleFunc("POST /api/v1/apps/{name}/rollback", h(rollback))
	mux.HandleFunc("POST /api/v1/apps/{name}/redeploy", h(redeploy))
}

func listBuilds(w http.ResponseWriter, _ *http.Request, b Builds) {
	list := b.List()
	if len(list) > 50 {
		list = list[:50]
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"builds": list})
}

func getBuild(w http.ResponseWriter, r *http.Request, b Builds) {
	build, err := b.Get(r.PathValue("id"))
	if err != nil {
		writeBuildError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, build)
}

func buildLog(w http.ResponseWriter, r *http.Request, b Builds) {
	past, _, stop, err := b.Follow(r.PathValue("id"))
	if err != nil {
		writeBuildError(w, err)
		return
	}
	stop()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(past)
}

func cancelBuild(w http.ResponseWriter, r *http.Request, b Builds) {
	if err := b.Cancel(r.PathValue("id")); err != nil {
		if errors.Is(err, deploy.ErrNotFound) {
			writeBuildError(w, err)
			return
		}
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "cancelling"})
}

func listApps(w http.ResponseWriter, r *http.Request, b Builds) {
	apps, err := b.Apps(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"apps": apps})
}

func redeploy(w http.ResponseWriter, r *http.Request, b Builds) {
	build, err := b.Redeploy(r.PathValue("name"))
	switch {
	case errors.Is(err, deploy.ErrNotFound):
		writeError(w, http.StatusNotFound, "no app with that name is running a successful build")
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusAccepted, build)
	}
}

func rollback(w http.ResponseWriter, r *http.Request, b Builds) {
	build, err := b.Rollback(r.PathValue("name"))
	switch {
	case errors.Is(err, deploy.ErrNotFound):
		writeError(w, http.StatusNotFound, "no app with that name has been deployed")
	case errors.Is(err, deploy.ErrNoRollback):
		writeError(w, http.StatusConflict, "nothing to roll back to: the app has no earlier successful build")
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusAccepted, build)
	}
}

func writeBuildError(w http.ResponseWriter, err error) {
	if errors.Is(err, deploy.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such build")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

// streamBuildLog sends the log so far, then each new chunk, as WebSocket
// text messages; it closes normally when the build ends.
func streamBuildLog(w http.ResponseWriter, r *http.Request, b Builds) {
	past, next, stop, err := b.Follow(r.PathValue("id"))
	if err != nil {
		writeBuildError(w, err)
		return
	}
	defer stop()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := conn.CloseRead(r.Context())

	send := func(p []byte) error {
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return conn.Write(wctx, websocket.MessageText, p)
	}
	// Large histories go out in pieces; a message is not a line.
	for len(past) > 0 {
		n := min(len(past), 32<<10)
		if err := send(past[:n]); err != nil {
			return
		}
		past = past[n:]
	}
	for {
		select {
		case chunk, ok := <-next:
			if !ok {
				_ = conn.Close(websocket.StatusNormalClosure, "build finished")
				return
			}
			if err := send(chunk); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

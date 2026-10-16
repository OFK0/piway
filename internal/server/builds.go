package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
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
	Restart(app string) (deploy.Build, error)
	DeployNow(ctx context.Context, r deploy.DeployRequest, pusher string) (deploy.Build, error)
	Targets() []deploy.Target
	Logs(ctx context.Context, app string, lines int) (string, error)
	Env(ctx context.Context, app string, values bool) (deploy.AppEnv, error)
	SetSecret(app, name, value string) error
	RemoveSecret(app, name string) error
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
	mux.HandleFunc("POST /api/v1/apps/{name}/restart", h(restart))
	mux.HandleFunc("POST /api/v1/apps/{name}/deploy", h(deployApp))
	mux.HandleFunc("GET /api/v1/apps/{name}/logs", h(appLogs))
	mux.HandleFunc("POST /api/v1/deploys", h(deployTarget))
	mux.HandleFunc("GET /api/v1/targets", h(listTargets))
}

func restart(w http.ResponseWriter, r *http.Request, b Builds) {
	build, err := b.Restart(r.PathValue("name"))
	switch {
	case errors.Is(err, deploy.ErrNotFound):
		writeError(w, http.StatusNotFound, "no app with that name is running a successful build")
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusAccepted, build)
	}
}

// deployApp deploys an app's branch: its newest commit, or {"commit"}.
func deployApp(w http.ResponseWriter, r *http.Request, b Builds) {
	var req deploy.DeployRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, `send {"commit": "<full SHA>"} or nothing`)
			return
		}
	}
	deployNow(w, r, b, deploy.DeployRequest{App: r.PathValue("name"), Commit: req.Commit})
}

// deployTarget deploys a repository branch from apps.yaml, built or not.
func deployTarget(w http.ResponseWriter, r *http.Request, b Builds) {
	var req deploy.DeployRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil || req.Repo == "" || req.Branch == "" {
		writeError(w, http.StatusBadRequest, `send {"repo": "owner/name", "branch": "main", "commit": optional full SHA}`)
		return
	}
	deployNow(w, r, b, deploy.DeployRequest{Repo: req.Repo, Branch: req.Branch, Commit: req.Commit})
}

func deployNow(w http.ResponseWriter, r *http.Request, b Builds, req deploy.DeployRequest) {
	build, err := b.DeployNow(r.Context(), req, "api")
	switch {
	case errors.Is(err, deploy.ErrUnknownTarget):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, deploy.ErrBadCommit):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, deploy.ErrNoHead):
		writeError(w, http.StatusBadGateway, err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusAccepted, build)
	}
}

func listTargets(w http.ResponseWriter, _ *http.Request, b Builds) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"targets": b.Targets()})
}

// appLogs returns the last ?lines= (default 100, at most 5000) lines of
// the app's running container.
func appLogs(w http.ResponseWriter, r *http.Request, b Builds) {
	lines := 100
	if v := r.URL.Query().Get("lines"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 5000 {
			writeError(w, http.StatusBadRequest, "lines must be between 1 and 5000")
			return
		}
		lines = n
	}
	out, err := b.Logs(r.Context(), r.PathValue("name"), lines)
	switch {
	case errors.Is(err, deploy.ErrNotFound):
		writeError(w, http.StatusNotFound, "no app with that name has been deployed")
	case errors.Is(err, deploy.ErrNotRunning):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte(out))
	}
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

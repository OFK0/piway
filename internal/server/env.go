package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ofk0/piway/internal/deploy"
	"github.com/ofk0/piway/internal/mcp"
	"github.com/ofk0/piway/internal/secrets"
)

// envRoutes serves an app's environment and lets a signed-in browser set
// its secrets. Secret values go only to a browser session: API tokens and
// the MCP server see names and whether each is set.
func envRoutes(mux *http.ServeMux, b Builds, session func(*http.Request) bool) {
	values := func(r *http.Request) bool {
		return session(r) && r.Header.Get("User-Agent") != mcp.UserAgent
	}
	h := func(f func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if b == nil {
				writeError(w, http.StatusServiceUnavailable, "deploys are disabled (no Docker daemon, see docs/DOCKER.md)")
				return
			}
			f(w, r)
		}
	}
	answer := func(w http.ResponseWriter, r *http.Request) {
		env, err := b.Env(r.Context(), r.PathValue("name"), values(r))
		switch {
		case errors.Is(err, deploy.ErrNotFound):
			writeError(w, http.StatusNotFound, "no app with that name is running a successful build")
		case err != nil:
			writeError(w, http.StatusInternalServerError, err.Error())
		default:
			writeJSON(w, http.StatusOK, env)
		}
	}
	change := func(w http.ResponseWriter, r *http.Request, err error) {
		switch {
		case errors.Is(err, deploy.ErrNotFound):
			writeError(w, http.StatusNotFound, "no app with that name is running a successful build")
		case errors.Is(err, secrets.ErrInvalid):
			writeError(w, http.StatusBadRequest, err.Error())
		case err != nil:
			writeError(w, http.StatusInternalServerError, err.Error())
		default:
			answer(w, r)
		}
	}
	sessionOnly := func(w http.ResponseWriter, r *http.Request) bool {
		if !values(r) {
			writeError(w, http.StatusForbidden, "secrets are set from a signed-in browser, not with an API token")
			return false
		}
		return true
	}

	mux.HandleFunc("GET /api/v1/apps/{name}/env", h(answer))
	mux.HandleFunc("PUT /api/v1/apps/{name}/secrets/{secret}", h(func(w http.ResponseWriter, r *http.Request) {
		if !sessionOnly(w, r) {
			return
		}
		var req struct {
			Value *string `json:"value"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || req.Value == nil {
			writeError(w, http.StatusBadRequest, `send {"value": "…"}`)
			return
		}
		change(w, r, b.SetSecret(r.PathValue("name"), r.PathValue("secret"), *req.Value))
	}))
	mux.HandleFunc("DELETE /api/v1/apps/{name}/secrets/{secret}", h(func(w http.ResponseWriter, r *http.Request) {
		if !sessionOnly(w, r) {
			return
		}
		change(w, r, b.RemoveSecret(r.PathValue("name"), r.PathValue("secret")))
	}))
}

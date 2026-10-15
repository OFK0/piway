package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/ofk0/piway/internal/webhook"
)

// Repos is the list of repositories that deploy (apps.yaml).
// *webhook.Store implements it.
type Repos interface {
	Targets() []webhook.Target
	Add(webhook.Target) error
	Remove(repo, branch string) ([]webhook.Target, error)
}

// repoEntry is a listed repository branch, with the app it became.
type repoEntry struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	App    string `json:"app,omitempty"`
}

const webhookPath = "/api/v1/webhooks/github"

func repoRoutes(mux *http.ServeMux, repos Repos, b Builds, webhookSecretSet bool) {
	if repos == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/repos", func(w http.ResponseWriter, _ *http.Request) {
		apps := map[string]string{}
		if b != nil {
			for _, t := range b.Targets() {
				apps[strings.ToLower(t.Repo)+"@"+t.Branch] = t.App
			}
		}
		out := []repoEntry{}
		for _, t := range repos.Targets() {
			out = append(out, repoEntry{Repo: t.Repo, Branch: t.Branch, App: apps[strings.ToLower(t.Repo)+"@"+t.Branch]})
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{
			"repos": out,
			"webhook": map[string]any{
				"path":       webhookPath,
				"configured": webhookSecretSet,
			},
		})
	})
	mux.HandleFunc("POST /api/v1/repos", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Repo   string `json:"repo"`
			Branch string `json:"branch"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, `send {"repo": "owner/name", "branch": "main"}`)
			return
		}
		if req.Branch == "" {
			req.Branch = "main"
		}
		switch err := repos.Add(webhook.Target{Repo: strings.TrimSpace(req.Repo), Branch: strings.TrimSpace(req.Branch)}); {
		case errors.Is(err, webhook.ErrExists):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, webhook.ErrInvalid):
			writeError(w, http.StatusBadRequest, err.Error())
		case err != nil:
			writeError(w, http.StatusInternalServerError, err.Error())
		default:
			writeJSON(w, http.StatusCreated, repoEntry{Repo: req.Repo, Branch: req.Branch})
		}
	})
	mux.HandleFunc("DELETE /api/v1/repos", func(w http.ResponseWriter, r *http.Request) {
		repo := r.URL.Query().Get("repo")
		if repo == "" {
			writeError(w, http.StatusBadRequest, "repo=owner/name is required; add branch= to remove just one")
			return
		}
		removed, err := repos.Remove(repo, r.URL.Query().Get("branch"))
		switch {
		case errors.Is(err, webhook.ErrNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case err != nil:
			writeError(w, http.StatusInternalServerError, err.Error())
		default:
			writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
		}
	})
}

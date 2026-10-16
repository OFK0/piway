package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/ofk0/piway/internal/gitauth"
	"github.com/ofk0/piway/internal/webhook"
)

// Repos is the list of repositories that deploy (apps.yaml).
// *webhook.Store implements it.
type Repos interface {
	Targets() []webhook.Target
	Add(webhook.Target) error
	Remove(repo, branch string, path ...string) ([]webhook.Target, error)
}

// RepoAccess keeps the deploy keys and the token of private
// repositories. *gitauth.Access implements it. The private half of a
// key never leaves it.
type RepoAccess interface {
	GenerateKey(repo string) (string, error)
	PublicKey(repo string) (string, error)
	RemoveKey(repo string) error
	HasToken() bool
	SetToken(token string) error
	RemoveToken() error
	Test(ctx context.Context, src gitauth.Source) error
}

// repoEntry is a listed repository branch, with the app it became.
type repoEntry struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Path   string `json:"path,omitempty"` // the app's directory; absent for the root
	App    string `json:"app,omitempty"`
	Auth   string `json:"auth,omitempty"` // deploy-key or token; absent when public
	// Provider is "gitlab" for a GitLab project (on Host, if not gitlab.com);
	// absent for GitHub.
	Provider string `json:"provider,omitempty"`
	Host     string `json:"host,omitempty"`
	// For a deploy key: what to paste into GitHub or GitLab, and where.
	PublicKey    string `json:"public_key,omitempty"`
	DeployKeyURL string `json:"deploy_key_url,omitempty"`
}

// accessResult is the answer to "Test access".
type accessResult struct {
	OK      bool         `json:"ok"`
	Kind    gitauth.Kind `json:"kind,omitempty"`
	Message string       `json:"message,omitempty"`
	Detail  string       `json:"detail,omitempty"` // git's own words
}

const (
	webhookPath       = "/api/v1/webhooks/github"
	gitlabWebhookPath = "/api/v1/webhooks/gitlab"
)

func repoRoutes(mux *http.ServeMux, repos Repos, access RepoAccess, b Builds, webhookSecretSet bool, publicURL func(ctx context.Context, provider string) (string, string)) {
	if repos == nil {
		return
	}
	entry := func(t webhook.Target, app string) repoEntry {
		e := repoEntry{Repo: t.Repo, Branch: t.Branch, Path: t.Path, App: app, Auth: t.Auth, Provider: t.Provider, Host: t.Host}
		if t.Auth == gitauth.DeployKey && access != nil {
			e.PublicKey, _ = access.PublicKey(t.Repo)
			e.DeployKeyURL = gitauth.DeployKeyURLFor(t.Provider, t.Host, t.Repo)
		}
		return e
	}

	mux.HandleFunc("GET /api/v1/repos", func(w http.ResponseWriter, r *http.Request) {
		apps := map[string]string{}
		if b != nil {
			for _, t := range b.Targets() {
				apps[targetKey(t.Repo, t.Branch, t.Path)] = t.App
			}
		}
		out := []repoEntry{}
		for _, t := range repos.Targets() {
			out = append(out, entry(t, apps[targetKey(t.Repo, t.Branch, t.Path)]))
		}
		// The address GitHub reaches the endpoint at: not the one the
		// dashboard was opened at, which is often tailnet-only (#140).
		var url, source, glURL, glSource string
		if publicURL != nil {
			url, source = publicURL(r.Context(), gitauth.GitHub)
			glURL, glSource = publicURL(r.Context(), gitauth.GitLab)
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{
			"repos": out,
			"webhook": map[string]any{
				"path":       webhookPath,
				"configured": webhookSecretSet,
				"url":        url,    // "" when piway knows no public address
				"url_source": source, // "config" or "tailscale funnel"
			},
			// The same for GitLab: its own endpoint, which takes the
			// secret as a token instead of a signature.
			"gitlab": map[string]any{
				"path":       gitlabWebhookPath,
				"configured": webhookSecretSet,
				"url":        glURL,
				"url_source": glSource,
			},
			"private": map[string]any{
				"available": access != nil,
				"token_set": access != nil && access.HasToken(),
			},
		})
	})

	mux.HandleFunc("POST /api/v1/repos", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Repo   string `json:"repo"`
			Branch string `json:"branch"`
			Path   string `json:"path"`
			Auth   string `json:"auth"`
			// "gitlab" for a GitLab project (Repo is its path, Host the
			// server unless it is gitlab.com).
			Provider string `json:"provider"`
			Host     string `json:"host"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, `send {"repo": "owner/name", "branch": "main", "auth": "" | "deploy-key" | "token", "provider": "" | "gitlab", "host": optional}`)
			return
		}
		t := webhook.Target{Repo: strings.TrimSpace(req.Repo), Branch: strings.TrimSpace(req.Branch),
			Path: strings.Trim(strings.TrimSpace(req.Path), "/"), Auth: strings.TrimSpace(req.Auth),
			Provider: strings.ToLower(strings.TrimSpace(req.Provider)), Host: strings.ToLower(strings.TrimSpace(req.Host))}
		if t.Provider == "github" {
			t.Provider = ""
		}
		if t.Host == gitauth.DefaultGitLabHost {
			t.Host = ""
		}
		if t.Branch == "" {
			t.Branch = "main"
		}
		if t.Auth != gitauth.Public && access == nil {
			writeError(w, http.StatusBadRequest, "private repositories are not available on this server")
			return
		}
		switch err := repos.Add(t); {
		case errors.Is(err, webhook.ErrExists):
			writeError(w, http.StatusConflict, err.Error())
			return
		case errors.Is(err, webhook.ErrInvalid):
			writeError(w, http.StatusBadRequest, err.Error())
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if t.Auth == gitauth.DeployKey {
			if _, err := access.GenerateKey(t.Repo); err != nil {
				writeError(w, http.StatusInternalServerError, "listed, but the deploy key could not be made: "+err.Error())
				return
			}
		}
		writeJSON(w, http.StatusCreated, entry(t, ""))
	})

	mux.HandleFunc("DELETE /api/v1/repos", func(w http.ResponseWriter, r *http.Request) {
		repo := r.URL.Query().Get("repo")
		if repo == "" {
			writeError(w, http.StatusBadRequest, "repo=owner/name is required; add branch= to remove just one")
			return
		}
		var removed []webhook.Target
		var err error
		if q := r.URL.Query(); q.Has("path") {
			removed, err = repos.Remove(repo, q.Get("branch"), q.Get("path"))
		} else {
			removed, err = repos.Remove(repo, q.Get("branch"))
		}
		switch {
		case errors.Is(err, webhook.ErrNotFound):
			writeError(w, http.StatusNotFound, err.Error())
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// The key goes with the repository's last branch.
		keyRemoved := false
		if access != nil && removed[0].Auth == gitauth.DeployKey && !webhook.Listed(repos.Targets(), repo) {
			if err := access.RemoveKey(removed[0].Repo); err != nil {
				writeError(w, http.StatusInternalServerError, "unlisted, but its deploy key could not be deleted: "+err.Error())
				return
			}
			keyRemoved = true
		}
		writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "key_removed": keyRemoved})
	})

	mux.HandleFunc("POST /api/v1/repos/test", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Repo string `json:"repo"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil || req.Repo == "" {
			writeError(w, http.StatusBadRequest, `send {"repo": "owner/name"}`)
			return
		}
		targets := repos.Targets()
		if !webhook.Listed(targets, req.Repo) {
			writeError(w, http.StatusNotFound, req.Repo+" is not listed")
			return
		}
		tg, _ := webhook.TargetOf(targets, req.Repo)
		src := tg.Source("")
		src.Repo = req.Repo
		var err error
		if access == nil {
			err = (*gitauth.Access)(nil).Test(r.Context(), src)
		} else {
			err = access.Test(r.Context(), src)
		}
		writeJSON(w, http.StatusOK, testResult(err))
	})

	mux.HandleFunc("PUT /api/v1/repos/token", func(w http.ResponseWriter, r *http.Request) {
		if access == nil {
			writeError(w, http.StatusBadRequest, "private repositories are not available on this server")
			return
		}
		var req struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, `send {"token": "github_pat_…"}`)
			return
		}
		if err := access.SetToken(req.Token); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /api/v1/repos/token", func(w http.ResponseWriter, _ *http.Request) {
		if access == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := access.RemoveToken(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func testResult(err error) accessResult {
	if err == nil {
		return accessResult{OK: true}
	}
	var e *gitauth.Error
	if errors.As(err, &e) {
		return accessResult{Kind: e.Kind, Message: e.Message(), Detail: e.Detail}
	}
	return accessResult{Kind: gitauth.Other, Message: err.Error()}
}

// targetKey identifies a repository branch and app directory.
func targetKey(repo, branch, path string) string {
	return strings.ToLower(repo) + "@" + branch + ":" + path
}

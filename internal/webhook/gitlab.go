package webhook

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ofk0/piway/internal/gitauth"
)

// GitLab returns the handler for POST /api/v1/webhooks/gitlab.
//
// GitLab does not sign a delivery. It sends the secret token as it was
// configured on the webhook, in X-Gitlab-Token, so the token is compared
// (in constant time) with webhook_secret, and nothing in the body is acted
// on before it matches. Replays are handled as for GitHub, by the event's
// UUID header.
func (h *Handler) GitLab() http.Handler { return http.HandlerFunc(h.serveGitLab) }

// zeros is the commit GitLab sends for a branch that was deleted.
const zeros = "0000000000000000000000000000000000000000"

type gitlabPush struct {
	ObjectKind string `json:"object_kind"`
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Checkout   string `json:"checkout_sha"`
	User       string `json:"user_username"`
	UserName   string `json:"user_name"`
	Project    struct {
		Path    string `json:"path_with_namespace"`
		HTTPURL string `json:"git_http_url"`
	} `json:"project"`
}

func (h *Handler) serveGitLab(w http.ResponseWriter, r *http.Request) {
	if len(h.secret) == 0 {
		reply(w, http.StatusServiceUnavailable, "disabled", "webhook_secret is not set")
		return
	}
	if !h.validToken(r.Header.Get("X-Gitlab-Token")) {
		h.log.Warn("GitLab webhook token rejected", "remote", r.RemoteAddr, "event", r.Header.Get("X-Gitlab-Event"))
		reply(w, http.StatusUnauthorized, "rejected", "invalid token")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			reply(w, http.StatusRequestEntityTooLarge, "rejected", "body too large")
			return
		}
		reply(w, http.StatusBadRequest, "rejected", "cannot read body")
		return
	}
	if id := r.Header.Get("X-Gitlab-Event-UUID"); id != "" && !h.seen.add("gitlab:"+id) {
		reply(w, http.StatusOK, "duplicate", "delivery already handled")
		return
	}
	switch event := r.Header.Get("X-Gitlab-Event"); event {
	case "Push Hook":
		h.gitlabPush(w, r, body)
	default:
		// Tag pushes, merge requests, pipelines, and the rest do not deploy.
		reply(w, http.StatusOK, "ignored", "event "+event+" does not deploy")
	}
}

// validToken compares the token with the secret without leaking, through
// timing, how much of it is right: both are hashed to the same length first.
func (h *Handler) validToken(token string) bool {
	if token == "" {
		return false
	}
	a, b := sha256.Sum256([]byte(token)), sha256.Sum256(h.secret)
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func (h *Handler) gitlabPush(w http.ResponseWriter, r *http.Request, body []byte) {
	var ev gitlabPush
	if err := json.Unmarshal(body, &ev); err != nil || ev.Project.Path == "" {
		reply(w, http.StatusBadRequest, "rejected", "invalid push payload")
		return
	}
	commit := ev.After
	if ev.Checkout != "" {
		commit = ev.Checkout
	}
	branch, ok := strings.CutPrefix(ev.Ref, "refs/heads/")
	switch {
	case !ok:
		reply(w, http.StatusOK, "ignored", "not a branch push: "+ev.Ref)
		return
	case commit == "" || commit == zeros:
		reply(w, http.StatusOK, "ignored", "branch deleted")
		return
	}
	host := ""
	if u, err := url.Parse(ev.Project.HTTPURL); err == nil {
		host = u.Hostname()
	}
	targets := h.wanted(ev.Project.Path, branch, true, host)
	if len(targets) == 0 {
		reply(w, http.StatusOK, "ignored", host+"/"+ev.Project.Path+"@"+branch+" is not configured to deploy")
		return
	}
	pusher := ev.User
	if pusher == "" {
		pusher = ev.UserName
	}
	id := r.Header.Get("X-Gitlab-Event-UUID")
	for _, t := range targets {
		req := Request{
			Repo: t.Repo, Branch: branch, Commit: commit, CloneURL: ev.Project.HTTPURL, Pusher: pusher,
			Delivery: id, Path: t.Path, Provider: gitauth.GitLab, Host: t.Host,
		}
		if err := h.deployer.Deploy(req); err != nil {
			h.seen.remove("gitlab:" + id)
			h.log.Error("queue deploy", "repo", req.Repo, "path", req.Path, "commit", req.Commit, "err", err)
			reply(w, http.StatusServiceUnavailable, "error", err.Error())
			return
		}
		h.log.Info("deploy queued", "provider", "gitlab", "repo", req.Repo, "branch", branch, "path", req.Path, "commit", req.Commit, "pusher", pusher)
	}
	reply(w, http.StatusAccepted, "queued", "")
}

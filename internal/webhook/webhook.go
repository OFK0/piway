// Package webhook receives GitHub push events and turns the ones for
// configured repositories and branches into deploy requests.
//
// # Trust
//
// GitHub signs each delivery with HMAC-SHA256 over the raw body, using the
// secret set on both sides, in the X-Hub-Signature-256 header. Nothing in a
// request is acted on before that signature is verified, and it is compared
// with hmac.Equal: a byte-by-byte compare that stops at the first mismatch
// would leak, through response timing, how much of a forged signature is
// right.
//
// # Replays
//
// A valid signature proves who sent a body, not when. GitHub does not sign
// a timestamp, but every delivery has a unique X-GitHub-Delivery id, and a
// redelivery from GitHub's UI reuses it. The handler remembers recent ids
// and acknowledges a repeated one without deploying again. Ids are recorded
// only after the signature checks out, so forged requests cannot fill the
// cache. Replaying an old push can at worst redeploy a commit that was
// already deployed from that branch.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// MinSecretLen is the shortest secret accepted; GitHub suggests a long
// random string (openssl rand -hex 32).
const MinSecretLen = 16

// maxBody bounds a delivery. Push payloads list commits and can be large,
// but not this large.
const maxBody = 5 << 20

// Target is a repository and branch whose pushes deploy.
type Target struct {
	Repo   string `yaml:"repo"`   // owner/name, matched case-insensitively
	Branch string `yaml:"branch"` // e.g. main
}

// Request asks for a commit to be deployed.
type Request struct {
	Repo     string // owner/name as GitHub spells it
	Branch   string
	Commit   string // full SHA
	CloneURL string
	Pusher   string
	Delivery string // X-GitHub-Delivery
}

// Deployer accepts deploy requests. Deploy must not block for long: the
// webhook answers GitHub once the request is queued, and GitHub gives up
// after 10 s.
type Deployer interface {
	Deploy(Request) error
}

// Handler serves POST /api/v1/webhooks/github.
type Handler struct {
	secret   []byte
	targets  TargetSource
	deployer Deployer
	log      *slog.Logger
	seen     *recent
}

// New returns a handler. An empty secret disables webhooks: every delivery
// is answered 503, so a misconfigured piway never deploys unsigned pushes.
func New(secret string, targets TargetSource, d Deployer, log *slog.Logger) *Handler {
	return &Handler{secret: []byte(secret), targets: targets, deployer: d, log: log, seen: newRecent(1000)}
}

type response struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

func reply(w http.ResponseWriter, code int, status, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(response{Status: status, Reason: reason})
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if len(h.secret) == 0 {
		reply(w, http.StatusServiceUnavailable, "disabled", "webhook_secret is not set")
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
	if !h.validSignature(r.Header.Get("X-Hub-Signature-256"), body) {
		h.log.Warn("webhook signature rejected", "remote", r.RemoteAddr, "event", r.Header.Get("X-GitHub-Event"))
		reply(w, http.StatusUnauthorized, "rejected", "invalid signature")
		return
	}

	// Authentic from here on. GitHub's default content type wraps the JSON
	// in a form field; the signature above covered the raw form body.
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		form, err := url.ParseQuery(string(body))
		if err != nil || form.Get("payload") == "" {
			reply(w, http.StatusBadRequest, "rejected", "form body without a payload field")
			return
		}
		body = []byte(form.Get("payload"))
	}
	if id := r.Header.Get("X-GitHub-Delivery"); id != "" && !h.seen.add(id) {
		reply(w, http.StatusOK, "duplicate", "delivery already handled")
		return
	}
	switch event := r.Header.Get("X-GitHub-Event"); event {
	case "ping":
		reply(w, http.StatusOK, "pong", "")
	case "push":
		h.push(w, r, body)
	default:
		reply(w, http.StatusOK, "ignored", "event "+event+" does not deploy")
	}
}

// validSignature checks "sha256=<hex>" against the HMAC of body.
func (h *Handler) validSignature(header string, body []byte) bool {
	hexSig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(hexSig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, h.secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

type pushEvent struct {
	Ref     string `json:"ref"`
	After   string `json:"after"`
	Deleted bool   `json:"deleted"`
	Repo    struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
	} `json:"repository"`
	Pusher struct {
		Name string `json:"name"`
	} `json:"pusher"`
}

func (h *Handler) push(w http.ResponseWriter, r *http.Request, body []byte) {
	var ev pushEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		reply(w, http.StatusBadRequest, "rejected", "invalid push payload")
		return
	}
	branch, ok := strings.CutPrefix(ev.Ref, "refs/heads/")
	switch {
	case !ok:
		reply(w, http.StatusOK, "ignored", "not a branch push: "+ev.Ref)
		return
	case ev.Deleted:
		reply(w, http.StatusOK, "ignored", "branch deleted")
		return
	case !h.wanted(ev.Repo.FullName, branch):
		reply(w, http.StatusOK, "ignored", ev.Repo.FullName+"@"+branch+" is not configured to deploy")
		return
	}

	req := Request{
		Repo:     ev.Repo.FullName,
		Branch:   branch,
		Commit:   ev.After,
		CloneURL: ev.Repo.CloneURL,
		Pusher:   ev.Pusher.Name,
		Delivery: r.Header.Get("X-GitHub-Delivery"),
	}
	if err := h.deployer.Deploy(req); err != nil {
		h.seen.remove(req.Delivery)
		h.log.Error("queue deploy", "repo", req.Repo, "commit", req.Commit, "err", err)
		reply(w, http.StatusServiceUnavailable, "error", err.Error())
		return
	}
	h.log.Info("deploy queued", "repo", req.Repo, "branch", branch, "commit", req.Commit, "pusher", req.Pusher)
	reply(w, http.StatusAccepted, "queued", "")
}

func (h *Handler) wanted(repo, branch string) bool {
	for _, t := range h.targets.Targets() {
		if strings.EqualFold(t.Repo, repo) && t.Branch == branch {
			return true
		}
	}
	return false
}

// recent remembers the last n delivery ids.
type recent struct {
	mu   sync.Mutex
	ids  map[string]struct{}
	ring []string
	next int
}

func newRecent(n int) *recent {
	return &recent{ids: make(map[string]struct{}, n), ring: make([]string, n)}
}

// add records id and reports whether it was new.
func (c *recent) add(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.ids[id]; ok {
		return false
	}
	if old := c.ring[c.next]; old != "" {
		delete(c.ids, old)
	}
	c.ring[c.next] = id
	c.ids[id] = struct{}{}
	c.next = (c.next + 1) % len(c.ring)
	return true
}

// remove releases a failed delivery so GitHub can redeliver it. Clear its
// ring slot too, so that slot cannot later evict a successful retry.
func (c *recent) remove(id string) {
	if id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.ids, id)
	for i, old := range c.ring {
		if old == id {
			c.ring[i] = ""
			break
		}
	}
}

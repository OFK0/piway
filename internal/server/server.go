// Package server wires up piway's HTTP API.
package server

import (
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/ofk0/piway/internal/agent"
	"github.com/ofk0/piway/internal/auth"
	"github.com/ofk0/piway/internal/mcp"
	"github.com/ofk0/piway/internal/prom"
	"github.com/ofk0/piway/internal/version"
)

// SnapshotSource provides the most recent metrics snapshot. *agent.Agent
// implements it; tests use a stub.
type SnapshotSource interface {
	// Latest returns the newest Snapshot, or nil before the first round.
	Latest() *agent.Snapshot
}

// Deps are the parts of piway the HTTP API serves. Nil History, Webhooks
// handler and Builds mean the feature is off; UI nil means no dashboard.
type Deps struct {
	Snapshots SnapshotSource
	Live      http.Handler // WebSocket snapshot stream
	History   SeriesQuerier
	Webhooks  http.Handler // GitHub webhooks (internal/webhook)
	Builds    Builds
	Repos     Repos // nil: the repository list is not editable
	// RepoAccess holds the credentials of private repositories; nil
	// allows public repositories only.
	RepoAccess RepoAccess
	// WebhookSecretSet tells the dashboard whether GitHub deliveries can
	// be verified at all.
	WebhookSecretSet bool
	UI               fs.FS
	// Auth guards the API; nil leaves it open (tests).
	Auth   *auth.Auth
	Alerts Alerts
	Push   Push
}

// Push registers the Web Push API (internal/push).
type Push interface {
	Routes(mux *http.ServeMux, protect func(http.Handler) http.Handler)
}

// New returns the root HTTP handler.
func New(d Deps) (http.Handler, error) {
	snapshots, live, hist, hooks, ui := d.Snapshots, d.Live, d.History, d.Webhooks, d.UI
	if live == nil {
		live = http.NotFoundHandler()
	}
	if hooks == nil {
		hooks = http.NotFoundHandler()
	}
	dashboard, err := uiHandler(ui)
	if err != nil {
		return nil, err
	}

	protect := func(h http.Handler) http.Handler { return h }
	if d.Auth != nil {
		protect = d.Auth.Require
	}

	// Everything under /api/ needs a session, except the routes on mux.
	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/snapshot", snapshot(snapshots))
	api.Handle("GET /api/v1/stream", live)
	api.HandleFunc("GET /api/v1/series", series(hist, time.Now))
	api.HandleFunc("GET /api/v1/alerts", alerts(d.Alerts))
	if d.Push != nil {
		d.Push.Routes(api, func(h http.Handler) http.Handler { return h }) // api is protected as a whole
	}
	buildRoutes(api, d.Builds)
	session := func(*http.Request) bool { return true } // no Auth: tests
	if d.Auth != nil {
		session = d.Auth.Session
	}
	envRoutes(api, d.Builds, session)
	repoRoutes(api, d.Repos, d.RepoAccess, d.Builds, d.WebhookSecretSet)
	// Unknown API paths get a JSON 404, never the dashboard's index.html.
	// Method-specific patterns keep the mux's automatic 405 for, say,
	// POST /api/v1/snapshot.
	api.HandleFunc("GET /api/", apiNotFound)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	mux.Handle("POST /api/v1/webhooks/github", hooks) // signed by GitHub instead
	if d.Auth != nil {
		d.Auth.Routes(mux)
	}
	mux.Handle("/api/", protect(api))
	mux.Handle("GET /metrics", protect(prom.Handler(snapshots.Latest)))
	// MCP over streamable HTTP. Its tools call this same handler, in
	// process, with the caller's credentials (internal/mcp).
	var root http.Handler
	mcpServer := &mcp.Server{Name: "piway", Version: version.Version, Tools: mcp.Tools()}
	mux.Handle("/mcp", protect(mcpServer.HTTPHandler(func(r *http.Request) *mcp.Client {
		return &mcp.Client{
			Base:          "http://piway.internal",
			Authorization: r.Header.Get("Authorization"),
			Cookie:        r.Header.Get("Cookie"),
			RemoteAddr:    r.RemoteAddr,
			HTTP:          &http.Client{Transport: mcp.InProcess{Handler: root}, Timeout: 30 * time.Second},
		}
	})))
	// The dashboard's files are public: they hold no data, and its login
	// page is one of them.
	mux.Handle("/", getOnly(dashboard))

	// Reject cross-site form posts and fetches (CSRF): browsers say where
	// a request comes from in Sec-Fetch-Site or Origin. Requests without
	// those headers, such as GitHub's webhooks, are not from a browser.
	root = http.NewCrossOriginProtection().Handler(mux)
	return root, nil
}

// getOnly answers 405 to anything but GET and HEAD. The dashboard cannot
// be registered as "GET /": that would conflict with the "/api/" subtree.
func getOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func apiNotFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "no such endpoint")
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"version": version.Version,
		"commit":  version.Commit,
	})
}

func snapshot(src SnapshotSource) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		s := src.Latest()
		if s == nil {
			// Only possible in the first interval after startup.
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusServiceUnavailable, "no snapshot collected yet")
			return
		}
		// Every request should see fresh data, never a cached round.
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, s)
	}
}

// errorBody is the shape of every API error response.
type errorBody struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

// writeJSON marshals v before touching the response, so an encoding failure
// still yields a clean 500 instead of a 200 with a truncated body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Error("encode response", "err", err)
		status = http.StatusInternalServerError
		b = []byte(`{"error":"internal error"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

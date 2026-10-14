// Package server wires up piway's HTTP API.
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/ofk0/piway/internal/agent"
	"github.com/ofk0/piway/internal/version"
)

// SnapshotSource provides the most recent metrics snapshot. *agent.Agent
// implements it; tests use a stub.
type SnapshotSource interface {
	// Latest returns the newest Snapshot, or nil before the first round.
	Latest() *agent.Snapshot
}

// New returns the root HTTP handler.
func New(snapshots SnapshotSource) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /api/v1/snapshot", snapshot(snapshots))
	return mux
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

// Package server wires up piway's HTTP API.
package server

import (
	"encoding/json"
	"net/http"

	"github.com/ofk0/piway/internal/version"
)

// New returns the root HTTP handler.
func New() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	return mux
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"version": version.Version,
		"commit":  version.Commit,
	})
}

package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/ofk0/piway/internal/databases"
)

// Databases is the shared database engines as the API sees them.
// *databases.Manager implements it.
type Databases interface {
	Status(ctx context.Context) ([]databases.EngineStatus, error)
	BackupAll(ctx context.Context, keep int, log io.Writer) error
}

func databaseRoutes(mux *http.ServeMux, d Databases) {
	h := func(f http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if d == nil {
				writeError(w, http.StatusServiceUnavailable, "databases need Docker (docs/DOCKER.md)")
				return
			}
			f(w, r)
		}
	}
	mux.HandleFunc("GET /api/v1/databases", h(func(w http.ResponseWriter, r *http.Request) {
		engines, err := d.Status(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{"engines": engines})
	}))
	// Backs up every app's database now, as the nightly job does.
	mux.HandleFunc("POST /api/v1/databases/backup", h(func(w http.ResponseWriter, r *http.Request) {
		var log bytes.Buffer
		err := d.BackupAll(r.Context(), databases.Keep, &log)
		lines := strings.Split(strings.TrimSpace(log.String()), "\n")
		if lines[0] == "" {
			lines = []string{}
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "done": lines})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"done": lines})
	}))
}

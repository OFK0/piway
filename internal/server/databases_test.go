package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ofk0/piway/internal/databases"
)

type fakeDatabases struct{ backupErr error }

func (fakeDatabases) Status(context.Context) ([]databases.EngineStatus, error) {
	return []databases.EngineStatus{
		{Name: "postgres", State: "running", Image: "postgres:16-alpine", Pinned: "postgres:17-alpine", UpgradeAvailable: true,
			Size: 1 << 20, Memory: 40 << 20, Apps: []databases.App{{Name: "blog", Backups: 3}}},
		{Name: "redis", State: "not_started", Pinned: "redis:7.4-alpine", Size: -1, Memory: -1, Apps: []databases.App{}},
	}, nil
}

func (f fakeDatabases) BackupAll(_ context.Context, keep int, log io.Writer) error {
	fmt.Fprintf(log, "postgres/blog: /x.sql.gz (10 bytes)\n")
	return f.backupErr
}

func TestDatabasesAPI(t *testing.T) {
	h, err := New(Deps{Snapshots: stubSource{}, Databases: fakeDatabases{}})
	if err != nil {
		t.Fatal(err)
	}
	w := call(h, "GET", "/api/v1/databases", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"upgrade_available":true`) || !strings.Contains(w.Body.String(), `"state":"not_started"`) {
		t.Errorf("status: %d %s", w.Code, w.Body)
	}
	if w := call(h, "POST", "/api/v1/databases/backup", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "postgres/blog") {
		t.Errorf("backup: %d %s", w.Code, w.Body)
	}

	h, _ = New(Deps{Snapshots: stubSource{}, Databases: fakeDatabases{backupErr: errors.New("redis is down")}})
	if w := call(h, "POST", "/api/v1/databases/backup", ""); w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "redis is down") {
		t.Errorf("failed backup: %d %s", w.Code, w.Body)
	}
	h, _ = New(Deps{Snapshots: stubSource{}})
	if w := call(h, "GET", "/api/v1/databases", ""); w.Code != http.StatusServiceUnavailable {
		t.Errorf("without Docker: %d", w.Code)
	}
}

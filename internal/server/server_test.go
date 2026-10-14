package server

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ofk0/piway/internal/agent"
	"github.com/ofk0/piway/internal/collector/cpu"
	"github.com/ofk0/piway/internal/collector/mem"
)

// stubSource returns a fixed snapshot.
type stubSource struct{ s *agent.Snapshot }

func (f stubSource) Latest() *agent.Snapshot { return f.s }

// newServer builds the handler without a dashboard.
func newServer(t *testing.T, src SnapshotSource) http.Handler {
	t.Helper()
	h, err := New(src, http.NotFoundHandler(), nil, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func serve(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestHealthz(t *testing.T) {
	rec := serve(t, newServer(t, stubSource{}), http.MethodGet, "/healthz")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status field = %q, want ok", body["status"])
	}
}

func TestSnapshot(t *testing.T) {
	want := &agent.Snapshot{
		Seq:    42,
		Time:   time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC),
		CPU:    &cpu.Usage{Total: 12.5, PerCore: []float64{10, 15}},
		Memory: &mem.Info{Total: 4 << 30, Used: 1 << 30},
		Errors: map[string]string{"thermal": "vcgencmd: not found"},
	}
	rec := serve(t, newServer(t, stubSource{want}), http.MethodGet, "/api/v1/snapshot")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}

	// Decode into a generic map to pin the wire format documented in
	// docs/API.md, not just round-trip through the same Go types.
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"seq", "time", "cpu", "memory", "thermal", "disk", "network", "errors"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing key %q in %s", key, rec.Body)
		}
	}
	if got["seq"] != float64(42) {
		t.Errorf("seq = %v, want 42", got["seq"])
	}
	if got["time"] != "2026-10-14T12:00:00Z" {
		t.Errorf("time = %v, want RFC 3339", got["time"])
	}
	if got["thermal"] != nil {
		t.Errorf("thermal = %v, want null for a missing collector", got["thermal"])
	}
	if c := got["cpu"].(map[string]any); c["total"] != 12.5 {
		t.Errorf("cpu.total = %v, want 12.5", c["total"])
	}
	if m := got["memory"].(map[string]any); m["total_bytes"] != float64(4<<30) {
		t.Errorf("memory.total_bytes = %v, want %d", m["total_bytes"], 4<<30)
	}
	if e := got["errors"].(map[string]any); e["thermal"] != "vcgencmd: not found" {
		t.Errorf("errors.thermal = %v", e["thermal"])
	}
}

func TestSnapshotOmitsEmptyErrors(t *testing.T) {
	rec := serve(t, newServer(t, stubSource{&agent.Snapshot{Seq: 1}}), http.MethodGet, "/api/v1/snapshot")

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := got["errors"]; ok {
		t.Errorf("errors present in a clean round: %s", rec.Body)
	}
}

func TestSnapshotBeforeFirstRound(t *testing.T) {
	rec := serve(t, newServer(t, stubSource{}), http.MethodGet, "/api/v1/snapshot")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header missing")
	}
	var body errorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error == "" {
		t.Error("error message missing")
	}
}

func TestSnapshotEncodeFailure(t *testing.T) {
	// encoding/json rejects NaN, which a buggy collector could produce.
	bad := &agent.Snapshot{CPU: &cpu.Usage{Total: math.NaN()}}
	rec := serve(t, newServer(t, stubSource{bad}), http.MethodGet, "/api/v1/snapshot")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Errorf("body is not valid JSON: %q", rec.Body)
	}
}

func TestSnapshotMethodNotAllowed(t *testing.T) {
	rec := serve(t, newServer(t, stubSource{}), http.MethodPost, "/api/v1/snapshot")

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("Allow = %q, want \"GET, HEAD\"", allow)
	}
}

func TestMetrics(t *testing.T) {
	rec := serve(t, newServer(t, stubSource{&agent.Snapshot{Seq: 1}}), http.MethodGet, "/metrics")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("Content-Type = %q, want the Prometheus text format", ct)
	}
	if !strings.Contains(rec.Body.String(), "piway_snapshot_timestamp_seconds ") {
		t.Errorf("body has no snapshot timestamp:\n%s", rec.Body)
	}
}

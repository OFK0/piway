package recorder

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ofk0/piway/internal/agent"
	"github.com/ofk0/piway/internal/collector/cpu"
	"github.com/ofk0/piway/internal/collector/mem"
	"github.com/ofk0/piway/internal/collector/netdev"
	"github.com/ofk0/piway/internal/collector/thermal"
	"github.com/ofk0/piway/internal/tsdb"
)

type fakeStore struct {
	t      int64
	points []tsdb.Point
	err    error
}

func (f *fakeStore) Append(t int64, p []tsdb.Point) error {
	f.t, f.points = t, append([]tsdb.Point(nil), p...)
	return f.err
}

func values(points []tsdb.Point) map[string]float64 {
	m := make(map[string]float64)
	for _, p := range points {
		m[p.Series] = p.Value
	}
	return m
}

func TestPoints(t *testing.T) {
	temp := 48.5
	s := &agent.Snapshot{
		CPU:     &cpu.Usage{Total: 12.5, PerCore: []float64{10, 15}},
		Memory:  &mem.Info{Total: 4 << 30, Used: 1 << 30},
		Thermal: &thermal.Reading{TempCelsius: &temp, Throttle: &thermal.Throttle{Now: thermal.State{UnderVoltage: true}}},
		Network: []netdev.Interface{{Name: "eth0", RxBytesPerSec: 2048, RxErrors: 1, RxDropped: 2}},
	}
	got := values(Points(nil, s))
	want := map[string]float64{
		"cpu_usage_percent":                                  12.5,
		`cpu_core_usage_percent{core="0"}`:                   10,
		`cpu_core_usage_percent{core="1"}`:                   15,
		"memory_total_bytes":                                 4 << 30,
		"memory_used_bytes":                                  1 << 30,
		"temperature_celsius":                                48.5,
		"throttle_under_voltage":                             1,
		"throttle_throttled":                                 0,
		`network_receive_bytes_per_second{interface="eth0"}`: 2048,
		`network_receive_errors_total{interface="eth0"}`:     3,
	}
	for name, v := range want {
		if g, ok := got[name]; !ok || g != v {
			t.Errorf("%s = %v (present %v), want %v", name, g, ok, v)
		}
	}
	for name := range got {
		if len(name) > tsdb.MaxSeriesLen {
			t.Errorf("series name too long for the store: %s", name)
		}
	}
}

func TestMissingCollectorsLeaveGaps(t *testing.T) {
	if p := Points(nil, &agent.Snapshot{}); len(p) != 0 {
		t.Errorf("empty snapshot produced %d points: %v", len(p), p)
	}
}

func TestRecordUsesSnapshotTime(t *testing.T) {
	store := &fakeStore{}
	r := New(store, slog.New(slog.DiscardHandler))
	when := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	r.Record(&agent.Snapshot{Time: when, CPU: &cpu.Usage{Total: 1}})
	if store.t != when.UnixMilli() {
		t.Errorf("t = %d, want %d", store.t, when.UnixMilli())
	}
}

func TestRecordLogsErrorsOnTransitionOnly(t *testing.T) {
	var logs bytes.Buffer
	store := &fakeStore{err: errors.New("disk full")}
	r := New(store, slog.New(slog.NewTextHandler(&logs, nil)))
	s := &agent.Snapshot{CPU: &cpu.Usage{Total: 1}}

	for range 5 {
		r.Record(s)
	}
	store.err = nil
	r.Record(s)
	r.Record(s)

	out := logs.String()
	if n := strings.Count(out, "cannot record metrics"); n != 1 {
		t.Errorf("logged the failure %d times, want once:\n%s", n, out)
	}
	if n := strings.Count(out, "recording metrics again"); n != 1 {
		t.Errorf("logged the recovery %d times, want once:\n%s", n, out)
	}
}

package history

import (
	"math"
	"testing"
	"time"

	"github.com/ofk0/piway/internal/tsdb"
)

// t0 is a round hour, so minute and hour buckets start at known offsets.
var t0 = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func open(t *testing.T, dir string, c *clock, r Retention) *History {
	t.Helper()
	h, err := Open(Options{Dir: dir, Retention: r, now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

func appendAt(t *testing.T, h *History, at time.Time, series string, v float64) {
	t.Helper()
	if err := h.Append(at.UnixMilli(), []tsdb.Point{{Series: series, Value: v}}); err != nil {
		t.Fatal(err)
	}
}

func compact(t *testing.T, h *History) {
	t.Helper()
	if err := h.Compact(); err != nil {
		t.Fatal(err)
	}
}

// rollupAt reads the four components of series in store at bucket start b.
func rollupAt(t *testing.T, s *tsdb.Store, series string, b time.Time) (agg, bool) {
	t.Helper()
	var a agg
	found := false
	for suffix, dst := range map[string]*float64{SuffixMin: &a.min, SuffixMax: &a.max, SuffixSum: &a.sum, SuffixCount: &a.n} {
		got, err := s.Select(series+suffix, b.UnixMilli(), b.UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		if len(got) > 1 {
			t.Fatalf("%s%s has %d rollups at %v, want at most 1", series, suffix, len(got), b)
		}
		if len(got) == 1 {
			*dst = got[0].V
			found = true
		}
	}
	return a, found
}

func TestMinuteRollup(t *testing.T) {
	c := &clock{t: t0}
	h := open(t, t.TempDir(), c, Retention{})
	// Minute 0: values 0..59; minute 1: constant 7.
	for i := range 120 {
		v := float64(i)
		if i >= 60 {
			v = 7
		}
		appendAt(t, h, t0.Add(time.Duration(i)*time.Second), "cpu", v)
	}

	c.t = t0.Add(2*time.Minute + 4*time.Second) // inside the grace period of minute 1
	compact(t, h)
	if _, ok := rollupAt(t, h.Minute(), "cpu", t0.Add(time.Minute)); ok {
		t.Error("minute 1 rolled up before its grace period ended")
	}
	got, ok := rollupAt(t, h.Minute(), "cpu", t0)
	want := agg{min: 0, max: 59, sum: 59 * 60 / 2, n: 60}
	if !ok || got != want {
		t.Errorf("minute 0 = %+v, want %+v", got, want)
	}

	c.t = t0.Add(2*time.Minute + 5*time.Second)
	compact(t, h)
	got, ok = rollupAt(t, h.Minute(), "cpu", t0.Add(time.Minute))
	want = agg{min: 7, max: 7, sum: 7 * 60, n: 60}
	if !ok || got != want {
		t.Errorf("minute 1 = %+v, want %+v", got, want)
	}
}

func TestHourRollupWeightsBySampleCount(t *testing.T) {
	c := &clock{t: t0}
	h := open(t, t.TempDir(), c, Retention{})
	// Minute 0: 60 samples of 1. Minute 1: 10 samples of 100 (piway restarted).
	for i := range 60 {
		appendAt(t, h, t0.Add(time.Duration(i)*time.Second), "temp", 1)
	}
	for i := range 10 {
		appendAt(t, h, t0.Add(time.Minute+time.Duration(i)*time.Second), "temp", 100)
	}
	appendAt(t, h, t0.Add(time.Hour+time.Second), "temp", 50) // so the hour is complete

	c.t = t0.Add(time.Hour + time.Minute + 10*time.Second)
	compact(t, h)

	got, ok := rollupAt(t, h.Hour(), "temp", t0)
	if !ok {
		t.Fatal("no hourly rollup")
	}
	if got.min != 1 || got.max != 100 || got.n != 70 || got.sum != 60+1000 {
		t.Errorf("hour = %+v", got)
	}
	// The correct average is 1060/70 ≈ 15.1; averaging the two minutes'
	// averages would give 50.5.
	if avg := got.sum / got.n; math.Abs(avg-1060.0/70) > 1e-9 {
		t.Errorf("average = %v, want %v", avg, 1060.0/70)
	}
}

func TestRestartResumesWithoutDuplicates(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: t0}
	h, err := Open(Options{Dir: dir, now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 180 {
		appendAt(t, h, t0.Add(time.Duration(i)*time.Second), "cpu", 1)
	}
	c.t = t0.Add(3*time.Minute + 10*time.Second)
	compact(t, h)
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}

	// After a restart, compaction continues after the last stored rollup.
	h = open(t, dir, c, Retention{})
	for i := range 60 {
		appendAt(t, h, t0.Add(3*time.Minute+time.Duration(i)*time.Second), "cpu", 2)
	}
	c.t = t0.Add(4*time.Minute + 10*time.Second)
	compact(t, h)
	compact(t, h) // idempotent

	for m := range 4 {
		got, ok := rollupAt(t, h.Minute(), "cpu", t0.Add(time.Duration(m)*time.Minute))
		if !ok || got.n != 60 {
			t.Errorf("minute %d: %+v (found %v), want 60 samples", m, got, ok)
		}
	}
}

func TestDowntimeLeavesNoRollups(t *testing.T) {
	c := &clock{t: t0}
	h := open(t, t.TempDir(), c, Retention{})
	appendAt(t, h, t0, "cpu", 1)
	appendAt(t, h, t0.Add(10*time.Minute), "cpu", 2) // down for 9 minutes
	c.t = t0.Add(11*time.Minute + 10*time.Second)
	compact(t, h)

	got, err := h.Minute().Select("cpu"+SuffixCount, 0, math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("%d minute rollups, want 2 (one per minute with data)", len(got))
	}
}

func TestRetentionDeletesOldSegments(t *testing.T) {
	c := &clock{t: t0}
	h := open(t, t.TempDir(), c, Retention{Raw: 2 * time.Hour})
	// One sample a minute for 5 hours: raw segments span an hour each.
	for m := range 5 * 60 {
		appendAt(t, h, t0.Add(time.Duration(m)*time.Minute), "cpu", 1)
	}
	c.t = t0.Add(5*time.Hour + time.Minute)
	compact(t, h)

	minT, _, ok := h.Raw().Bounds()
	if !ok {
		t.Fatal("raw tier is empty")
	}
	if age := c.t.Sub(time.UnixMilli(minT)); age > 3*time.Hour {
		t.Errorf("oldest raw sample is %v old, want at most retention + one segment (3h)", age)
	}
	// Rolled-up history is kept although its raw data is gone.
	if _, ok := rollupAt(t, h.Minute(), "cpu", t0); !ok {
		t.Error("minute rollup of expired raw data is missing")
	}
}

func TestRawIsNotExpiredBeforeItIsRolledUp(t *testing.T) {
	c := &clock{t: t0}
	h := open(t, t.TempDir(), c, Retention{Raw: time.Minute})
	for m := range 3 * 60 {
		appendAt(t, h, t0.Add(time.Duration(m)*time.Minute), "cpu", 1)
	}
	// Expire without rolling anything up: nextMinute is still unknown.
	c.t = t0.Add(10 * time.Hour)
	if err := h.expire(c.t); err != nil {
		t.Fatal(err)
	}
	if minT, _, _ := h.Raw().Bounds(); minT != t0.UnixMilli() {
		t.Errorf("raw data expired before it was rolled up (oldest now %v)", time.UnixMilli(minT))
	}
}

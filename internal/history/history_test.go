package history

import (
	"errors"
	"math"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/ofk0/piway/internal/testfs"
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

func query(t *testing.T, h *History, name string, from, to time.Time, step time.Duration) Result {
	t.Helper()
	r, err := h.Query(name, from, to, step)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestQueryShortRangeReadsRaw(t *testing.T) {
	c := &clock{t: t0}
	h := open(t, t.TempDir(), c, Retention{})
	for i := range 60 {
		appendAt(t, h, t0.Add(time.Duration(i)*time.Second), "cpu", float64(i%10))
	}
	r := query(t, h, "cpu", t0, t0.Add(time.Minute), 10*time.Second)
	if r.Resolution != ResolutionRaw || r.Step != 10*time.Second {
		t.Fatalf("resolution %s step %v, want raw 10s", r.Resolution, r.Step)
	}
	if len(r.Series) != 1 || len(r.Series[0].Points) != 6 {
		t.Fatalf("got %+v, want 1 series of 6 points", r.Series)
	}
	p := r.Series[0].Points[0]
	if p.T != t0.UnixMilli() || p.Min != 0 || p.Max != 9 || p.Avg != 4.5 {
		t.Errorf("first bucket = %+v, want t0 min 0 max 9 avg 4.5", p)
	}
}

func TestQueryLongRangeReadsRollups(t *testing.T) {
	c := &clock{t: t0}
	h := open(t, t.TempDir(), c, Retention{})
	// Two hours, one sample every 10 s.
	for i := range 720 {
		appendAt(t, h, t0.Add(time.Duration(i)*10*time.Second), "cpu", 50)
	}
	c.t = t0.Add(2*time.Hour + time.Minute)
	compact(t, h)

	// 2 h at a 1 s step would be 7200 points: the step is raised to fit
	// MaxPoints, and with steps of a minute or more the 1m tier is used.
	r := query(t, h, "cpu", t0, t0.Add(2*time.Hour), time.Second)
	if r.Resolution != ResolutionRaw || r.Step < 7*time.Second {
		t.Errorf("2h at 1s: %s step %v, want raw with step raised to fit %d points", r.Resolution, r.Step, MaxPoints)
	}
	if n := len(r.Series[0].Points); n > MaxPoints {
		t.Errorf("%d points, want at most %d", n, MaxPoints)
	}

	r = query(t, h, "cpu", t0, t0.Add(2*time.Hour), 5*time.Minute)
	if r.Resolution != ResolutionMinute || r.Step != 5*time.Minute {
		t.Fatalf("5m step: %s %v, want 1m tier with 5m step", r.Resolution, r.Step)
	}
	if n := len(r.Series[0].Points); n != 24 {
		t.Errorf("%d points, want 24", n)
	}
	if p := r.Series[0].Points[0]; p.Avg != 50 || p.Min != 50 || p.Max != 50 {
		t.Errorf("bucket = %+v, want 50s", p)
	}
}

func TestQueryFallsBackWhenRawExpired(t *testing.T) {
	c := &clock{t: t0}
	h := open(t, t.TempDir(), c, Retention{Raw: time.Hour})
	for m := range 4 * 60 {
		appendAt(t, h, t0.Add(time.Duration(m)*time.Minute), "cpu", 1)
	}
	c.t = t0.Add(4*time.Hour + time.Minute)
	compact(t, h) // raw before ~2h ago is gone; the 1m tier has it all

	r := query(t, h, "cpu", t0, t0.Add(30*time.Minute), 10*time.Second)
	if r.Resolution != ResolutionMinute {
		t.Errorf("resolution %s, want 1m: raw no longer covers the start", r.Resolution)
	}
	if len(r.Series) != 1 || len(r.Series[0].Points) != 30 {
		t.Errorf("got %d series / points %v", len(r.Series), r.Series)
	}
}

func TestQueryMatchesLabelledSeries(t *testing.T) {
	c := &clock{t: t0}
	h := open(t, t.TempDir(), c, Retention{})
	for _, p := range []tsdb.Point{
		{Series: `net_rx{interface="wlan0"}`, Value: 1},
		{Series: `net_rx{interface="eth0"}`, Value: 2},
		{Series: "net_rx_total", Value: 3},
	} {
		if err := h.Append(t0.UnixMilli(), []tsdb.Point{p}); err != nil {
			t.Fatal(err)
		}
	}
	r := query(t, h, "net_rx", t0, t0.Add(time.Minute), time.Minute)
	var names []string
	for _, s := range r.Series {
		names = append(names, s.Name)
	}
	want := []string{`net_rx{interface="eth0"}`, `net_rx{interface="wlan0"}`}
	if !slices.Equal(names, want) {
		t.Errorf("series %v, want %v (labelled variants, not net_rx_total)", names, want)
	}

	r = query(t, h, `net_rx{interface="eth0"}`, t0, t0.Add(time.Minute), time.Minute)
	if len(r.Series) != 1 || r.Series[0].Points[0].Avg != 2 {
		t.Errorf("exact match: %+v", r.Series)
	}
}

func TestQueryRejectsEmptyRange(t *testing.T) {
	h := open(t, t.TempDir(), &clock{t: t0}, Retention{})
	if _, err := h.Query("cpu", t0, t0, time.Minute); err == nil {
		t.Error("empty range accepted")
	}
}

func TestQueryPointLimit(t *testing.T) {
	tests := []struct {
		name                 string
		resolution           Resolution
		span, interval, step time.Duration
	}{
		{"raw unaligned", ResolutionRaw, 1000 * time.Second, 100 * time.Millisecond, time.Second},
		{"minute rounded down", ResolutionMinute, 7 * 24 * time.Hour, time.Minute, time.Minute},
		{"hour rounded down", ResolutionHour, 90 * 24 * time.Hour, time.Hour, time.Hour},
		{"maximum step", ResolutionHour, 90 * 24 * time.Hour, time.Hour, time.Duration(math.MaxInt64)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := open(t, t.TempDir(), &clock{t: t0}, Retention{})
			store := h.Raw()
			if tt.resolution == ResolutionMinute {
				store = h.Minute()
			}
			if tt.resolution == ResolutionHour {
				store = h.Hour()
			}
			from := t0.Add(123 * time.Millisecond)
			to := from.Add(tt.span)
			for at := t0; at.Before(to); at = at.Add(tt.interval) {
				points := []tsdb.Point{{Series: "cpu", Value: 7}}
				if tt.resolution != ResolutionRaw {
					points = rollupPoints(map[string]*agg{"cpu": {min: 7, max: 7, sum: 7, n: 1}})
				}
				if err := store.Append(at.UnixMilli(), points); err != nil {
					t.Fatal(err)
				}
			}
			r := query(t, h, "cpu", from, to, tt.step)
			if r.Resolution != tt.resolution {
				t.Fatalf("resolution = %s, want %s", r.Resolution, tt.resolution)
			}
			if r.Step < tt.step {
				t.Errorf("step = %v, must not shrink requested %v", r.Step, tt.step)
			}
			if len(r.Series) != 1 {
				t.Fatalf("series = %d, want 1", len(r.Series))
			}
			if n := len(r.Series[0].Points); n > MaxPoints {
				t.Errorf("%d points at %v, want at most %d", n, r.Step, MaxPoints)
			}
			for _, p := range r.Series[0].Points {
				if p.Avg != 7 || p.Min != 7 || p.Max != 7 {
					t.Errorf("changed aggregate: %+v", p)
				}
			}
		})
	}
}

// A disk that fills up while compacting: the rollup fails with the reason,
// no raw data is lost, and once space is back the next Compact rolls up
// the same minutes without duplicates.
func TestFullDisk(t *testing.T) {
	testfs.Run(t, 512<<10, func(t *testing.T, dir string) {
		c := &clock{t: t0}
		h := open(t, filepath.Join(dir, "history"), c, DefaultRetention())
		for i := range 150 {
			for _, series := range []string{"cpu", "mem", "temp"} {
				appendAt(t, h, t0.Add(time.Duration(i)*time.Second), series, float64(i))
			}
		}

		free := testfs.Fill(t, dir)
		c.t = t0.Add(3 * time.Minute)
		if err := h.Compact(); !errors.Is(err, syscall.ENOSPC) {
			t.Fatalf("Compact on a full disk: %v, want ENOSPC", err)
		}
		raw, err := h.Raw().Select("cpu", math.MinInt64, math.MaxInt64)
		if err != nil || len(raw) != 150 {
			t.Fatalf("raw samples after a failed Compact: %d, %v; want 150", len(raw), err)
		}

		free()
		compact(t, h)
		for _, series := range []string{"cpu", "mem", "temp"} {
			got, ok := rollupAt(t, h.Minute(), series, t0.Add(time.Minute))
			want := agg{min: 60, max: 119, sum: (60 + 119) * 60 / 2, n: 60}
			if !ok || got != want {
				t.Errorf("%s, minute 1 = %+v, want %+v", series, got, want)
			}
		}
	})
}

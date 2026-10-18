package history

import (
	"bytes"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ofk0/piway/internal/tsdb"
)

// TestSimulateDays records PIWAY_SIMULATE_DAYS days of a Pi's history, one
// round a second with the fake clock, compacting every 10 s as piway does,
// and reports the disk use, the compaction time and the bytes written. It
// skips unless the variable is set: 30 days is minutes of CPU.
//
//	PIWAY_SIMULATE_DAYS=30 go test ./internal/history -run SimulateDays -v -timeout 0
//
// The figures are in docs/PERFORMANCE.md. Written bytes come from
// /proc/self/io, so they count only on Linux, and the test process's own
// writes, which are the history's alone.
func TestSimulateDays(t *testing.T) {
	days, _ := strconv.Atoi(os.Getenv("PIWAY_SIMULATE_DAYS"))
	if days <= 0 {
		t.Skip("set PIWAY_SIMULATE_DAYS to run")
	}
	dir := t.TempDir()
	c := &clock{t: t0}
	h := open(t, dir, c, Retention{})
	rng := rand.New(rand.NewPCG(1, 2))

	// About the 60 series of a Pi 4: counts and names like the recorder's.
	series := make([]string, 60)
	vals := make([]float64, len(series))
	for i := range series {
		series[i] = fmt.Sprintf("series_%02d", i)
		vals[i] = rng.Float64() * 100
	}
	points := make([]tsdb.Point, len(series))

	writtenAtStart := writeBytes()
	var compactTime, slowest time.Duration
	var compactions int
	rounds := days * 24 * 3600
	for r := range rounds {
		c.t = c.t.Add(time.Second)
		for i := range series {
			vals[i] += rng.Float64() - 0.5
			points[i] = tsdb.Point{Series: series[i], Value: vals[i]}
		}
		if err := h.Append(c.t.UnixMilli(), points); err != nil {
			t.Fatal(err)
		}
		if r%10 == 9 {
			start := time.Now()
			compact(t, h)
			d := time.Since(start)
			compactTime += d
			slowest = max(slowest, d)
			compactions++
		}
		if r%(24*3600) == 24*3600-1 {
			t.Logf("day %d: %s", r/(24*3600)+1, diskUse(t, dir))
		}
	}
	written := writeBytes() - writtenAtStart
	hours := float64(days * 24)
	t.Logf("disk use: %s", diskUse(t, dir))
	t.Logf("compaction: %d runs, %v each on average, %v the slowest", compactions, compactTime/time.Duration(compactions), slowest)
	if written > 0 {
		t.Logf("written: %.1f MB in total, %.2f MB per simulated hour", float64(written)/1e6, float64(written)/1e6/hours)
	}
}

// diskUse is the size of each tier on disk.
func diskUse(t *testing.T, dir string) string {
	t.Helper()
	var out string
	for _, tier := range []string{"raw", "1m", "1h"} {
		var n int64
		err := filepath.WalkDir(filepath.Join(dir, tier), func(_ string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return err
			}
			info, err := e.Info()
			if err == nil {
				n += info.Size()
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		out += fmt.Sprintf("%s %.1f MB  ", tier, float64(n)/1e6)
	}
	return out
}

// writeBytes is the bytes this process has sent to storage, or 0 where
// /proc/self/io does not exist.
func writeBytes() int64 {
	b, err := os.ReadFile("/proc/self/io")
	if err != nil {
		return 0
	}
	for _, line := range bytes.Split(b, []byte("\n")) {
		if v, ok := bytes.CutPrefix(line, []byte("write_bytes: ")); ok {
			n, _ := strconv.ParseInt(string(v), 10, 64)
			return n
		}
	}
	return 0
}

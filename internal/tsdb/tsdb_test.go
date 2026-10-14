package tsdb

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func openStore(t testing.TB, dir string, opts Options) *Store {
	t.Helper()
	s, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// crash releases s the way a killed process would: files are closed, no
// trailer is written and the lock goes away.
func crash(s *Store) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil {
		s.active.f.Close()
	}
	s.lock.Close()
	s.closed = true
}

func mustAppend(t testing.TB, s *Store, ts int64, points ...Point) {
	t.Helper()
	if err := s.Append(ts, points); err != nil {
		t.Fatal(err)
	}
}

func mustSelect(t testing.TB, s *Store, series string, mint, maxt int64) []Sample {
	t.Helper()
	got, err := s.Select(series, mint, maxt)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func segFiles(t testing.TB, dir string) []string {
	t.Helper()
	paths, _, err := segmentPaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestAppendSelectRoundTrip(t *testing.T) {
	s := openStore(t, t.TempDir(), Options{})
	for i := range int64(10) {
		mustAppend(t, s, i*1000,
			Point{"cpu.total", float64(i)},
			Point{"mem.used", float64(i * 100)},
		)
	}
	// A series that first appears later in the segment.
	mustAppend(t, s, 10_000, Point{"temp", math.NaN()}, Point{"cpu.total", 10})

	cpu := mustSelect(t, s, "cpu.total", math.MinInt64, math.MaxInt64)
	if len(cpu) != 11 {
		t.Fatalf("cpu.total: got %d samples, want 11", len(cpu))
	}
	for i, smp := range cpu {
		if smp.T != int64(i)*1000 || smp.V != float64(i) {
			t.Errorf("cpu.total[%d] = %+v", i, smp)
		}
	}
	if got := mustSelect(t, s, "temp", 0, math.MaxInt64); len(got) != 1 || !math.IsNaN(got[0].V) {
		t.Errorf("temp = %v, want one NaN", got)
	}
	if got := mustSelect(t, s, "mem.used", 2000, 4000); !slices.Equal(got, []Sample{{2000, 200}, {3000, 300}, {4000, 400}}) {
		t.Errorf("mem.used[2000,4000] = %v", got)
	}
	if got := mustSelect(t, s, "missing", math.MinInt64, math.MaxInt64); len(got) != 0 {
		t.Errorf("missing series = %v, want none", got)
	}
}

func TestReopenKeepsDataAndStartsNewSegment(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, Options{})
	mustAppend(t, s, 1, Point{"a", 1})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(2, []Point{{"a", 2}}); err != ErrClosed {
		t.Errorf("Append after Close: %v, want ErrClosed", err)
	}

	s = openStore(t, dir, Options{})
	mustAppend(t, s, 2, Point{"a", 2})
	if got := mustSelect(t, s, "a", 0, 10); !slices.Equal(got, []Sample{{1, 1}, {2, 2}}) {
		t.Errorf("a = %v", got)
	}
	s.Close()
	if n := len(segFiles(t, dir)); n != 2 {
		t.Errorf("%d segments, want 2", n)
	}
}

func TestRotationBySizeAndSpan(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, Options{SegmentBytes: 256, SegmentSpan: time.Minute})
	for i := range int64(100) {
		mustAppend(t, s, i*100, Point{"a", float64(i)}) // 10 s of data, size-bound
	}
	bySize := len(segFiles(t, dir))
	if bySize < 3 {
		t.Fatalf("%d segments after 100 appends with 256-byte segments", bySize)
	}
	mustAppend(t, s, 200_000, Point{"a", 100}) // over a minute after the active segment's start
	mustAppend(t, s, 200_001, Point{"a", 101})
	if n := len(segFiles(t, dir)); n != bySize+1 {
		t.Errorf("%d segments, want %d after a span rotation", n, bySize+1)
	}

	got := mustSelect(t, s, "a", math.MinInt64, math.MaxInt64)
	if len(got) != 102 {
		t.Fatalf("got %d samples across segments, want 102", len(got))
	}
	// Segments that cannot overlap the range are skipped, and the result is
	// still exact.
	if got := mustSelect(t, s, "a", 500, 700); !slices.Equal(got, []Sample{{500, 5}, {600, 6}, {700, 7}}) {
		t.Errorf("a[500,700] = %v", got)
	}
}

func TestSelectSortsByTime(t *testing.T) {
	s := openStore(t, t.TempDir(), Options{})
	mustAppend(t, s, 3000, Point{"a", 3})
	mustAppend(t, s, 1000, Point{"a", 1}) // the clock stepped back
	mustAppend(t, s, 2000, Point{"a", 2})
	if got := mustSelect(t, s, "a", 0, 5000); !slices.Equal(got, []Sample{{1000, 1}, {2000, 2}, {3000, 3}}) {
		t.Errorf("a = %v", got)
	}
}

func TestAppendValidates(t *testing.T) {
	s := openStore(t, t.TempDir(), Options{})
	for _, name := range []string{"", strings.Repeat("x", MaxSeriesLen+1)} {
		if err := s.Append(1, []Point{{name, 1}}); err == nil {
			t.Errorf("Append with a %d-byte name succeeded", len(name))
		}
	}
	if err := s.Append(1, nil); err != nil {
		t.Errorf("empty Append: %v", err)
	}
	if n := len(segFiles(t, s.dir)); n != 0 {
		t.Errorf("rejected appends created %d segments", n)
	}
}

func TestSecondOpenIsRefused(t *testing.T) {
	dir := t.TempDir()
	openStore(t, dir, Options{})
	if _, err := Open(dir, Options{}); err == nil || !strings.Contains(err.Error(), "already open") {
		t.Fatalf("second Open: %v, want already open", err)
	}
}

// TestTornTail cuts the active segment at every byte of its last write, the
// way a crash mid-write could, and checks that reopening keeps everything
// before that write.
func TestTornTail(t *testing.T) {
	src := t.TempDir()
	s := openStore(t, src, Options{})
	for i := range int64(5) {
		mustAppend(t, s, i, Point{"a", float64(i)}, Point{"b", float64(-i)})
	}
	lastStart := s.active.dataEnd
	// The last write defines a new series, so it is a series frame followed
	// by a samples frame.
	mustAppend(t, s, 5, Point{"a", 5}, Point{"c", 5})
	path := s.active.path
	crash(s)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for cut := lastStart; cut < int64(len(data)); cut++ {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(path)), data[:cut], 0o644); err != nil {
			t.Fatal(err)
		}
		r := openStore(t, dir, Options{})
		if got := mustSelect(t, r, "a", 0, 10); len(got) != 5 {
			t.Errorf("cut at %d: a has %d samples, want 5", cut, len(got))
		}
		if got := mustSelect(t, r, "c", 0, 10); len(got) != 0 {
			t.Errorf("cut at %d: c = %v, want nothing from the torn write", cut, got)
		}
		// The repaired segment is sealed and stays readable after a restart.
		r.Close()
		r = openStore(t, dir, Options{})
		if got := mustSelect(t, r, "b", 0, 10); len(got) != 5 {
			t.Errorf("cut at %d, reopened: b has %d samples, want 5", cut, len(got))
		}
	}

	// The intact file loses nothing.
	r := openStore(t, src, Options{})
	if got := mustSelect(t, r, "c", 0, 10); !slices.Equal(got, []Sample{{5, 5}}) {
		t.Errorf("uncut: c = %v", got)
	}
}

func TestCorruptTailRecordIsDropped(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, Options{})
	mustAppend(t, s, 1, Point{"a", 1})
	mid := s.active.dataEnd
	mustAppend(t, s, 2, Point{"a", 2})
	mustAppend(t, s, 3, Point{"a", 3})
	path := s.active.path
	crash(s)

	flipByte(t, path, mid+frameHeaderSize+1)
	r := openStore(t, dir, Options{})
	// Everything from the damaged record on is gone; earlier data is kept.
	if got := mustSelect(t, r, "a", 0, 10); !slices.Equal(got, []Sample{{1, 1}}) {
		t.Errorf("a = %v", got)
	}
}

func TestTornHeaderSegmentIsRemoved(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, Options{})
	mustAppend(t, s, 1, Point{"a", 1})
	s.Close()
	// A crash right after creating the next segment.
	torn := filepath.Join(dir, segmentName(2))
	if err := os.WriteFile(torn, []byte("PWS"), 0o644); err != nil {
		t.Fatal(err)
	}

	s = openStore(t, dir, Options{})
	if _, err := os.Stat(torn); !os.IsNotExist(err) {
		t.Errorf("torn segment still exists: %v", err)
	}
	mustAppend(t, s, 2, Point{"a", 2})
	if got := mustSelect(t, s, "a", 0, 10); len(got) != 2 {
		t.Errorf("a = %v", got)
	}
}

func TestCorruptSealedSegmentIsReported(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, Options{})
	mustAppend(t, s, 1, Point{"a", 1})
	s.Close()
	flipByte(t, segFiles(t, dir)[0], headerSize+frameHeaderSize+1)

	s = openStore(t, dir, Options{})
	if _, err := s.Select("a", 0, 10); err == nil {
		t.Fatal("Select over a corrupt sealed segment succeeded")
	}
}

func TestForeignSegmentFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, segmentName(1)), []byte("not a segment at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, Options{}); err == nil {
		t.Fatal("Open accepted a file that is not a segment")
	}
	// The lock was released despite the error.
	os.Remove(filepath.Join(dir, segmentName(1)))
	openStore(t, dir, Options{})
}

func flipByte(t *testing.T, path string, off int64) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[off] ^= 0xff
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

const childEnv = "PIWAY_TSDB_KILL_CHILD"

// TestKill9 runs a writer in a child process, kills it with SIGKILL while it
// is appending, and checks that every recovered round is complete and that
// rounds are contiguous: only the newest rounds may be lost.
func TestKill9(t *testing.T) {
	if dir := os.Getenv(childEnv); dir != "" {
		runKillChild(dir)
		return
	}
	if testing.Short() {
		t.Skip("spawns a child process")
	}

	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestKill9$")
	cmd.Env = append(os.Environ(), childEnv+"="+dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(segFiles(t, dir)) < 4 {
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			t.Fatal("child wrote fewer than 4 segments in 10s")
		}
		time.Sleep(time.Millisecond)
	}
	cmd.Process.Kill()
	cmd.Wait()

	s := openStore(t, dir, Options{})
	a := mustSelect(t, s, "a", math.MinInt64, math.MaxInt64)
	b := mustSelect(t, s, "b", math.MinInt64, math.MaxInt64)
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("recovered %d samples of a and %d of b", len(a), len(b))
	}
	for i := range a {
		if a[i] != (Sample{int64(i), float64(i)}) || b[i] != (Sample{int64(i), -float64(i)}) {
			t.Fatalf("round %d: a=%v b=%v", i, a[i], b[i])
		}
	}
	t.Logf("recovered %d rounds from %d segments", len(a), len(segFiles(t, dir)))
}

func runKillChild(dir string) {
	s, err := Open(dir, Options{SegmentBytes: 4096})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	stop := time.Now().Add(10 * time.Second) // in case the parent dies first
	for i := int64(0); time.Now().Before(stop); i++ {
		if err := s.Append(i, []Point{{"a", float64(i)}, {"b", -float64(i)}}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	os.Exit(0)
}

// benchPoints mimics one agent round: about 30 series.
func benchPoints() []Point {
	points := make([]Point, 30)
	for i := range points {
		points[i].Series = fmt.Sprintf("piway.series.%02d", i)
	}
	return points
}

func BenchmarkAppend(b *testing.B) {
	dir := b.TempDir()
	s := openStore(b, dir, Options{})
	points := benchPoints()
	var t int64
	for b.Loop() {
		t += 1000
		for i := range points {
			points[i].Value = float64(t) * 0.25 * float64(i)
		}
		if err := s.Append(t, points); err != nil {
			b.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		b.Fatal(err)
	}

	var size int64
	for _, p := range segFiles(b, dir) {
		st, err := os.Stat(p)
		if err != nil {
			b.Fatal(err)
		}
		size += st.Size()
	}
	samples := float64(b.N * len(points))
	b.ReportMetric(samples/b.Elapsed().Seconds(), "samples/s")
	b.ReportMetric(float64(size)/samples, "disk-B/sample")
}

func BenchmarkSelect1h(b *testing.B) {
	s := openStore(b, b.TempDir(), Options{})
	points := benchPoints()
	for i := range int64(3600) { // 1h at a 1s interval
		if err := s.Append(i*1000, points); err != nil {
			b.Fatal(err)
		}
	}
	for b.Loop() {
		got, err := s.Select("piway.series.07", 0, math.MaxInt64)
		if err != nil || len(got) != 3600 {
			b.Fatalf("got %d samples, err %v", len(got), err)
		}
	}
}

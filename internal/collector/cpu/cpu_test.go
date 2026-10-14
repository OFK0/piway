package cpu

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantTotal Times
		wantCores int
		wantErr   bool
	}{
		{
			name:      "modern kernel, 10 columns with guest",
			input:     "cpu  10 1 5 100 4 0 2 0 7 3\ncpu0 10 1 5 100 4 0 2 0 7 3\nintr 1 2 3\n",
			wantTotal: Times{User: 10, Nice: 1, System: 5, Idle: 100, IOWait: 4, SoftIRQ: 2},
			wantCores: 1,
		},
		{
			name:      "old kernel, only 4 columns",
			input:     "cpu 1 2 3 4\ncpu0 1 2 3 4\n",
			wantTotal: Times{User: 1, Nice: 2, System: 3, Idle: 4},
			wantCores: 1,
		},
		{
			name:      "no trailing newline",
			input:     "cpu 1 2 3 4",
			wantTotal: Times{User: 1, Nice: 2, System: 3, Idle: 4},
		},
		{name: "missing aggregate line", input: "cpu0 1 2 3 4\n", wantErr: true},
		{name: "too few fields", input: "cpu 1 2 3\n", wantErr: true},
		{name: "non-numeric field", input: "cpu 1 2 x 4\n", wantErr: true},
		{name: "empty", input: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s Stat
			err := Parse([]byte(tt.input), &s)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if s.Total != tt.wantTotal {
				t.Errorf("Total = %+v, want %+v", s.Total, tt.wantTotal)
			}
			if len(s.Cores) != tt.wantCores {
				t.Errorf("len(Cores) = %d, want %d", len(s.Cores), tt.wantCores)
			}
		})
	}
}

func TestParseFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "stat_pi4_a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var s Stat
	if err := Parse(data, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Cores) != 4 {
		t.Fatalf("Pi 4 has 4 cores, parsed %d", len(s.Cores))
	}

	// The aggregate line tracks the sum of the per-core lines, but not exactly:
	// the kernel converts each line from nanoseconds to ticks independently,
	// so rounding drifts by a few ticks.
	var sum uint64
	for _, c := range s.Cores {
		sum += c.total()
	}
	got := s.Total.total()
	if diff := math.Abs(float64(got) - float64(sum)); diff/float64(sum) > 0.001 {
		t.Errorf("aggregate total = %d, sum of cores = %d (off by more than 0.1%%)", got, sum)
	}
}

func TestBusyPercent(t *testing.T) {
	tests := []struct {
		name      string
		prev, cur Times
		want      float64
	}{
		{"idle", Times{Idle: 100}, Times{Idle: 200}, 0},
		{"fully busy", Times{User: 100}, Times{User: 200}, 100},
		{"half busy", Times{User: 0, Idle: 0}, Times{User: 50, Idle: 50}, 50},
		{"iowait counts as idle", Times{}, Times{User: 25, IOWait: 75}, 25},
		{"steal counts as busy", Times{}, Times{Steal: 10, Idle: 90}, 10},
		{"no elapsed time", Times{User: 5}, Times{User: 5}, 0},
		{"counters went backwards", Times{User: 100, Idle: 100}, Times{User: 50, Idle: 50}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := busyPercent(tt.prev, tt.cur); math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("busyPercent = %v, want %v", got, tt.want)
			}
		})
	}
}

func writeStat(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectorSample(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stat")
	writeStat(t, path, "cpu 0 0 0 0\ncpu0 0 0 0 0\ncpu1 0 0 0 0\n")

	c, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var u Usage
	if ok, err := c.Sample(&u); err != nil || ok {
		t.Fatalf("first Sample = (%v, %v), want (false, nil)", ok, err)
	}

	// cpu0 fully busy, cpu1 idle → aggregate 50%.
	writeStat(t, path, "cpu 100 0 0 100\ncpu0 100 0 0 0\ncpu1 0 0 0 100\n")
	ok, err := c.Sample(&u)
	if err != nil || !ok {
		t.Fatalf("second Sample = (%v, %v), want (true, nil)", ok, err)
	}
	if u.Total != 50 {
		t.Errorf("Total = %v, want 50", u.Total)
	}
	if len(u.PerCore) != 2 || u.PerCore[0] != 100 || u.PerCore[1] != 0 {
		t.Errorf("PerCore = %v, want [100 0]", u.PerCore)
	}

	// A core going offline invalidates the previous sample.
	writeStat(t, path, "cpu 200 0 0 100\ncpu0 200 0 0 0\n")
	if ok, err := c.Sample(&u); err != nil || ok {
		t.Fatalf("Sample after core count change = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestCollectorSampleLargeFile(t *testing.T) {
	// Larger than the initial 4 KiB buffer, like /proc/stat on many-core machines.
	path := filepath.Join(t.TempDir(), "stat")
	content := "cpu 1 0 0 1\ncpu0 1 0 0 1\nintr " + strings.Repeat("0 ", 5000) + "\n"
	writeStat(t, path, content)

	c, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var u Usage
	if _, err := c.Sample(&u); err != nil {
		t.Fatal(err)
	}
	if len(c.buf) != len(content) {
		t.Errorf("read %d bytes, want %d", len(c.buf), len(content))
	}
}

func TestCollectorSampleDoesNotAllocate(t *testing.T) {
	c, err := New(filepath.Join("testdata", "stat_pi4_a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var u Usage
	for range 2 { // prime buffers and slices
		if _, err := c.Sample(&u); err != nil {
			t.Fatal(err)
		}
	}
	allocs := testing.AllocsPerRun(100, func() {
		if _, err := c.Sample(&u); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Errorf("Sample allocates %v times per call, want 0", allocs)
	}
}

func BenchmarkSampleProcStat(b *testing.B) {
	c, err := New(DefaultPath)
	if err != nil {
		b.Skipf("no %s: %v", DefaultPath, err)
	}
	defer c.Close()

	var u Usage
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Sample(&u); err != nil {
			b.Fatal(err)
		}
	}
}

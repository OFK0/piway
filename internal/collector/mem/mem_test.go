package mem

import (
	"os"
	"path/filepath"
	"testing"
)

const kib = 1024

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Info
		wantErr bool
	}{
		{
			name: "modern kernel",
			input: "MemTotal:  1000 kB\nMemFree:  100 kB\nMemAvailable:  600 kB\n" +
				"Buffers:  50 kB\nCached:  400 kB\nSwapTotal:  200 kB\nSwapFree:  150 kB\n" +
				"HugePages_Total:       0\n",
			want: Info{
				Total: 1000 * kib, Free: 100 * kib, Available: 600 * kib, Used: 400 * kib,
				Buffers: 50 * kib, Cached: 400 * kib,
				SwapTotal: 200 * kib, SwapFree: 150 * kib, SwapUsed: 50 * kib,
			},
		},
		{
			name:  "no MemAvailable falls back to free+buffers+cached",
			input: "MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 50 kB\nCached: 250 kB\n",
			want: Info{
				Total: 1000 * kib, Free: 100 * kib, Available: 400 * kib, Used: 600 * kib,
				Buffers: 50 * kib, Cached: 250 * kib,
			},
		},
		{
			name:  "no swap configured",
			input: "MemTotal: 1000 kB\nMemAvailable: 1000 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB",
			want:  Info{Total: 1000 * kib, Available: 1000 * kib},
		},
		{name: "missing MemTotal", input: "MemFree: 100 kB\n", wantErr: true},
		{name: "non-numeric value", input: "MemTotal: 1x00 kB\n", wantErr: true},
		{name: "unknown unit", input: "MemTotal: 1000 MB\n", wantErr: true},
		{name: "empty value", input: "MemTotal:\n", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("Parse =\n %+v\nwant\n %+v", got, tt.want)
			}
		})
	}
}

func TestParseFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "meminfo_pi4.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}

	want := Info{
		Total:     3886840 * kib,
		Free:      1892280 * kib,
		Available: 3298464 * kib,
		Used:      (3886840 - 3298464) * kib,
		Buffers:   72536 * kib,
		Cached:    1357416 * kib,
		SwapTotal: 2097148 * kib,
		SwapFree:  2097148 * kib,
	}
	if got != want {
		t.Errorf("Parse =\n %+v\nwant\n %+v", got, want)
	}
}

func TestCollectorSample(t *testing.T) {
	c, err := New(filepath.Join("testdata", "meminfo_pi4.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	info, err := c.Sample()
	if err != nil {
		t.Fatal(err)
	}
	if info.Total != 3886840*kib {
		t.Errorf("Total = %d, want %d", info.Total, 3886840*kib)
	}

	allocs := testing.AllocsPerRun(100, func() {
		if _, err := c.Sample(); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Errorf("Sample allocates %v times per call, want 0", allocs)
	}
}

func BenchmarkSampleProcMeminfo(b *testing.B) {
	c, err := New(DefaultPath)
	if err != nil {
		b.Skipf("no %s: %v", DefaultPath, err)
	}
	defer c.Close()

	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Sample(); err != nil {
			b.Fatal(err)
		}
	}
}

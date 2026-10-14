package disk

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ofk0/piway/internal/procfs"
)

func TestParseLine(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantName string
		want     Counters
		wantErr  bool
	}{
		{
			name:     "kernel 5.5+ with discard and flush columns",
			line:     " 179       0 mmcblk0 9000 10 180000 500 4000 20 96000 900 0 1200 1400 0 0 0 0 0 0",
			wantName: "mmcblk0",
			want:     Counters{ReadsCompleted: 9000, SectorsRead: 180000, WritesCompleted: 4000, SectorsWritten: 96000, IOTimeMs: 1200},
		},
		{
			name:     "old kernel, exactly 11 columns",
			line:     "   8       0 sda 1 0 2 0 3 0 4 0 0 5 0",
			wantName: "sda",
			want:     Counters{ReadsCompleted: 1, SectorsRead: 2, WritesCompleted: 3, SectorsWritten: 4, IOTimeMs: 5},
		},
		{name: "too few columns", line: "8 0 sda 1 2 3", wantErr: true},
		{name: "non-numeric column", line: "8 0 sda 1 0 2 0 x 0 4 0 0 5 0", wantErr: true},
		{name: "empty", line: "   ", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, got, err := ParseLine([]byte(tt.line))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(name) != tt.wantName || got != tt.want {
				t.Errorf("ParseLine = %q %+v, want %q %+v", name, got, tt.wantName, tt.want)
			}
		})
	}
}

func TestParseFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "diskstats_pi4.txt"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for len(data) > 0 {
		var line []byte
		line, data = procfs.NextLine(data)
		name, _, err := ParseLine(line)
		if err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		found = found || string(name) == "mmcblk0"
	}
	if !found {
		t.Error("mmcblk0 not found in fixture")
	}
}

type fixture struct {
	t         *testing.T
	diskstats string
	sysBlock  string
}

// newFixture fakes /sys/block with physical disks that have a "device" entry
// and virtual ones that do not.
func newFixture(t *testing.T, physical, virtual []string) fixture {
	t.Helper()
	root := t.TempDir()
	f := fixture{t: t, diskstats: filepath.Join(root, "diskstats"), sysBlock: filepath.Join(root, "block")}
	for _, n := range physical {
		if err := os.MkdirAll(filepath.Join(f.sysBlock, n, "device"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range virtual {
		if err := os.MkdirAll(filepath.Join(f.sysBlock, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.write("")
	return f
}

func (f fixture) write(content string) {
	f.t.Helper()
	if err := os.WriteFile(f.diskstats, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f fixture) collector() *Collector {
	f.t.Helper()
	c, err := New(Config{DiskstatsPath: f.diskstats, SysBlockPath: f.sysBlock, MountPoints: []string{f.t.TempDir()}})
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { c.Close() })
	return c
}

func TestSampleRates(t *testing.T) {
	f := newFixture(t, []string{"mmcblk0"}, []string{"loop0", "zram0"})
	c := f.collector()
	t0 := time.Now()
	var s Stats

	f.write("179 0 mmcblk0 100 0 1000 0 50 0 2000 0 0 100 0\n" +
		"179 2 mmcblk0p2 100 0 1000 0 50 0 2000 0 0 100 0\n" +
		"7 0 loop0 5 0 5 0 5 0 5 0 0 5 0\n" +
		"254 0 zram0 5 0 5 0 5 0 5 0 0 5 0\n")
	if err := c.Sample(t0, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Devices) != 0 {
		t.Fatalf("first sample reported devices %+v, want none", s.Devices)
	}
	if len(s.Filesystems) != 1 || s.Filesystems[0].TotalBytes == 0 {
		t.Fatalf("Filesystems = %+v, want one with a size", s.Filesystems)
	}

	// Two seconds later: +2048 sectors read, +4096 sectors written, +500 ms busy.
	f.write("179 0 mmcblk0 140 0 3048 0 90 0 6096 0 0 600 0\n" +
		"179 2 mmcblk0p2 140 0 3048 0 90 0 6096 0 0 600 0\n" +
		"7 0 loop0 9 0 9 0 9 0 9 0 0 9 0\n" +
		"254 0 zram0 9 0 9 0 9 0 9 0 0 9 0\n")
	if err := c.Sample(t0.Add(2*time.Second), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Devices) != 1 {
		t.Fatalf("Devices = %+v, want only mmcblk0 (no partitions, loop or zram)", s.Devices)
	}
	want := Device{
		Name:             "mmcblk0",
		ReadBytesPerSec:  2048 * 512 / 2,
		WriteBytesPerSec: 4096 * 512 / 2,
		ReadOpsPerSec:    20,
		WriteOpsPerSec:   20,
		BusyPercent:      25,
		WrittenBytes:     6096 * 512,
	}
	if s.Devices[0] != want {
		t.Errorf("Device =\n %+v\nwant\n %+v", s.Devices[0], want)
	}
}

func TestSampleDeviceComesAndGoes(t *testing.T) {
	f := newFixture(t, []string{"mmcblk0", "sda"}, nil)
	c := f.collector()
	t0 := time.Now()
	var s Stats
	mmc := "179 0 mmcblk0 0 0 0 0 0 0 0 0 0 0 0\n"

	f.write(mmc)
	_ = c.Sample(t0, &s)

	// A USB disk is plugged in: it needs one sample as a baseline.
	f.write(mmc + "8 0 sda 0 0 0 0 0 0 0 0 0 0 0\n")
	if err := c.Sample(t0.Add(time.Second), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Devices) != 1 || s.Devices[0].Name != "mmcblk0" {
		t.Fatalf("Devices = %+v, want only mmcblk0 while sda has no baseline", s.Devices)
	}

	f.write(mmc + "8 0 sda 0 0 200 0 0 0 0 0 0 0 0\n")
	if err := c.Sample(t0.Add(2*time.Second), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Devices) != 2 || s.Devices[1].ReadBytesPerSec != 200*512 {
		t.Fatalf("Devices = %+v, want sda reading 102400 B/s", s.Devices)
	}

	// Unplugged, then a different disk shows up under the same name with lower
	// counters: it must not produce a rate against the old disk's baseline.
	f.write(mmc)
	_ = c.Sample(t0.Add(3*time.Second), &s)
	if _, ok := c.devices["sda"]; ok {
		t.Error("sda still tracked after it disappeared")
	}
	f.write(mmc + "8 0 sda 0 0 10 0 0 0 0 0 0 0 0\n")
	_ = c.Sample(t0.Add(4*time.Second), &s)
	if len(s.Devices) != 1 {
		t.Errorf("Devices = %+v, want re-plugged sda to wait for a baseline", s.Devices)
	}
}

func TestSampleCounterResetClampsToZero(t *testing.T) {
	f := newFixture(t, []string{"sda"}, nil)
	c := f.collector()
	t0 := time.Now()
	var s Stats

	f.write("8 0 sda 100 0 100 0 100 0 100 0 0 100 0\n")
	_ = c.Sample(t0, &s)
	f.write("8 0 sda 1 0 1 0 1 0 1 0 0 1 0\n")
	if err := c.Sample(t0.Add(time.Second), &s); err != nil {
		t.Fatal(err)
	}
	d := s.Devices[0]
	if d.ReadBytesPerSec != 0 || d.WriteBytesPerSec != 0 || d.BusyPercent != 0 {
		t.Errorf("Device = %+v, want zero rates after a counter reset", d)
	}
}

func TestStatFSRoot(t *testing.T) {
	fs, err := statFS("/")
	if err != nil {
		t.Fatal(err)
	}
	if fs.TotalBytes == 0 || fs.UsedBytes > fs.TotalBytes || fs.UsedPercent < 0 || fs.UsedPercent > 100 {
		t.Errorf("statFS(/) = %+v, looks implausible", fs)
	}
	if _, err := statFS("/does/not/exist"); err == nil {
		t.Error("expected error for missing mount point")
	}
}

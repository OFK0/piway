package netdev

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const header = "Inter-|   Receive                                                |  Transmit\n" +
	" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n"

func TestParseLine(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantName string
		want     Counters
		wantOK   bool
		wantErr  bool
	}{
		{
			name:     "padded name",
			line:     " wlan0: 2000 20 1 2 0 0 0 5 1000 10 3 4 0 0 0 0",
			wantName: "wlan0",
			want:     Counters{RxBytes: 2000, RxPackets: 20, RxErrors: 1, RxDropped: 2, TxBytes: 1000, TxPackets: 10, TxErrors: 3, TxDropped: 4},
			wantOK:   true,
		},
		{
			// Old kernels print no space after the colon once the byte count is wide.
			name:     "no space after colon",
			line:     "  eth0:123 1 0 0 0 0 0 0 456 2 0 0 0 0 0 0",
			wantName: "eth0",
			want:     Counters{RxBytes: 123, RxPackets: 1, TxBytes: 456, TxPackets: 2},
			wantOK:   true,
		},
		{name: "header line", line: "Inter-|   Receive |  Transmit"},
		{name: "too few fields", line: "eth0: 1 2 3", wantErr: true},
		{name: "non-numeric field", line: "eth0: 1 2 3 4 5 6 7 8 9 x 11 12 13 14 15 16", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, got, ok, err := ParseLine([]byte(tt.line))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != tt.wantOK || string(name) != tt.wantName || got != tt.want {
				t.Errorf("ParseLine = %q %+v ok=%v, want %q %+v ok=%v", name, got, ok, tt.wantName, tt.want, tt.wantOK)
			}
		})
	}
}

func TestDefaultSkip(t *testing.T) {
	for name, want := range map[string]bool{
		"lo": true, "veth1a2b3c": true,
		"eth0": false, "wlan0": false, "wg0": false, "docker0": false, "lowpan0": false,
	} {
		if got := DefaultSkip(name); got != want {
			t.Errorf("DefaultSkip(%q) = %v, want %v", name, got, want)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSampleFixture(t *testing.T) {
	c, err := New(filepath.Join("testdata", "net_dev_sample.txt"), DefaultSkip)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	t0 := time.Now()
	var ifs []Interface
	if ifs, err = c.Sample(t0, ifs); err != nil || len(ifs) != 0 {
		t.Fatalf("first Sample = %+v, %v; want empty", ifs, err)
	}
	if ifs, err = c.Sample(t0.Add(time.Second), ifs); err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, i := range ifs {
		names = append(names, i.Name)
		if i.RxBytesPerSec != 0 || i.TxBytesPerSec != 0 {
			t.Errorf("%s: unchanged file should give zero rates, got %+v", i.Name, i)
		}
	}
	if got, want := len(names), 3; got != want {
		t.Errorf("interfaces = %v, want eth0, wlan0, docker0 (lo and veth skipped)", names)
	}
}

func TestSampleRates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev")
	writeFile(t, path, header+"  eth0: 1000 10 0 0 0 0 0 0 500 5 0 0 0 0 0 0\n")
	c, err := New(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	t0 := time.Now()
	ifs, _ := c.Sample(t0, nil)

	// Four seconds later: +4000 B in, +2000 B out, one new RX error.
	writeFile(t, path, header+"  eth0: 5000 50 1 0 0 0 0 0 2500 25 0 0 0 0 0 0\n")
	ifs, err = c.Sample(t0.Add(4*time.Second), ifs)
	if err != nil {
		t.Fatal(err)
	}
	want := Interface{
		Name: "eth0", RxBytesPerSec: 1000, TxBytesPerSec: 500, RxPacketsPerSec: 10, TxPacketsPerSec: 5, RxErrors: 1,
		Totals: Counters{RxBytes: 5000, RxPackets: 50, RxErrors: 1, TxBytes: 2500, TxPackets: 25},
	}
	if len(ifs) != 1 || ifs[0] != want {
		t.Fatalf("Sample = %+v, want [%+v]", ifs, want)
	}

	// Interface bounced and the driver reset its counters.
	writeFile(t, path, header+"  eth0: 10 1 0 0 0 0 0 0 10 1 0 0 0 0 0 0\n")
	if ifs, err = c.Sample(t0.Add(5*time.Second), ifs); err != nil {
		t.Fatal(err)
	}
	if ifs[0].RxBytesPerSec != 0 || ifs[0].TxBytesPerSec != 0 {
		t.Errorf("after counter reset = %+v, want zero rates", ifs[0])
	}
}

func TestNewMissingFile(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "missing"), nil); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestSampleInterfaceGoesAway(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev")
	eth := "  eth0: 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n"
	vpn := "   wg0: 100 1 0 0 0 0 0 0 100 1 0 0 0 0 0 0\n"
	writeFile(t, path, header+eth+vpn)

	c, err := New(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	t0 := time.Now()
	ifs, _ := c.Sample(t0, nil)

	writeFile(t, path, header+eth)
	ifs, _ = c.Sample(t0.Add(time.Second), ifs)
	if _, ok := c.ifaces["wg0"]; ok {
		t.Error("wg0 still tracked after it disappeared")
	}

	// VPN comes back with fresh, lower counters: no rate until it has a baseline.
	writeFile(t, path, header+eth+"   wg0: 5 1 0 0 0 0 0 0 5 1 0 0 0 0 0 0\n")
	ifs, _ = c.Sample(t0.Add(2*time.Second), ifs)
	if len(ifs) != 1 || ifs[0].Name != "eth0" {
		t.Errorf("Sample = %+v, want only eth0", ifs)
	}
}

func TestSampleDoesNotAllocate(t *testing.T) {
	c, err := New(filepath.Join("testdata", "net_dev_sample.txt"), DefaultSkip)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	now := time.Now()
	var ifs []Interface
	for range 2 {
		now = now.Add(time.Second)
		ifs, _ = c.Sample(now, ifs)
	}
	allocs := testing.AllocsPerRun(100, func() {
		now = now.Add(time.Second)
		if ifs, err = c.Sample(now, ifs); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Errorf("Sample allocates %v times per call, want 0", allocs)
	}
}

func BenchmarkSampleProcNetDev(b *testing.B) {
	c, err := New(DefaultPath, DefaultSkip)
	if err != nil {
		b.Skipf("no %s: %v", DefaultPath, err)
	}
	defer c.Close()

	now := time.Now()
	var ifs []Interface
	b.ReportAllocs()
	for b.Loop() {
		now = now.Add(time.Second)
		if ifs, err = c.Sample(now, ifs); err != nil {
			b.Fatal(err)
		}
	}
}

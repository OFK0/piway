package thermal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDecode(t *testing.T) {
	tests := []struct {
		name string
		mask uint32
		want Throttle
	}{
		{"healthy", 0x0, Throttle{}},
		{
			// Typical weak power supply: under-voltage and throttling right now.
			name: "under-voltage now and since boot",
			mask: 0x50005,
			want: Throttle{
				Raw:       0x50005,
				Now:       State{UnderVoltage: true, Throttled: true},
				SinceBoot: State{UnderVoltage: true, Throttled: true},
			},
		},
		{
			name: "recovered: only the since-boot bits remain",
			mask: 0xA0000,
			want: Throttle{Raw: 0xA0000, SinceBoot: State{FreqCapped: true, SoftTempLimit: true}},
		},
		{
			name: "all eight flags",
			mask: 0xF000F,
			want: Throttle{
				Raw:       0xF000F,
				Now:       State{true, true, true, true},
				SinceBoot: State{true, true, true, true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Decode(tt.mask); got != tt.want {
				t.Errorf("Decode(%#x) = %+v, want %+v", tt.mask, got, tt.want)
			}
		})
	}
}

func TestParseThrottled(t *testing.T) {
	tests := []struct {
		in      string
		want    uint32
		wantErr bool
	}{
		{in: "throttled=0x0\n", want: 0},
		{in: "throttled=0x50005\n", want: 0x50005},
		{in: "50005\n", want: 0x50005}, // sysfs form
		{in: "0\n", want: 0},
		{in: "throttled=nope\n", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tt := range tests {
		got, err := parseThrottled([]byte(tt.in))
		if (err != nil) != tt.wantErr {
			t.Errorf("parseThrottled(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("parseThrottled(%q) = %#x, want %#x", tt.in, got, tt.want)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindCPUZonePrefersCPUThermal(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "thermal_zone0", "type"), "wifi-thermal\n")
	writeFile(t, filepath.Join(root, "thermal_zone1", "type"), "cpu-thermal\n")

	if got, want := findCPUZone(root), filepath.Join(root, "thermal_zone1", "temp"); got != want {
		t.Errorf("findCPUZone = %q, want %q", got, want)
	}
}

func TestFindCPUZoneFallsBackToFirst(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "thermal_zone0", "type"), "x86_pkg_temp\n")

	if got, want := findCPUZone(root), filepath.Join(root, "thermal_zone0", "temp"); got != want {
		t.Errorf("findCPUZone = %q, want %q", got, want)
	}
	if got := findCPUZone(t.TempDir()); got != "" {
		t.Errorf("findCPUZone on empty root = %q, want empty", got)
	}
}

func TestSampleSysfs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "thermal", "thermal_zone0", "type"), "cpu-thermal\n")
	writeFile(t, filepath.Join(root, "thermal", "thermal_zone0", "temp"), "45277\n")
	throttled := filepath.Join(root, "get_throttled")
	writeFile(t, throttled, "50005\n")

	c, err := New(Config{ThermalRoot: filepath.Join(root, "thermal"), ThrottledSysfsPath: throttled})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	r, err := c.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.TempCelsius == nil || *r.TempCelsius != 45.277 {
		t.Errorf("TempCelsius = %v, want 45.277", r.TempCelsius)
	}
	if r.Throttle == nil || !r.Throttle.Now.UnderVoltage {
		t.Errorf("Throttle = %+v, want under-voltage now", r.Throttle)
	}
}

func TestSampleFallsBackToVcgencmd(t *testing.T) {
	c, err := New(Config{ThrottledSysfsPath: filepath.Join(t.TempDir(), "missing"), Vcgencmd: "unused"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.vcgencmd == nil {
		t.Fatal("expected vcgencmd fallback when sysfs file is missing")
	}
	c.vcgencmd = func(context.Context) ([]byte, error) { return []byte("throttled=0x80000\n"), nil }

	r, err := c.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Throttle == nil || !r.Throttle.SinceBoot.SoftTempLimit || r.Throttle.Now.SoftTempLimit {
		t.Errorf("Throttle = %+v, want soft temp limit since boot only", r.Throttle)
	}

	c.vcgencmd = func(context.Context) ([]byte, error) { return nil, errors.New("boom") }
	if _, err := c.Sample(context.Background()); err == nil {
		t.Error("expected vcgencmd failure to surface as an error")
	}
}

func TestSampleNotAPi(t *testing.T) {
	// No thermal zones, no sysfs file, no vcgencmd: everything is nil, nothing fails.
	c, err := New(Config{ThermalRoot: t.TempDir(), ThrottledSysfsPath: filepath.Join(t.TempDir(), "missing")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	r, err := c.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.TempCelsius != nil || r.Throttle != nil {
		t.Errorf("Reading = %+v, want all nil", r)
	}
}

func TestSampleRealHardware(t *testing.T) {
	c, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	r, err := c.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.TempCelsius == nil {
		t.Skip("no thermal zone on this machine")
	}
	if *r.TempCelsius < 0 || *r.TempCelsius > 120 {
		t.Errorf("TempCelsius = %v, outside plausible SoC range", *r.TempCelsius)
	}
	t.Logf("temp=%.1f°C throttle=%+v", *r.TempCelsius, r.Throttle)
}

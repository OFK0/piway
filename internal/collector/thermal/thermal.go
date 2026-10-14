// Package thermal reports SoC temperature and Raspberry Pi throttling state.
package thermal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ofk0/piway/internal/procfs"
)

// Default locations. ThrottledSysfsPath only exists on older Raspberry Pi kernels;
// newer ones expose the value through the firmware mailbox, which vcgencmd queries.
const (
	ThermalRoot        = "/sys/class/thermal"
	ThrottledSysfsPath = "/sys/devices/platform/soc/soc:firmware/get_throttled"
)

// vcgencmdTimeout bounds a single vcgencmd call so a wedged firmware cannot stall sampling.
const vcgencmdTimeout = 2 * time.Second

// State is one half of the firmware throttled bitmask.
type State struct {
	UnderVoltage  bool `json:"under_voltage"`
	FreqCapped    bool `json:"freq_capped"`
	Throttled     bool `json:"throttled"`
	SoftTempLimit bool `json:"soft_temp_limit"`
}

// Throttle is the decoded firmware throttled bitmask.
type Throttle struct {
	Raw       uint32 `json:"raw"`
	Now       State  `json:"now"`
	SinceBoot State  `json:"since_boot"`
}

// Decode splits the bitmask: bits 0-3 describe the current state,
// bits 16-19 latch once the same condition has happened since boot.
func Decode(mask uint32) Throttle {
	state := func(m uint32) State {
		return State{
			UnderVoltage:  m&(1<<0) != 0,
			FreqCapped:    m&(1<<1) != 0,
			Throttled:     m&(1<<2) != 0,
			SoftTempLimit: m&(1<<3) != 0,
		}
	}
	return Throttle{Raw: mask, Now: state(mask), SinceBoot: state(mask >> 16)}
}

// Reading is one thermal sample. A nil field means the source is not
// available on this machine (for example throttling on non-Pi hardware).
type Reading struct {
	TempCelsius *float64  `json:"temp_celsius"`
	Throttle    *Throttle `json:"throttle"`
}

// parseMilliCelsius parses sysfs thermal output such as "45277\n".
func parseMilliCelsius(b []byte) (float64, error) {
	v, err := strconv.ParseInt(string(bytes.TrimSpace(b)), 10, 64)
	if err != nil {
		return 0, err
	}
	return float64(v) / 1000, nil
}

// parseThrottled accepts both the sysfs form ("50005") and the vcgencmd form
// ("throttled=0x50005"); both are hexadecimal.
func parseThrottled(b []byte) (uint32, error) {
	b = bytes.TrimSpace(b)
	b = bytes.TrimPrefix(b, []byte("throttled="))
	b = bytes.TrimPrefix(b, []byte("0x"))
	v, err := strconv.ParseUint(string(b), 16, 32)
	if err != nil {
		return 0, fmt.Errorf("parse throttled %q: %w", b, err)
	}
	return uint32(v), nil
}

// findCPUZone returns the temp file of the "cpu-thermal" zone, falling back to
// the first zone. It returns "" when the machine exposes no thermal zones.
func findCPUZone(root string) string {
	zones, _ := filepath.Glob(filepath.Join(root, "thermal_zone*"))
	for _, z := range zones {
		if t, err := os.ReadFile(filepath.Join(z, "type")); err == nil && string(bytes.TrimSpace(t)) == "cpu-thermal" {
			return filepath.Join(z, "temp")
		}
	}
	if len(zones) > 0 {
		return filepath.Join(zones[0], "temp")
	}
	return ""
}

// Config selects data sources. Empty fields disable that source.
type Config struct {
	ThermalRoot        string
	ThrottledSysfsPath string
	Vcgencmd           string // path to the vcgencmd binary
}

// DefaultConfig probes the standard locations.
func DefaultConfig() Config {
	cfg := Config{ThermalRoot: ThermalRoot, ThrottledSysfsPath: ThrottledSysfsPath}
	if p, err := exec.LookPath("vcgencmd"); err == nil {
		cfg.Vcgencmd = p
	}
	return cfg
}

// Collector samples temperature and throttling state.
// A Collector is not safe for concurrent use.
type Collector struct {
	temp      *procfs.File
	throttled *procfs.File
	vcgencmd  func(ctx context.Context) ([]byte, error)
}

// New opens whichever sources cfg points to and that exist on this machine.
// Missing sources are not an error; their fields are reported as nil.
func New(cfg Config) (*Collector, error) {
	c := &Collector{}

	if cfg.ThermalRoot != "" {
		if path := findCPUZone(cfg.ThermalRoot); path != "" {
			f, err := procfs.Open(path)
			if err != nil {
				return nil, fmt.Errorf("thermal: %w", err)
			}
			c.temp = f
		}
	}

	if cfg.ThrottledSysfsPath != "" {
		f, err := procfs.Open(cfg.ThrottledSysfsPath)
		switch {
		case err == nil:
			c.throttled = f
		case !errors.Is(err, os.ErrNotExist):
			c.Close()
			return nil, fmt.Errorf("thermal: %w", err)
		}
	}

	if c.throttled == nil && cfg.Vcgencmd != "" {
		bin := cfg.Vcgencmd
		c.vcgencmd = func(ctx context.Context) ([]byte, error) {
			ctx, cancel := context.WithTimeout(ctx, vcgencmdTimeout)
			defer cancel()
			return exec.CommandContext(ctx, bin, "get_throttled").Output()
		}
	}

	return c, nil
}

// Close releases any open files.
func (c *Collector) Close() error {
	var errs []error
	if c.temp != nil {
		errs = append(errs, c.temp.Close())
	}
	if c.throttled != nil {
		errs = append(errs, c.throttled.Close())
	}
	return errors.Join(errs...)
}

// Sample reads the current temperature and throttling state.
func (c *Collector) Sample(ctx context.Context) (Reading, error) {
	var r Reading

	if c.temp != nil {
		data, err := c.temp.Read()
		if err != nil {
			return Reading{}, fmt.Errorf("thermal: read temp: %w", err)
		}
		t, err := parseMilliCelsius(data)
		if err != nil {
			return Reading{}, fmt.Errorf("thermal: parse temp: %w", err)
		}
		r.TempCelsius = &t
	}

	var raw []byte
	var err error
	switch {
	case c.throttled != nil:
		raw, err = c.throttled.Read()
	case c.vcgencmd != nil:
		// vcgencmd costs a process spawn (~4 ms on a Pi 4); acceptable at
		// one sample per second, and the only option on kernels without sysfs.
		raw, err = c.vcgencmd(ctx)
	default:
		return r, nil
	}
	if err != nil {
		return Reading{}, fmt.Errorf("thermal: read throttled: %w", err)
	}
	mask, err := parseThrottled(raw)
	if err != nil {
		return Reading{}, fmt.Errorf("thermal: %w", err)
	}
	t := Decode(mask)
	r.Throttle = &t
	return r, nil
}

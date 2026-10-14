// Package mem reports memory and swap usage from /proc/meminfo.
package mem

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/ofk0/piway/internal/procfs"
)

// DefaultPath is where Linux exposes memory statistics.
const DefaultPath = "/proc/meminfo"

// Info is a memory snapshot. All values are in bytes.
type Info struct {
	Total     uint64 `json:"total_bytes"`
	Free      uint64 `json:"free_bytes"`
	Available uint64 `json:"available_bytes"`
	Used      uint64 `json:"used_bytes"`
	Buffers   uint64 `json:"buffers_bytes"`
	Cached    uint64 `json:"cached_bytes"`
	SwapTotal uint64 `json:"swap_total_bytes"`
	SwapFree  uint64 `json:"swap_free_bytes"`
	SwapUsed  uint64 `json:"swap_used_bytes"`
}

var errNoTotal = errors.New("mem: MemTotal missing from /proc/meminfo")

// Parse decodes /proc/meminfo.
//
// Used is Total minus Available, not Total minus Free: Linux fills spare RAM
// with page cache, so MemFree is usually tiny on a healthy machine while most
// of that cache can be reclaimed instantly. MemAvailable is the kernel's own
// estimate of memory that can be handed to new workloads without swapping.
func Parse(data []byte) (Info, error) {
	var info Info
	var haveTotal, haveAvailable bool

	for len(data) > 0 {
		var line []byte
		line, data = procfs.NextLine(data)

		key, rest, ok := bytes.Cut(line, []byte{':'})
		if !ok {
			continue
		}

		var dst *uint64
		// switch on string(key) is optimised by the compiler and does not allocate.
		switch string(key) {
		case "MemTotal":
			dst, haveTotal = &info.Total, true
		case "MemFree":
			dst = &info.Free
		case "MemAvailable":
			dst, haveAvailable = &info.Available, true
		case "Buffers":
			dst = &info.Buffers
		case "Cached":
			dst = &info.Cached
		case "SwapTotal":
			dst = &info.SwapTotal
		case "SwapFree":
			dst = &info.SwapFree
		default:
			continue
		}

		v, err := parseKB(rest)
		if err != nil {
			return Info{}, fmt.Errorf("mem: %s: %w", key, err)
		}
		*dst = v
	}

	if !haveTotal {
		return Info{}, errNoTotal
	}
	if !haveAvailable {
		// Kernels before 3.14 have no MemAvailable; this is the classic approximation.
		info.Available = min(info.Free+info.Buffers+info.Cached, info.Total)
	}
	info.Used = info.Total - min(info.Available, info.Total)
	info.SwapUsed = info.SwapTotal - min(info.SwapFree, info.SwapTotal)
	return info, nil
}

// parseKB parses a value such as "   3886840 kB" and returns it in bytes.
func parseKB(b []byte) (uint64, error) {
	num, rest := procfs.NextField(b)
	unit, _ := procfs.NextField(rest)
	v, err := procfs.ParseUint(num)
	if err != nil {
		return 0, err
	}

	switch string(unit) {
	case "kB":
		// The kernel's "kB" is actually KiB.
		return v * 1024, nil
	case "":
		return v, nil
	default:
		return 0, fmt.Errorf("unknown unit %q", unit)
	}
}

// Collector samples /proc/meminfo. A Collector is not safe for concurrent use.
type Collector struct {
	file *procfs.File
}

// New opens path (normally DefaultPath) for sampling.
func New(path string) (*Collector, error) {
	f, err := procfs.Open(path)
	if err != nil {
		return nil, fmt.Errorf("mem: %w", err)
	}
	return &Collector{file: f}, nil
}

// Close releases the underlying file.
func (c *Collector) Close() error {
	return c.file.Close()
}

// Sample reads the current memory statistics.
func (c *Collector) Sample() (Info, error) {
	data, err := c.file.Read()
	if err != nil {
		return Info{}, fmt.Errorf("mem: read: %w", err)
	}
	return Parse(data)
}

// Package cpu reports CPU utilisation computed from /proc/stat.
//
// The kernel exposes cumulative time counters (in USER_HZ ticks) per CPU, so a
// single read says nothing about current load. Utilisation is the share of
// non-idle time between two consecutive samples.
package cpu

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/ofk0/piway/internal/procfs"
)

// DefaultPath is where Linux exposes CPU time counters.
const DefaultPath = "/proc/stat"

// Times holds the cumulative counters of one "cpu" line in /proc/stat.
//
// guest and guest_nice are intentionally omitted: the kernel already
// accounts them inside user and nice, so adding them would double count.
type Times struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal uint64
}

func (t Times) total() uint64 {
	return t.User + t.Nice + t.System + t.Idle + t.IOWait + t.IRQ + t.SoftIRQ + t.Steal
}

// idle counts iowait as idle: the CPU was not executing anything while waiting on I/O.
func (t Times) idle() uint64 {
	return t.Idle + t.IOWait
}

// Stat is one parsed /proc/stat snapshot.
type Stat struct {
	Total Times   // aggregate "cpu" line
	Cores []Times // "cpu0", "cpu1", ... in kernel order
}

// Usage is CPU utilisation between two samples, as percentages in [0, 100].
type Usage struct {
	Total   float64   `json:"total"`
	PerCore []float64 `json:"per_core"`
}

var (
	errNoAggregate = errors.New("cpu: no aggregate cpu line in /proc/stat")
	cpuPrefix      = []byte("cpu")
)

// Parse decodes the cpu lines of /proc/stat into s, reusing s.Cores' backing array.
func Parse(data []byte, s *Stat) error {
	s.Cores = s.Cores[:0]
	seenAggregate := false

	for len(data) > 0 {
		var line []byte
		line, data = procfs.NextLine(data)

		if !bytes.HasPrefix(line, cpuPrefix) {
			// cpu lines are always first; once past them there is nothing left to read.
			if seenAggregate {
				break
			}
			continue
		}

		name, fields := procfs.NextField(line)
		t, err := parseTimes(fields)
		if err != nil {
			return fmt.Errorf("cpu: line %q: %w", name, err)
		}

		if len(name) == len(cpuPrefix) {
			s.Total = t
			seenAggregate = true
		} else {
			s.Cores = append(s.Cores, t)
		}
	}

	if !seenAggregate {
		return errNoAggregate
	}
	return nil
}

// minFields is user, nice, system, idle: the columns every kernel since 2.6 provides.
const minFields = 4

func parseTimes(b []byte) (Times, error) {
	var v [8]uint64
	n := 0
	for n < len(v) {
		var field []byte
		field, b = procfs.NextField(b)
		if len(field) == 0 {
			break
		}
		x, err := procfs.ParseUint(field)
		if err != nil {
			return Times{}, err
		}
		v[n] = x
		n++
	}
	if n < minFields {
		return Times{}, fmt.Errorf("got %d fields, want at least %d", n, minFields)
	}
	return Times{v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7]}, nil
}

// busyPercent returns the non-idle share of time elapsed between prev and cur.
func busyPercent(prev, cur Times) float64 {
	dt := sub(cur.total(), prev.total())
	if dt == 0 {
		return 0
	}
	di := min(sub(cur.idle(), prev.idle()), dt)
	return float64(dt-di) / float64(dt) * 100
}

// sub clamps at zero so counters that go backwards (CPU hotplug, VM migration)
// read as no activity instead of wrapping to huge values.
func sub(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

// Collector samples /proc/stat and reports usage since its previous sample.
// A Collector is not safe for concurrent use.
type Collector struct {
	file      *procfs.File
	prev, cur Stat
	primed    bool
}

// New opens path (normally DefaultPath) for sampling.
func New(path string) (*Collector, error) {
	f, err := procfs.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cpu: %w", err)
	}
	return &Collector{file: f}, nil
}

// Close releases the underlying file.
func (c *Collector) Close() error {
	return c.file.Close()
}

// Sample reads the current counters and writes usage since the previous call
// into u, reusing u.PerCore. It returns false when there is no usable previous
// sample yet: on the first call, and when the number of cores changed.
func (c *Collector) Sample(u *Usage) (bool, error) {
	data, err := c.file.Read()
	if err != nil {
		return false, fmt.Errorf("cpu: read: %w", err)
	}
	if err := Parse(data, &c.cur); err != nil {
		return false, err
	}

	ok := c.primed && len(c.prev.Cores) == len(c.cur.Cores)
	if ok {
		u.Total = busyPercent(c.prev.Total, c.cur.Total)
		u.PerCore = u.PerCore[:0]
		for i := range c.cur.Cores {
			u.PerCore = append(u.PerCore, busyPercent(c.prev.Cores[i], c.cur.Cores[i]))
		}
	}

	// Swap rather than copy so both Core slices keep their capacity.
	c.prev, c.cur = c.cur, c.prev
	c.primed = true
	return ok, nil
}

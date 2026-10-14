// Package netdev reports per-interface network throughput from /proc/net/dev.
// It is not called "net" to avoid shadowing the standard library package.
package netdev

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/ofk0/piway/internal/procfs"
)

// DefaultPath is where Linux exposes per-interface counters.
const DefaultPath = "/proc/net/dev"

// numFields is the 8 receive and 8 transmit columns after the interface name.
const numFields = 16

// Counters are the cumulative /proc/net/dev columns piway uses.
type Counters struct {
	RxBytes, RxPackets, RxErrors, RxDropped uint64
	TxBytes, TxPackets, TxErrors, TxDropped uint64
}

// Interface is the activity of one network interface between two samples.
type Interface struct {
	Name            string  `json:"name"`
	RxBytesPerSec   float64 `json:"rx_bytes_per_sec"`
	TxBytesPerSec   float64 `json:"tx_bytes_per_sec"`
	RxPacketsPerSec float64 `json:"rx_packets_per_sec"`
	TxPacketsPerSec float64 `json:"tx_packets_per_sec"`
	// Errors and drops are cumulative: any non-zero growth is worth a look,
	// a per-second rate would mostly read 0.00.
	RxErrors  uint64 `json:"rx_errors"`
	TxErrors  uint64 `json:"tx_errors"`
	RxDropped uint64 `json:"rx_dropped"`
	TxDropped uint64 `json:"tx_dropped"`
}

// ParseLine decodes one interface line such as "  wlan0: 2107 223 0 0 ...".
// ok is false for the two header lines. name aliases line.
func ParseLine(line []byte) (name []byte, c Counters, ok bool, err error) {
	name, rest, found := bytes.Cut(line, []byte{':'})
	if !found {
		return nil, Counters{}, false, nil
	}
	name = bytes.TrimSpace(name)

	var v [numFields]uint64
	for i := range v {
		var f []byte
		f, rest = procfs.NextField(rest)
		if len(f) == 0 {
			return nil, Counters{}, false, fmt.Errorf("%s: got %d fields, want %d", name, i, numFields)
		}
		if v[i], err = procfs.ParseUint(f); err != nil {
			return nil, Counters{}, false, fmt.Errorf("%s: %w", name, err)
		}
	}
	// Receive: bytes packets errs drop fifo frame compressed multicast
	// Transmit: bytes packets errs drop fifo colls carrier compressed
	return name, Counters{
		RxBytes: v[0], RxPackets: v[1], RxErrors: v[2], RxDropped: v[3],
		TxBytes: v[8], TxPackets: v[9], TxErrors: v[10], TxDropped: v[11],
	}, true, nil
}

// DefaultSkip hides loopback and the per-container veth pairs Docker creates,
// which would otherwise double count container traffic.
func DefaultSkip(name string) bool {
	return name == "lo" || strings.HasPrefix(name, "veth")
}

type ifaceState struct {
	name    string
	tracked bool // decided once per name
	prev    Counters
	seen    uint64 // generation of the last sample that contained this interface
}

// Collector samples /proc/net/dev. A Collector is not safe for concurrent use.
type Collector struct {
	file *procfs.File
	skip func(name string) bool

	ifaces   map[string]*ifaceState
	gen      uint64
	prevTime time.Time
}

// New opens path (normally DefaultPath). skip filters out interfaces by name;
// nil reports every interface.
func New(path string, skip func(name string) bool) (*Collector, error) {
	f, err := procfs.Open(path)
	if err != nil {
		return nil, fmt.Errorf("netdev: %w", err)
	}
	return &Collector{file: f, skip: skip, ifaces: make(map[string]*ifaceState)}, nil
}

// Close releases the underlying file.
func (c *Collector) Close() error {
	return c.file.Close()
}

// Sample writes per-interface rates since the previous call into dst, reusing
// its backing array, and returns the result. Interfaces appear once they have
// been seen in two consecutive samples; in steady state Sample does not allocate.
func (c *Collector) Sample(now time.Time, dst []Interface) ([]Interface, error) {
	data, err := c.file.Read()
	if err != nil {
		return dst, fmt.Errorf("netdev: read: %w", err)
	}

	c.gen++
	elapsed := now.Sub(c.prevTime).Seconds()
	havePrev := !c.prevTime.IsZero() && elapsed > 0
	dst = dst[:0]

	for len(data) > 0 {
		var line []byte
		line, data = procfs.NextLine(data)
		name, cur, ok, err := ParseLine(line)
		if err != nil {
			return dst, fmt.Errorf("netdev: %w", err)
		}
		if !ok {
			continue
		}

		st, known := c.ifaces[string(name)]
		if !known {
			n := string(name)
			st = &ifaceState{name: n, tracked: c.skip == nil || !c.skip(n)}
			c.ifaces[n] = st
		}
		inPrev := st.seen == c.gen-1
		st.seen = c.gen
		if !st.tracked {
			continue
		}

		if havePrev && inPrev {
			p := st.prev
			dst = append(dst, Interface{
				Name:            st.name,
				RxBytesPerSec:   float64(sub(cur.RxBytes, p.RxBytes)) / elapsed,
				TxBytesPerSec:   float64(sub(cur.TxBytes, p.TxBytes)) / elapsed,
				RxPacketsPerSec: float64(sub(cur.RxPackets, p.RxPackets)) / elapsed,
				TxPacketsPerSec: float64(sub(cur.TxPackets, p.TxPackets)) / elapsed,
				RxErrors:        cur.RxErrors,
				TxErrors:        cur.TxErrors,
				RxDropped:       cur.RxDropped,
				TxDropped:       cur.TxDropped,
			})
		}
		st.prev = cur
	}

	// Forget interfaces that went away (container stopped, VPN down) so the map
	// does not grow forever and a reused name starts from a fresh baseline.
	for name, st := range c.ifaces {
		if st.seen != c.gen {
			delete(c.ifaces, name)
		}
	}
	c.prevTime = now
	return dst, nil
}

// sub clamps at zero: some drivers reset counters when an interface goes down and up.
func sub(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

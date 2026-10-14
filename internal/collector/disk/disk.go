// Package disk reports block device I/O rates from /proc/diskstats and
// filesystem usage via statfs(2).
package disk

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ofk0/piway/internal/procfs"
)

// Default locations.
const (
	DiskstatsPath = "/proc/diskstats"
	SysBlockPath  = "/sys/block"
)

// sectorSize is the unit of the sector columns in /proc/diskstats. It is fixed
// at 512 bytes by the kernel regardless of the device's real sector size.
const sectorSize = 512

// minFields is the 11 I/O columns every kernel since 2.6.25 reports after the
// device name; 4.18 appended discard columns and 5.5 flush columns.
const minFields = 11

// Counters are the cumulative /proc/diskstats columns piway uses.
type Counters struct {
	ReadsCompleted  uint64
	SectorsRead     uint64
	WritesCompleted uint64
	SectorsWritten  uint64
	IOTimeMs        uint64 // time the device had at least one request in flight
}

// Device is the I/O activity of one physical disk between two samples.
type Device struct {
	Name             string  `json:"name"`
	ReadBytesPerSec  float64 `json:"read_bytes_per_sec"`
	WriteBytesPerSec float64 `json:"write_bytes_per_sec"`
	ReadOpsPerSec    float64 `json:"read_ops_per_sec"`
	WriteOpsPerSec   float64 `json:"write_ops_per_sec"`
	BusyPercent      float64 `json:"busy_percent"`
	// WrittenBytes is cumulative since boot; on an SD card it is the number to watch for wear.
	WrittenBytes uint64 `json:"written_bytes"`
	// Totals are the raw cumulative counters at this sample, for consumers
	// that compute their own rates (Prometheus). Not part of the JSON API.
	Totals Counters `json:"-"`
}

// Filesystem is the usage of one mounted filesystem.
type Filesystem struct {
	Path        string  `json:"path"`
	TotalBytes  uint64  `json:"total_bytes"`
	UsedBytes   uint64  `json:"used_bytes"`
	AvailBytes  uint64  `json:"avail_bytes"`
	UsedPercent float64 `json:"used_percent"`
}

// Stats is one disk sample.
type Stats struct {
	Devices     []Device     `json:"devices"`
	Filesystems []Filesystem `json:"filesystems"`
}

// ParseLine decodes one /proc/diskstats line. name aliases line.
func ParseLine(line []byte) (name []byte, c Counters, err error) {
	var f []byte
	f, line = procfs.NextField(line) // major
	if len(f) == 0 {
		return nil, Counters{}, errors.New("empty line")
	}
	_, line = procfs.NextField(line) // minor
	name, line = procfs.NextField(line)

	var v [minFields]uint64
	for i := range v {
		f, line = procfs.NextField(line)
		if len(f) == 0 {
			return nil, Counters{}, fmt.Errorf("%s: got %d fields, want at least %d", name, i, minFields)
		}
		if v[i], err = procfs.ParseUint(f); err != nil {
			return nil, Counters{}, fmt.Errorf("%s: %w", name, err)
		}
	}
	// Column order: reads, reads merged, sectors read, ms reading,
	// writes, writes merged, sectors written, ms writing, in flight, ms doing I/O, weighted ms.
	return name, Counters{
		ReadsCompleted:  v[0],
		SectorsRead:     v[2],
		WritesCompleted: v[4],
		SectorsWritten:  v[6],
		IOTimeMs:        v[9],
	}, nil
}

// Config selects data sources.
type Config struct {
	DiskstatsPath string
	// SysBlockPath is used to tell physical disks from partitions and virtual
	// devices: only entries of /sys/block that have a "device" link are reported.
	SysBlockPath string
	// MountPoints are reported via statfs, in this order.
	MountPoints []string
}

// DefaultConfig reports the root filesystem and all physical disks.
func DefaultConfig() Config {
	return Config{DiskstatsPath: DiskstatsPath, SysBlockPath: SysBlockPath, MountPoints: []string{"/"}}
}

type deviceState struct {
	name    string
	tracked bool // physical disk; decided once per name
	prev    Counters
	seen    uint64 // generation of the last sample that contained this device
}

// Collector samples disk I/O and filesystem usage.
// A Collector is not safe for concurrent use.
type Collector struct {
	file        *procfs.File
	sysBlock    string
	mountPoints []string

	devices  map[string]*deviceState
	gen      uint64
	prevTime time.Time
}

// New opens cfg.DiskstatsPath for sampling.
func New(cfg Config) (*Collector, error) {
	f, err := procfs.Open(cfg.DiskstatsPath)
	if err != nil {
		return nil, fmt.Errorf("disk: %w", err)
	}
	return &Collector{
		file:        f,
		sysBlock:    cfg.SysBlockPath,
		mountPoints: cfg.MountPoints,
		devices:     make(map[string]*deviceState),
	}, nil
}

// Close releases the underlying file.
func (c *Collector) Close() error {
	return c.file.Close()
}

func (c *Collector) isPhysical(name string) bool {
	_, err := os.Stat(filepath.Join(c.sysBlock, name, "device"))
	return err == nil
}

// Sample writes current filesystem usage and I/O rates since the previous call
// into s, reusing its slices. now should come from time.Now(); its monotonic
// clock reading makes the elapsed time immune to wall-clock changes.
//
// Devices only appear once they have been seen in two consecutive samples,
// so the first call reports filesystems but no devices.
func (c *Collector) Sample(now time.Time, s *Stats) error {
	data, err := c.file.Read()
	if err != nil {
		return fmt.Errorf("disk: read: %w", err)
	}

	c.gen++
	elapsed := now.Sub(c.prevTime).Seconds()
	havePrev := !c.prevTime.IsZero() && elapsed > 0
	s.Devices = s.Devices[:0]

	for len(data) > 0 {
		var line []byte
		line, data = procfs.NextLine(data)
		if len(line) == 0 {
			continue
		}
		name, cur, err := ParseLine(line)
		if err != nil {
			return fmt.Errorf("disk: %w", err)
		}

		// Indexing a map with string(bytes) does not allocate; only new devices do.
		st, ok := c.devices[string(name)]
		if !ok {
			n := string(name)
			st = &deviceState{name: n, tracked: c.isPhysical(n)}
			c.devices[n] = st
		}
		inPrev := st.seen == c.gen-1
		st.seen = c.gen
		if !st.tracked {
			continue
		}

		if havePrev && inPrev {
			s.Devices = append(s.Devices, Device{
				Name:             st.name,
				ReadBytesPerSec:  float64(sub(cur.SectorsRead, st.prev.SectorsRead)*sectorSize) / elapsed,
				WriteBytesPerSec: float64(sub(cur.SectorsWritten, st.prev.SectorsWritten)*sectorSize) / elapsed,
				ReadOpsPerSec:    float64(sub(cur.ReadsCompleted, st.prev.ReadsCompleted)) / elapsed,
				WriteOpsPerSec:   float64(sub(cur.WritesCompleted, st.prev.WritesCompleted)) / elapsed,
				BusyPercent:      min(float64(sub(cur.IOTimeMs, st.prev.IOTimeMs))/(elapsed*1000)*100, 100),
				WrittenBytes:     cur.SectorsWritten * sectorSize,
				Totals:           cur,
			})
		}
		st.prev = cur
	}

	// Forget devices that disappeared (USB disk unplugged) so a later device
	// reusing the name starts from a fresh baseline.
	for name, st := range c.devices {
		if st.seen != c.gen {
			delete(c.devices, name)
		}
	}
	c.prevTime = now

	s.Filesystems = s.Filesystems[:0]
	for _, mp := range c.mountPoints {
		fs, err := statFS(mp)
		if err != nil {
			return fmt.Errorf("disk: %w", err)
		}
		s.Filesystems = append(s.Filesystems, fs)
	}
	return nil
}

// statFS reports usage the same way df does: Used is blocks not free, and
// UsedPercent is relative to what non-root users can use, because ext4
// reserves ~5% of blocks for root.
func statFS(path string) (Filesystem, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Filesystem{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	bs := uint64(st.Frsize)
	if bs == 0 {
		bs = uint64(st.Bsize)
	}
	fs := Filesystem{
		Path:       path,
		TotalBytes: st.Blocks * bs,
		UsedBytes:  (st.Blocks - st.Bfree) * bs,
		AvailBytes: st.Bavail * bs,
	}
	if denom := fs.UsedBytes + fs.AvailBytes; denom > 0 {
		fs.UsedPercent = float64(fs.UsedBytes) / float64(denom) * 100
	}
	return fs, nil
}

// sub clamps at zero so a counter reset reads as no activity instead of wrapping.
func sub(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

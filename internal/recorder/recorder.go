// Package recorder stores every agent snapshot in the time series store.
//
// Series names follow Prometheus conventions (snake_case, base units, a
// _total suffix for counters, labels in braces) so the range API (#18) and
// the /metrics endpoint (#19) can expose them unchanged.
package recorder

import (
	"fmt"
	"log/slog"
	"strconv"

	"github.com/ofk0/piway/internal/agent"
	"github.com/ofk0/piway/internal/tsdb"
)

// Appender is the part of *tsdb.Store the recorder needs.
type Appender interface {
	Append(t int64, points []tsdb.Point) error
}

// Recorder appends snapshots to a store. Record is called from the agent's
// collection loop and is not safe for concurrent use.
type Recorder struct {
	store   Appender
	log     *slog.Logger
	lastErr string
	buf     []tsdb.Point
}

func New(store Appender, log *slog.Logger) *Recorder {
	return &Recorder{store: store, log: log}
}

// Record appends the metrics in s at s.Time. Failures are logged when they
// start, change or stop, not on every round.
func (r *Recorder) Record(s *agent.Snapshot) {
	r.buf = Points(r.buf[:0], s)
	err := r.store.Append(s.Time.UnixMilli(), r.buf)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if msg != r.lastErr {
		if msg == "" {
			r.log.Info("recording metrics again")
		} else {
			r.log.Warn("cannot record metrics", "err", msg)
		}
		r.lastErr = msg
	}
}

// Points appends the series of s to dst. Missing collectors contribute
// nothing, so a gap in a series means the metric was unavailable.
func Points(dst []tsdb.Point, s *agent.Snapshot) []tsdb.Point {
	add := func(name string, v float64) { dst = append(dst, tsdb.Point{Series: name, Value: v}) }
	label := func(name, key, value string) string {
		return fmt.Sprintf("%s{%s=%s}", name, key, strconv.Quote(value))
	}
	flag := func(b bool) float64 {
		if b {
			return 1
		}
		return 0
	}

	if c := s.CPU; c != nil {
		add("cpu_usage_percent", c.Total)
		for i, v := range c.PerCore {
			add(label("cpu_core_usage_percent", "core", strconv.Itoa(i)), v)
		}
	}
	if m := s.Memory; m != nil {
		add("memory_total_bytes", float64(m.Total))
		add("memory_used_bytes", float64(m.Used))
		add("memory_available_bytes", float64(m.Available))
		add("memory_cached_bytes", float64(m.Cached))
		add("memory_buffers_bytes", float64(m.Buffers))
		add("swap_total_bytes", float64(m.SwapTotal))
		add("swap_used_bytes", float64(m.SwapUsed))
	}
	if th := s.Thermal; th != nil {
		if th.TempCelsius != nil {
			add("temperature_celsius", *th.TempCelsius)
		}
		if tr := th.Throttle; tr != nil {
			add("throttle_under_voltage", flag(tr.Now.UnderVoltage))
			add("throttle_freq_capped", flag(tr.Now.FreqCapped))
			add("throttle_throttled", flag(tr.Now.Throttled))
			add("throttle_soft_temp_limit", flag(tr.Now.SoftTempLimit))
		}
	}
	if d := s.Disk; d != nil {
		for _, dev := range d.Devices {
			add(label("disk_read_bytes_per_second", "device", dev.Name), dev.ReadBytesPerSec)
			add(label("disk_write_bytes_per_second", "device", dev.Name), dev.WriteBytesPerSec)
			add(label("disk_busy_percent", "device", dev.Name), dev.BusyPercent)
			add(label("disk_written_bytes_total", "device", dev.Name), float64(dev.WrittenBytes))
		}
		for _, fs := range d.Filesystems {
			add(label("filesystem_size_bytes", "path", fs.Path), float64(fs.TotalBytes))
			add(label("filesystem_used_bytes", "path", fs.Path), float64(fs.UsedBytes))
			add(label("filesystem_available_bytes", "path", fs.Path), float64(fs.AvailBytes))
		}
	}
	for _, n := range s.Network {
		add(label("network_receive_bytes_per_second", "interface", n.Name), n.RxBytesPerSec)
		add(label("network_transmit_bytes_per_second", "interface", n.Name), n.TxBytesPerSec)
		add(label("network_receive_errors_total", "interface", n.Name), float64(n.RxErrors+n.RxDropped))
		add(label("network_transmit_errors_total", "interface", n.Name), float64(n.TxErrors+n.TxDropped))
	}
	return dst
}

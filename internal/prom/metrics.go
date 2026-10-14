package prom

import (
	"bytes"
	"log/slog"
	"net/http"
	"runtime"
	"strconv"

	"github.com/ofk0/piway/internal/agent"
	"github.com/ofk0/piway/internal/collector/thermal"
	"github.com/ofk0/piway/internal/version"
)

// userHZ is the unit of the CPU times in /proc/stat. It is part of the
// kernel's user ABI and 100 on every architecture Linux runs a Pi on.
const userHZ = 100

const contentType = "text/plain; version=0.0.4; charset=utf-8"

// Handler serves GET /metrics from the newest snapshot. Rates are left to
// Prometheus: CPU, disk and network are exposed as cumulative counters, so
// rate() works at any scrape interval and no sample between scrapes is lost.
func Handler(latest func() *agent.Snapshot) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var m Writer
		Collect(&m, latest())
		// Render first, so a failure is a clean 500 rather than half a page.
		var buf bytes.Buffer
		if _, err := m.WriteTo(&buf); err != nil {
			slog.Error("render metrics", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(buf.Bytes())
	})
}

// Collect adds every metric of s to m. s may be nil before the first round;
// then only piway_build_info is reported.
func Collect(m *Writer, s *agent.Snapshot) {
	m.Add("piway_build_info", Gauge, "Always 1; the labels describe the running piway binary.", 1,
		"version", version.Version, "commit", version.Commit, "goversion", runtime.Version())
	if s == nil {
		return
	}
	m.Add("piway_snapshot_timestamp_seconds", Gauge, "When the newest snapshot was collected, in Unix seconds.",
		float64(s.Time.UnixNano())/1e9)

	if s.CPU != nil {
		cpuMetrics(m, s)
	}
	if mem := s.Memory; mem != nil {
		const p = "piway_memory_"
		m.Add(p+"total_bytes", Gauge, "Usable RAM (MemTotal).", float64(mem.Total))
		m.Add(p+"available_bytes", Gauge, "RAM available to new programs without swapping (MemAvailable).", float64(mem.Available))
		m.Add(p+"free_bytes", Gauge, "Unused RAM (MemFree).", float64(mem.Free))
		m.Add(p+"buffers_bytes", Gauge, "RAM used for block device buffers (Buffers).", float64(mem.Buffers))
		m.Add(p+"cached_bytes", Gauge, "RAM used for the page cache (Cached).", float64(mem.Cached))
		m.Add(p+"swap_total_bytes", Gauge, "Swap space (SwapTotal).", float64(mem.SwapTotal))
		m.Add(p+"swap_free_bytes", Gauge, "Unused swap space (SwapFree).", float64(mem.SwapFree))
	}
	if t := s.Thermal; t != nil {
		if t.TempCelsius != nil {
			m.Add("piway_cpu_temperature_celsius", Gauge, "SoC temperature.", *t.TempCelsius)
		}
		if t.Throttle != nil {
			throttleMetrics(m, t.Throttle)
		}
	}
	if s.Disk != nil {
		diskMetrics(m, s)
	}
	for _, n := range s.Network {
		const p = "piway_network_"
		c, l := n.Totals, []string{"interface", n.Name}
		m.Add(p+"receive_bytes_total", Counter, "Bytes received.", float64(c.RxBytes), l...)
		m.Add(p+"transmit_bytes_total", Counter, "Bytes sent.", float64(c.TxBytes), l...)
		m.Add(p+"receive_packets_total", Counter, "Packets received.", float64(c.RxPackets), l...)
		m.Add(p+"transmit_packets_total", Counter, "Packets sent.", float64(c.TxPackets), l...)
		m.Add(p+"receive_errors_total", Counter, "Receive errors.", float64(c.RxErrors), l...)
		m.Add(p+"transmit_errors_total", Counter, "Transmit errors.", float64(c.TxErrors), l...)
		m.Add(p+"receive_drop_total", Counter, "Received packets dropped.", float64(c.RxDropped), l...)
		m.Add(p+"transmit_drop_total", Counter, "Packets dropped before sending.", float64(c.TxDropped), l...)
	}
}

func cpuMetrics(m *Writer, s *agent.Snapshot) {
	const name, help = "piway_cpu_seconds_total", "Time each core spent in each mode."
	for i, t := range s.CPU.Cores {
		core := strconv.Itoa(i)
		for _, x := range []struct {
			mode  string
			ticks uint64
		}{
			{"user", t.User}, {"nice", t.Nice}, {"system", t.System}, {"idle", t.Idle},
			{"iowait", t.IOWait}, {"irq", t.IRQ}, {"softirq", t.SoftIRQ}, {"steal", t.Steal},
		} {
			m.Add(name, Counter, help, float64(x.ticks)/userHZ, "cpu", core, "mode", x.mode)
		}
	}
}

func throttleMetrics(m *Writer, t *thermal.Throttle) {
	conditions := func(s thermal.State) []struct {
		name string
		on   bool
	} {
		return []struct {
			name string
			on   bool
		}{
			{"under_voltage", s.UnderVoltage},
			{"freq_capped", s.FreqCapped},
			{"throttled", s.Throttled},
			{"soft_temp_limit", s.SoftTempLimit},
		}
	}
	for _, c := range conditions(t.Now) {
		m.Add("piway_throttle_active", Gauge, "1 while the firmware reports the condition (vcgencmd get_throttled).",
			bool01(c.on), "condition", c.name)
	}
	for _, c := range conditions(t.SinceBoot) {
		m.Add("piway_throttle_occurred", Gauge, "1 if the condition has happened since boot.",
			bool01(c.on), "condition", c.name)
	}
}

func diskMetrics(m *Writer, s *agent.Snapshot) {
	const sectorSize = 512 // /proc/diskstats counts 512-byte sectors on every device
	for _, d := range s.Disk.Devices {
		const p = "piway_disk_"
		c, l := d.Totals, []string{"device", d.Name}
		m.Add(p+"read_bytes_total", Counter, "Bytes read.", float64(c.SectorsRead*sectorSize), l...)
		m.Add(p+"written_bytes_total", Counter, "Bytes written. On an SD card, the number to watch for wear.",
			float64(c.SectorsWritten*sectorSize), l...)
		m.Add(p+"reads_completed_total", Counter, "Read requests completed.", float64(c.ReadsCompleted), l...)
		m.Add(p+"writes_completed_total", Counter, "Write requests completed.", float64(c.WritesCompleted), l...)
		m.Add(p+"io_time_seconds_total", Counter, "Time the device had at least one request in flight.",
			float64(c.IOTimeMs)/1000, l...)
	}
	for _, f := range s.Disk.Filesystems {
		const p = "piway_filesystem_"
		l := []string{"mountpoint", f.Path}
		m.Add(p+"size_bytes", Gauge, "Filesystem size.", float64(f.TotalBytes), l...)
		m.Add(p+"used_bytes", Gauge, "Space in use.", float64(f.UsedBytes), l...)
		m.Add(p+"avail_bytes", Gauge, "Space available to unprivileged users.", float64(f.AvailBytes), l...)
	}
}

func bool01(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

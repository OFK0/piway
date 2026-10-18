package prom

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ofk0/piway/internal/agent"
	"github.com/ofk0/piway/internal/collector/cpu"
	"github.com/ofk0/piway/internal/collector/disk"
	"github.com/ofk0/piway/internal/collector/mem"
	"github.com/ofk0/piway/internal/collector/netdev"
	"github.com/ofk0/piway/internal/collector/thermal"
)

// fullSnapshot has every field set, with round numbers that make the unit
// conversions easy to check.
func fullSnapshot() *agent.Snapshot {
	temp := 48.5
	throttle := thermal.Decode(1<<0 | 1<<16 | 1<<18) // under-voltage now; since boot also throttled
	return &agent.Snapshot{
		Seq:  7,
		Time: time.Unix(1_700_000_000, 500_000_000),
		CPU: &cpu.Usage{
			Total: 12.5, PerCore: []float64{20, 5},
			Cores: []cpu.Times{{User: 12_345, Idle: 100_000}, {System: 50, IOWait: 7}},
		},
		Memory: &mem.Info{
			Total: 4 << 30, Free: 1 << 30, Available: 3 << 30, Used: 1 << 30,
			Buffers: 64 << 20, Cached: 512 << 20, SwapTotal: 100 << 20, SwapFree: 100 << 20,
		},
		Thermal: &thermal.Reading{TempCelsius: &temp, Throttle: &throttle},
		Disk: &disk.Stats{
			Devices: []disk.Device{{
				Name:   "mmcblk0",
				Totals: disk.Counters{ReadsCompleted: 10, SectorsRead: 2, WritesCompleted: 20, SectorsWritten: 4, IOTimeMs: 1500},
			}},
			Filesystems: []disk.Filesystem{{Path: "/", TotalBytes: 1000, UsedBytes: 400, AvailBytes: 550}},
		},
		Network: []netdev.Interface{{
			Name:   "eth0",
			Totals: netdev.Counters{RxBytes: 1000, RxPackets: 10, RxErrors: 1, RxDropped: 2, TxBytes: 500, TxPackets: 5},
		}},
	}
}

func exposition(t *testing.T, s *agent.Snapshot) string {
	t.Helper()
	var w Writer
	Collect(&w, s)
	return render(t, &w)
}

// handlerExposition is what /metrics serves: the snapshot and the process.
func handlerExposition(t *testing.T) string {
	t.Helper()
	var w Writer
	Collect(&w, fullSnapshot())
	Process(&w)
	return render(t, &w)
}

func TestCollectConvertsUnits(t *testing.T) {
	out := exposition(t, fullSnapshot())
	for _, line := range []string{
		`piway_snapshot_timestamp_seconds 1.7000000005e+09`,
		`piway_cpu_seconds_total{cpu="0",mode="user"} 123.45`, // ticks / USER_HZ
		`piway_cpu_seconds_total{cpu="0",mode="idle"} 1000`,
		`piway_cpu_seconds_total{cpu="1",mode="iowait"} 0.07`,
		`piway_memory_total_bytes 4.294967296e+09`,
		`piway_memory_available_bytes 3.221225472e+09`,
		`piway_cpu_temperature_celsius 48.5`,
		`piway_throttle_active{condition="under_voltage"} 1`,
		`piway_throttle_active{condition="throttled"} 0`,
		`piway_throttle_occurred{condition="throttled"} 1`,
		`piway_disk_read_bytes_total{device="mmcblk0"} 1024`, // sectors × 512
		`piway_disk_written_bytes_total{device="mmcblk0"} 2048`,
		`piway_disk_io_time_seconds_total{device="mmcblk0"} 1.5`,
		`piway_filesystem_avail_bytes{mountpoint="/"} 550`,
		`piway_network_receive_bytes_total{interface="eth0"} 1000`,
		`piway_network_receive_drop_total{interface="eth0"} 2`,
		`piway_network_transmit_packets_total{interface="eth0"} 5`,
	} {
		if !strings.Contains(out, "\n"+line+"\n") {
			t.Errorf("missing line %q", line)
		}
	}
	if n := strings.Count(out, "piway_cpu_seconds_total{"); n != 16 {
		t.Errorf("%d cpu_seconds series, want 2 cores × 8 modes", n)
	}
}

func TestCollectBeforeFirstRound(t *testing.T) {
	out := exposition(t, nil)
	if !strings.HasPrefix(out, "# HELP piway_build_info ") || strings.Count(out, "\n") != 3 {
		t.Errorf("want only piway_build_info, got\n%s", out)
	}
}

func TestCollectSkipsMissingCollectors(t *testing.T) {
	// A round where every collector failed or had no baseline yet.
	out := exposition(t, &agent.Snapshot{Seq: 1, Time: time.Unix(1, 0)})
	for _, prefix := range []string{"piway_cpu_", "piway_memory_", "piway_throttle_", "piway_disk_", "piway_network_"} {
		if strings.Contains(out, prefix) {
			t.Errorf("reported %s* without data:\n%s", prefix, out)
		}
	}
}

var (
	nameRE  = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	labelRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// TestExpositionRules checks the rules promtool enforces, so the test suite
// catches a bad name even where promtool is not installed.
func TestExpositionRules(t *testing.T) {
	out := handlerExposition(t)
	types := map[string]string{}
	series := map[string]bool{}
	current := ""
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if rest, ok := strings.CutPrefix(line, "# TYPE "); ok {
			name, typ, _ := strings.Cut(rest, " ")
			if _, dup := types[name]; dup {
				t.Errorf("family %s appears twice; families must be contiguous", name)
			}
			types[name], current = typ, name
			if !nameRE.MatchString(name) {
				t.Errorf("bad metric name %q", name)
			}
			if (typ == Counter) != strings.HasSuffix(name, "_total") {
				t.Errorf("%s is a %s: counters, and only counters, end in _total", name, typ)
			}
			continue
		}
		if strings.HasPrefix(line, "# HELP ") {
			continue
		}
		key, _, _ := strings.Cut(line, " ")
		name, labels, _ := strings.Cut(key, "{")
		if name != current {
			t.Errorf("sample %q outside its family (current %q)", line, current)
		}
		for _, pair := range strings.Split(strings.TrimSuffix(labels, "}"), ",") {
			if pair == "" {
				continue
			}
			if l, _, _ := strings.Cut(pair, "="); !labelRE.MatchString(l) {
				t.Errorf("bad label name %q in %q", l, line)
			}
		}
		if series[key] {
			t.Errorf("duplicate series %s", key)
		}
		series[key] = true
	}
}

// TestPromtool runs the real linter when it is installed.
func TestPromtool(t *testing.T) {
	path, err := exec.LookPath("promtool")
	if err != nil {
		t.Skip("promtool not on PATH")
	}
	cmd := exec.Command(path, "check", "metrics")
	cmd.Stdin = strings.NewReader(handlerExposition(t))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("promtool check metrics: %v\n%s", err, out.String())
	}
}

func TestHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(fullSnapshot).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != contentType {
		t.Fatalf("status %d, Content-Type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "piway_build_info{") {
		t.Errorf("body:\n%s", rec.Body)
	}
}

func TestProcess(t *testing.T) {
	var w Writer
	Process(&w)
	out := render(t, &w)
	for _, name := range []string{
		"piway_process_goroutines", "piway_process_heap_alloc_bytes", "piway_process_go_memory_bytes",
		"piway_process_gc_cycles_total", "piway_process_gc_pause_seconds_total", "piway_process_cpu_seconds_total",
	} {
		if !strings.Contains(out, "\n"+name+" ") {
			t.Errorf("%s missing:\n%s", name, out)
		}
	}
	if runtime.GOOS == "linux" {
		for _, name := range []string{"piway_process_open_fds", "piway_process_max_fds", "piway_process_resident_memory_bytes"} {
			if !strings.Contains(out, "\n"+name+" ") {
				t.Errorf("%s missing on Linux", name)
			}
		}
	}
}

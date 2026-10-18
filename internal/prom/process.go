package prom

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// Process adds piway's own process metrics to m: what a leak shows in. A
// metric the system cannot report is left out, as the host metrics are.
func Process(m *Writer) {
	const p = "piway_process_"
	m.Add(p+"goroutines", Gauge, "Goroutines that currently exist.", float64(runtime.NumGoroutine()))

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	m.Add(p+"heap_alloc_bytes", Gauge, "Heap bytes in use by live and not yet collected objects.", float64(ms.HeapAlloc))
	m.Add(p+"go_memory_bytes", Gauge, "Address space the Go runtime has obtained from the system; most of it may never be touched.", float64(ms.Sys))
	m.Add(p+"gc_cycles_total", Counter, "Completed garbage collection cycles.", float64(ms.NumGC))
	m.Add(p+"gc_pause_seconds_total", Counter, "Time the program was stopped by garbage collection.", float64(ms.PauseTotalNs)/1e9)

	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) == nil {
		cpu := float64(ru.Utime.Sec) + float64(ru.Utime.Usec)/1e6 + float64(ru.Stime.Sec) + float64(ru.Stime.Usec)/1e6
		m.Add(p+"cpu_seconds_total", Counter, "CPU time piway has used, user and system.", cpu)
	}
	if b, err := os.ReadFile("/proc/self/statm"); err == nil {
		// Second field: resident pages.
		if f := strings.Fields(string(b)); len(f) > 1 {
			if pages, err := strconv.ParseUint(f[1], 10, 64); err == nil {
				m.Add(p+"resident_memory_bytes", Gauge, "Memory piway holds in RAM (RSS).", float64(pages*uint64(os.Getpagesize())))
			}
		}
	}
	if dir, err := os.ReadDir("/proc/self/fd"); err == nil {
		m.Add(p+"open_fds", Gauge, "File descriptors piway has open.", float64(len(dir)))
	}
	var lim syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim) == nil {
		m.Add(p+"max_fds", Gauge, "The most file descriptors piway may open (the soft limit).", float64(lim.Cur))
	}
}

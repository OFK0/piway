// Command loadlab is an example app that puts load on the machine on
// demand: CPU, memory and disk. It is for trying piway's limits, health
// checks and alerts, and a fixture of its end-to-end tests.
//
//	POST /cpu?seconds=10&workers=2   busy loops on that many cores
//	POST /mem?mb=100&seconds=30      allocates and touches memory, then frees it
//	POST /disk?mb=50                 writes a file to the data volume
//	DELETE /disk                     deletes those files
//	GET  /ws                         resource use every second (WebSocket)
//	GET  /api/info, /healthz         the example apps' common contract
package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

//go:embed index.html
var static embed.FS

var version = "dev" // set with -ldflags at build time

// lab holds the load currently running.
type lab struct {
	dataDir string
	busy    atomic.Int64 // CPU workers running
	mu      sync.Mutex
	held    [][]byte // memory being held
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	l := &lab{dataDir: env("DATA_DIR", "/data")}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /api/info", l.info)
	mux.HandleFunc("POST /cpu", l.cpu)
	mux.HandleFunc("POST /mem", l.mem)
	mux.HandleFunc("POST /disk", l.disk)
	mux.HandleFunc("DELETE /disk", l.clearDisk)
	mux.HandleFunc("GET /api/stats", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, l.stats()) })
	mux.HandleFunc("GET /ws", l.stream)

	srv := &http.Server{Addr: ":" + env("PORT", "8080"), Handler: logRequests(log, mux), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		log.Info("shutting down")
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()
	log.Info("listening", "addr", srv.Addr, "version", version, "data", l.dataDir)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}

// info proves what the deploy gave the app: its variables and that the
// secret arrived (its length and a hash prefix, never the value).
func (l *lab) info(w http.ResponseWriter, _ *http.Request) {
	secret := os.Getenv("LOADLAB_TOKEN")
	sum := sha256.Sum256([]byte(secret))
	writeJSON(w, map[string]any{
		"app":      "loadlab",
		"language": "Go",
		"runtime":  runtime.Version(),
		"version":  version,
		"env":      map[string]string{"GREETING": os.Getenv("GREETING")},
		"secret":   map[string]any{"name": "LOADLAB_TOKEN", "set": secret != "", "length": len(secret), "sha256_prefix": hex.EncodeToString(sum[:4])},
	})
}

func (l *lab) cpu(w http.ResponseWriter, r *http.Request) {
	seconds := intParam(r, "seconds", 10, 1, 600)
	workers := intParam(r, "workers", 1, 1, runtime.NumCPU()*4)
	for range workers {
		go func() {
			l.busy.Add(1)
			defer l.busy.Add(-1)
			end := time.Now().Add(time.Duration(seconds) * time.Second)
			x := 0
			for time.Now().Before(end) {
				for i := range 100_000 {
					x += i * i
				}
			}
			_ = x
		}()
	}
	writeJSON(w, map[string]any{"workers": workers, "seconds": seconds})
}

// mem allocates mb megabytes, touching every page so it is really used,
// holds it for seconds, then lets it go. Past run.memory, the kernel kills
// the app (an OOM kill), which is one of the things to try.
func (l *lab) mem(w http.ResponseWriter, r *http.Request) {
	mb := intParam(r, "mb", 50, 1, 4096)
	seconds := intParam(r, "seconds", 30, 1, 600)
	buf := make([]byte, mb<<20)
	for i := 0; i < len(buf); i += 4096 {
		buf[i] = 1
	}
	l.mu.Lock()
	l.held = append(l.held, buf)
	l.mu.Unlock()
	time.AfterFunc(time.Duration(seconds)*time.Second, func() {
		l.mu.Lock()
		for i, b := range l.held {
			if &b[0] == &buf[0] {
				l.held = append(l.held[:i], l.held[i+1:]...)
				break
			}
		}
		l.mu.Unlock()
		runtime.GC()
	})
	writeJSON(w, map[string]any{"mb": mb, "seconds": seconds})
}

func (l *lab) disk(w http.ResponseWriter, r *http.Request) {
	mb := intParam(r, "mb", 10, 1, 4096)
	name := filepath.Join(l.dataDir, fmt.Sprintf("fill-%d.bin", time.Now().UnixNano()))
	f, err := os.Create(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	block := make([]byte, 1<<20)
	for i := range block {
		block[i] = byte(i)
	}
	for range mb {
		if _, err := f.Write(block); err != nil {
			f.Close()
			os.Remove(name)
			http.Error(w, err.Error(), http.StatusInsufficientStorage)
			return
		}
	}
	if err := f.Close(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"file": filepath.Base(name), "mb": mb})
}

func (l *lab) clearDisk(w http.ResponseWriter, _ *http.Request) {
	files, _ := filepath.Glob(filepath.Join(l.dataDir, "fill-*.bin"))
	for _, f := range files {
		os.Remove(f)
	}
	writeJSON(w, map[string]any{"removed": len(files)})
}

// stats is what /ws sends every second.
type stats struct {
	Time        time.Time `json:"time"`
	CPUWorkers  int64     `json:"cpu_workers"`
	HeldMB      int       `json:"held_mb"`
	RSSBytes    int64     `json:"rss_bytes"`             // this process
	CgroupBytes int64     `json:"cgroup_bytes"`          // the container
	LimitBytes  int64     `json:"limit_bytes,omitempty"` // run.memory, if enforced
	DiskBytes   int64     `json:"disk_bytes"`            // files written to the volume
	Goroutines  int       `json:"goroutines"`
}

func (l *lab) stats() stats {
	l.mu.Lock()
	held := 0
	for _, b := range l.held {
		held += len(b) >> 20
	}
	l.mu.Unlock()
	s := stats{Time: time.Now().UTC(), CPUWorkers: l.busy.Load(), HeldMB: held, Goroutines: runtime.NumGoroutine()}
	s.RSSBytes = statusKB("/proc/self/status", "VmRSS:") << 10
	s.CgroupBytes = readInt("/sys/fs/cgroup/memory.current")
	s.LimitBytes = readInt("/sys/fs/cgroup/memory.max") // "max" reads as 0: no limit
	files, _ := filepath.Glob(filepath.Join(l.dataDir, "fill-*.bin"))
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil {
			s.DiskBytes += fi.Size()
		}
	}
	return s
}

func (l *lab) stream(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := c.CloseRead(r.Context())
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		if err := wsjson.Write(ctx, c, l.stats()); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func intParam(r *http.Request, name string, def, lo, hi int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return def
	}
	return max(lo, min(hi, v))
}

func statusKB(path, field string) int64 {
	b, _ := os.ReadFile(path)
	for line := range strings.SplitSeq(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, field); ok {
			n, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "kB")), 10, 64)
			return n
		}
	}
	return 0
}

func readInt(path string) int64 {
	b, _ := os.ReadFile(path)
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path != "/healthz" {
			log.Info("request", "method", r.Method, "path", r.URL.Path, "took", time.Since(start).Round(time.Millisecond).String())
		}
	})
}

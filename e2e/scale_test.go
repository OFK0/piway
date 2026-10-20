//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const scaleApps = 20

// scaleManifest is loadlab with a smaller footprint, under another name.
const scaleManifest = `piway: 1
name: %s

build:
  dockerfile: Dockerfile

run:
  port: 8080
  memory: 64MiB
  cpus: 0.5
  health:
    path: /healthz
`

// processMetrics reads piway's own /metrics (piway_process_*).
func processMetrics(t *testing.T, p *piway) map[string]float64 {
	t.Helper()
	code, body := get(t, p.url("/metrics"))
	if code != 200 {
		t.Fatalf("/metrics: %d", code)
	}
	out := map[string]float64{}
	for _, line := range strings.Split(body, "\n") {
		name, value, ok := strings.Cut(line, " ")
		if ok && strings.HasPrefix(name, "piway_process_") {
			out[strings.TrimPrefix(name, "piway_process_")], _ = strconv.ParseFloat(value, 64)
		}
	}
	return out
}

func describe(m map[string]float64) string {
	return fmt.Sprintf("%.0f MB RSS, %.0f goroutines, %.0f descriptors, %.1f s CPU in all",
		m["resident_memory_bytes"]/1e6, m["goroutines"], m["open_fds"], m["cpu_seconds_total"])
}

// TestScale deploys 20 apps at once and checks that piway stays small and
// quick with all of them: one push for each, built one at a time, then
// 20 containers behind 20 stable addresses. It does not run with TestE2E:
//
//	make e2e E2E_RUN='TestScale$'
//
// It logs what it measured; docs/PERFORMANCE.md keeps the Pi 4 figures.
func TestScale(t *testing.T) {
	requireDocker(t)
	var names []string
	for i := 1; i <= scaleApps; i++ {
		names = append(names, fmt.Sprintf("scale%02d", i))
	}
	for _, n := range names {
		out, _ := exec.Command("docker", "ps", "-aq", "--filter", "label=piway.app="+n).Output()
		if strings.TrimSpace(string(out)) != "" {
			t.Fatalf("a container of app %s exists and the run would remove it", n)
		}
	}
	p := newPiway(t)
	t.Cleanup(p.cleanup)
	t.Cleanup(func() {
		p.stop(false)
		if t.Failed() {
			t.Logf("piway's log:\n%s", tail(p.log.String(), 8000))
		}
	})
	write(t, filepath.Join(p.cfgDir(), "apps.yaml"), "deploy:\n", 0o600)
	src := filepath.Join("..", "examples", "loadlab")
	for _, n := range names {
		dir := filepath.Join(t.TempDir(), n)
		if out, err := exec.Command("cp", "-r", src, dir).CombinedOutput(); err != nil {
			t.Fatalf("copy loadlab: %v: %s", err, out)
		}
		write(t, filepath.Join(dir, "piway.yaml"), fmt.Sprintf(scaleManifest, n), 0o644)
		p.repo("e2e/"+n, dir)
		p.apps = append(p.apps, n)
	}
	p.start()
	idle := processMetrics(t, p)
	t.Logf("idle, no apps: %s", describe(idle))

	// A push for every app within moments of each other.
	began := time.Now()
	for _, n := range names {
		p.push("e2e/" + n)
	}
	t.Logf("%d pushes accepted in %v", len(names), time.Since(began).Round(time.Millisecond))
	for _, n := range names {
		p.waitBuild(n, 20*time.Minute, func(b build) bool { return b.Repo == "e2e/"+n && b.Kind == "" })
	}
	t.Logf("%d apps built and released in %v", len(names), time.Since(began).Round(time.Second))

	for _, n := range names {
		base := p.appURL(n)
		eventually(t, n+" /healthz", time.Minute, func() string {
			if code, _, err := tryGet(base + "/healthz"); err != nil || code != 200 {
				return fmt.Sprintf("status %d %v", code, err)
			}
			return ""
		})
	}
	var list struct {
		Apps []struct {
			Name string `json:"name"`
		} `json:"apps"`
	}
	if _, err := p.api("GET", "/api/v1/apps", nil, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Apps) != scaleApps {
		t.Errorf("/api/v1/apps lists %d apps, want %d", len(list.Apps), scaleApps)
	}

	// The apps list asks Docker about every container: how long does it take?
	var took []time.Duration
	for range 50 {
		start := time.Now()
		if code, err := p.api("GET", "/api/v1/apps", nil, nil); err != nil || code != 200 {
			t.Fatalf("/api/v1/apps: %d %v", code, err)
		}
		took = append(took, time.Since(start))
	}
	slices.Sort(took)
	t.Logf("GET /api/v1/apps with %d apps: p50 %v, p99 %v", scaleApps, took[len(took)/2].Round(time.Millisecond), took[len(took)-2].Round(time.Millisecond))

	loaded := processMetrics(t, p)
	t.Logf("with %d apps: %s", scaleApps, describe(loaded))
	time.Sleep(60 * time.Second)
	after := processMetrics(t, p)
	rate := (after["cpu_seconds_total"] - loaded["cpu_seconds_total"]) / 60
	t.Logf("a minute later: %s; %.3f CPU-seconds per second at rest", describe(after), rate)

	if after["resident_memory_bytes"] > 150e6 {
		t.Errorf("piway holds %.0f MB with %d apps", after["resident_memory_bytes"]/1e6, scaleApps)
	}
	if after["goroutines"] > 300 {
		t.Errorf("%.0f goroutines with %d apps", after["goroutines"], scaleApps)
	}
	if rate > 0.25 {
		t.Errorf("%.3f CPU-seconds per second at rest", rate)
	}
	if out, err := exec.Command("df", "--output=avail", "-B1M", os.TempDir()).Output(); err == nil {
		t.Logf("free space in %s: %s", os.TempDir(), strings.Fields(string(out))[len(strings.Fields(string(out)))-1]+" MB")
	}
}

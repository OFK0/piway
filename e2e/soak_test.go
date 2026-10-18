//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// soakSample is one reading of piway's own process.
type soakSample struct {
	at                   time.Duration
	rss, goroutines, fds float64
}

// median of f over the samples.
func median(s []soakSample, f func(soakSample) float64) float64 {
	v := make([]float64, len(s))
	for i := range s {
		v[i] = f(s[i])
	}
	slices.Sort(v)
	return v[len(v)/2]
}

// TestSoak keeps piway busy for E2E_SOAK (such as 24h) and checks that its
// memory, goroutines and descriptors do not grow. Without E2E_SOAK it is
// skipped. Run it by hand, and give go test a timeout longer than the soak:
//
//	cd e2e && E2E_FORCE=1 E2E_SOAK=24h go test -tags e2e -timeout 30h -run 'TestSoak$' -v .
//
// A piway runs three of the example apps (loadlab, thumbnailer, docs-site)
// under constant load: 20 dashboard clients on the WebSocket stream, a
// history query a second, a steady trickle of requests to the apps, and
// a redeploy of each app every half hour. It writes a reading every five
// minutes to $E2E_SOAK_OUT (default piway-soak.csv in the temp directory).
// The first hour is warm-up. Of what is left, the median of the last quarter
// must not exceed the median of the first quarter by more than 25% of RSS
// (and 10 MB), 20 goroutines or 20 descriptors.
func TestSoak(t *testing.T) {
	spec := os.Getenv("E2E_SOAK")
	if spec == "" {
		t.Skip("set E2E_SOAK=24h to run the soak")
	}
	total, err := time.ParseDuration(spec)
	if err != nil {
		t.Fatalf("E2E_SOAK: %v", err)
	}
	every := 5 * time.Minute
	warmup := time.Hour
	if total < 3*time.Hour { // a trial run
		every, warmup = 30*time.Second, total/4
	}
	requireDocker(t)

	apps := []string{"loadlab", "thumbnailer", "docs-site"}
	p := newPiway(t)
	t.Cleanup(p.cleanup)
	t.Cleanup(func() {
		p.stop(false)
		if t.Failed() {
			t.Logf("piway's log:\n%s", tail(p.log.String(), 8000))
		}
	})
	write(t, filepath.Join(p.cfgDir(), "apps.yaml"), "deploy:\n", 0o600)
	for _, ex := range examples {
		if !slices.Contains(apps, ex.name) {
			continue
		}
		p.repo("e2e/"+ex.name, filepath.Join("..", "examples", ex.name))
		p.apps = append(p.apps, ex.name)
		if ex.secret != "" {
			write(t, filepath.Join(p.cfgDir(), "secrets", ex.name+".env"), ex.secret+"="+secretValue(ex.name)+"\n", 0o600)
		}
	}
	p.start()
	for _, a := range apps {
		deployExample(t, p, a, a != "docs-site")
	}

	out := os.Getenv("E2E_SOAK_OUT")
	if out == "" {
		out = filepath.Join(os.TempDir(), "piway-soak.csv")
	}
	csv, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer csv.Close()
	fmt.Fprintln(csv, "elapsed_s,rss_bytes,goroutines,open_fds,heap_alloc_bytes,gc_cycles,cpu_seconds,apps_healthy")
	t.Logf("soak of %v, a reading every %v into %s", total, every, out)

	// Load, until the soak ends. The load tools run in hour-long pieces.
	stop := make(chan struct{})
	defer close(stop)
	piece := func(args ...string) {
		for {
			select {
			case <-stop:
				return
			default:
			}
			cmd := exec.Command("go", append([]string{"run"}, args...)...)
			cmd.Dir = ".."
			done := make(chan struct{})
			go func() { cmd.Run(); close(done) }()
			select {
			case <-stop:
				cmd.Process.Kill()
				return
			case <-done:
			}
		}
	}
	go piece("./cmd/wsload", "-url", "ws://"+p.addr+"/api/v1/stream", "-clients", "20", "-duration", "1h")
	go func() { // a history query a second, as a dashboard open on the charts page would send
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Second):
			}
			tryGet(p.url("/api/v1/series?name=cpu_usage_percent&from=-1h"))
		}
	}()
	go func() {
		bases := map[string]string{}
		for _, a := range apps {
			bases[a] = p.appURL(a)
		}
		for {
			select {
			case <-stop:
				return
			case <-time.After(250 * time.Millisecond):
			}
			for _, a := range apps {
				tryGet(bases[a] + "/healthz")
			}
		}
	}()

	began := time.Now()
	nextDeploy := began.Add(30 * time.Minute)
	var samples []soakSample
	for time.Since(began) < total {
		time.Sleep(every)
		if time.Now().After(nextDeploy) {
			for _, a := range apps {
				redeploy(t, p, a)
			}
			nextDeploy = time.Now().Add(30 * time.Minute)
		}
		m := processMetrics(t, p)
		healthy := 0
		for _, a := range apps {
			path := "/healthz"
			if a == "docs-site" {
				path = "/"
			}
			if code, _, err := tryGet(p.appURL(a) + path); err == nil && code == 200 {
				healthy++
			} else {
				t.Errorf("after %v: %s does not answer (%d %v)", time.Since(began).Round(time.Second), a, code, err)
			}
		}
		at := time.Since(began)
		samples = append(samples, soakSample{at, m["resident_memory_bytes"], m["goroutines"], m["open_fds"]})
		fmt.Fprintf(csv, "%.0f,%.0f,%.0f,%.0f,%.0f,%.0f,%.1f,%d\n", at.Seconds(), m["resident_memory_bytes"], m["goroutines"],
			m["open_fds"], m["heap_alloc_bytes"], m["gc_cycles_total"], m["cpu_seconds_total"], healthy)
		csv.Sync()
		t.Logf("%v: %s", at.Round(time.Minute), describe(m))
	}

	var settled []soakSample
	for _, s := range samples {
		if s.at >= warmup {
			settled = append(settled, s)
		}
	}
	if len(settled) < 8 {
		t.Fatalf("only %d readings after the warm-up: too few to judge", len(settled))
	}
	q := len(settled) / 4
	first, last := settled[:q], settled[len(settled)-q:]
	for _, c := range []struct {
		name       string
		f          func(soakSample) float64
		pct, floor float64
	}{
		{"RSS (bytes)", func(s soakSample) float64 { return s.rss }, 0.25, 10e6},
		{"goroutines", func(s soakSample) float64 { return s.goroutines }, 0, 20},
		{"descriptors", func(s soakSample) float64 { return s.fds }, 0, 20},
	} {
		a, b := median(first, c.f), median(last, c.f)
		t.Logf("%s: %.0f in the first quarter, %.0f in the last", c.name, a, b)
		if b > a*(1+c.pct) && b-a > c.floor || c.pct == 0 && b-a > c.floor {
			t.Errorf("%s grew from %.0f to %.0f", c.name, a, b)
		}
	}
}

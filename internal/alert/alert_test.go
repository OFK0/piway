package alert

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ofk0/piway/internal/tsdb"
)

var t0 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

type sink struct{ events []Event }

func (r *sink) on(e Event) { r.events = append(r.events, e) }

func (r *sink) states() string {
	var s []string
	for _, e := range r.events {
		s = append(s, string(e.State))
	}
	return strings.Join(s, ",")
}

func hot() Rule {
	return Rule{Name: "CPU is hot", Metric: "temperature_celsius", Above: ptr(75), For: 2 * time.Minute, ClearFor: time.Minute}
}

func engine(rules ...Rule) (*Engine, *sink) {
	e := New(Config{Rules: rules, DeployFailures: true})
	r := &sink{}
	e.OnEvent(r.on)
	return e, r
}

// feed observes one temperature per second.
func feed(e *Engine, from time.Time, temps ...float64) time.Time {
	at := from
	for _, v := range temps {
		e.Observe([]tsdb.Point{{Series: "temperature_celsius", Value: v}}, at)
		at = at.Add(time.Second)
	}
	return at
}

func repeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestFiresAfterForAndResolvesAfterClearFor(t *testing.T) {
	e, r := engine(hot())
	at := feed(e, t0, repeat(80, 120)...) // 0..119 s: not yet 2 minutes
	if r.states() != "" || e.Active()[0].State != Pending {
		t.Fatalf("before `for`: events %q, active %+v", r.states(), e.Active())
	}
	at = feed(e, at, 80) // 120 s
	if r.states() != "firing" {
		t.Fatalf("at `for`: events %q", r.states())
	}
	ev := r.events[0]
	if ev.Summary != "CPU is hot: 80 °C (> 75 °C)" || ev.FiredAt == nil || !ev.At.Equal(t0.Add(120*time.Second)) {
		t.Errorf("firing event = %+v", ev)
	}

	at = feed(e, at, repeat(70, 60)...) // back to normal for 59 s
	if r.states() != "firing" || e.Active()[0].State != Clearing {
		t.Fatalf("while clearing: events %q, active %+v", r.states(), e.Active())
	}
	feed(e, at, 70)
	if r.states() != "firing,resolved" || len(e.Active()) != 0 {
		t.Fatalf("after clear_for: events %q, active %+v", r.states(), e.Active())
	}
	if last := r.events[1]; last.Summary != "CPU is hot: 70 °C (> 75 °C)" {
		t.Errorf("resolved summary %q", last.Summary)
	}
}

func TestFlappingDoesNotSpam(t *testing.T) {
	e, r := engine(hot())
	// Hovering at the threshold for ten minutes: above and below in turn.
	var temps []float64
	for i := range 600 {
		temps = append(temps, []float64{74, 76}[i%2])
	}
	feed(e, t0, temps...)
	if r.states() != "" {
		t.Errorf("hovering before ever being hot for 2 minutes: %q", r.states())
	}

	// Hot for good, then hovering: one firing, no resolve while it hovers.
	e, r = engine(hot())
	at := feed(e, t0, repeat(80, 121)...)
	feed(e, at, temps...)
	if r.states() != "firing" {
		t.Errorf("hovering after firing: %q, want only firing", r.states())
	}
}

func TestZeroDurationsAndBelow(t *testing.T) {
	r0 := Rule{Name: "Disk busy", Metric: "temperature_celsius", Below: ptr(10)}
	e, r := engine(r0)
	feed(e, t0, 20, 5, 20)
	if r.states() != "firing,resolved" {
		t.Errorf("for and clear_for 0: %q", r.states())
	}
}

func TestLabelledSeriesAreSeparateAlerts(t *testing.T) {
	disk := Rule{Name: "Disk is almost full", Metric: "filesystem_used_percent", Above: ptr(90)}
	e, r := engine(disk)
	e.Observe([]tsdb.Point{
		{Series: `filesystem_used_percent{path="/"}`, Value: 95},
		{Series: `filesystem_used_percent{path="/boot/firmware"}`, Value: 20},
		{Series: `filesystem_used_bytes{path="/"}`, Value: 95},
	}, t0)
	if len(r.events) != 1 || r.events[0].Summary != "Disk is almost full (/): 95 % (> 90 %)" {
		t.Fatalf("events %+v", r.events)
	}
	// A rule with labels matches only that series.
	only := Rule{Name: "Boot full", Metric: `filesystem_used_percent{path="/boot/firmware"}`, Above: ptr(10)}
	e, r = engine(only)
	e.Observe([]tsdb.Point{
		{Series: `filesystem_used_percent{path="/"}`, Value: 95},
		{Series: `filesystem_used_percent{path="/boot/firmware"}`, Value: 20},
	}, t0)
	if len(r.events) != 1 || r.events[0].Series != `filesystem_used_percent{path="/boot/firmware"}` {
		t.Errorf("events %+v", r.events)
	}
}

func TestVanishedSeries(t *testing.T) {
	e, _ := engine(hot())
	feed(e, t0, 80)
	e.Observe(nil, t0.Add(time.Second))
	if len(e.Active()) != 0 {
		t.Errorf("a pending alert outlived its series: %+v", e.Active())
	}
	at := feed(e, t0, repeat(80, 121)...)
	e.Observe(nil, at)
	if a := e.Active(); len(a) != 1 || a[0].State != Firing {
		t.Errorf("a firing alert resolved without its series being seen fine: %+v", a)
	}
}

func TestDeployFailures(t *testing.T) {
	e, r := engine()
	const blog = "ofk0/blog@main"
	e.DeployFinished(blog, "blog", false, "", t0)
	// Fails before piway.yaml is read: no app name yet.
	e.DeployFinished(blog, "", true, "fetch: exit status 128", t0.Add(time.Minute))
	e.DeployFinished(blog, "blog", true, "build: exit status 2", t0.Add(2*time.Minute))
	if r.states() != "firing" || r.events[0].Summary != "Deploy of ofk0/blog@main failed: fetch: exit status 128" {
		t.Fatalf("events %+v", r.events)
	}
	if a := e.Active(); len(a) != 1 || a[0].Summary != "Deploy of blog failed: build: exit status 2" {
		t.Errorf("active %+v", a)
	}
	e.DeployFinished("ofk0/other@main", "other", false, "", t0.Add(3*time.Minute))
	e.DeployFinished(blog, "blog", false, "", t0.Add(4*time.Minute))
	if r.states() != "firing,resolved" || r.events[1].Summary != "blog deploys again" {
		t.Errorf("events %+v", r.events)
	}

	off := New(Config{DeployFailures: false})
	off.DeployFinished(blog, "blog", true, "x", t0)
	if len(off.Active()) != 0 {
		t.Error("deploy alert with deploy_failures off")
	}
}

func TestEventsKeepsTheNewest(t *testing.T) {
	e, _ := engine(Rule{Name: "x", Metric: "temperature_celsius", Above: ptr(0)})
	for i := range keepEvents {
		feed(e, t0.Add(time.Duration(i)*time.Minute), 1, -1)
	}
	evs := e.Events()
	if len(evs) != keepEvents || evs[0].State != Resolved || !evs[0].At.After(evs[1].At) {
		t.Errorf("%d events, first %+v", len(evs), evs[0])
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if c, err := Load(filepath.Join(dir, "missing.yaml")); err != nil || len(c.Rules) != len(Defaults().Rules) || !c.DeployFailures {
		t.Errorf("missing file: %+v, %v", c, err)
	}

	path := filepath.Join(dir, "alerts.yaml")
	os.WriteFile(path, []byte(`
rules:
  - name: Hot
    metric: temperature_celsius
    above: 70
    for: 30s
  - name: Root full
    metric: filesystem_used_percent{path="/"}
    above: 80
    clear_for: 5m
deploy_failures: false
`), 0o644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Rules) != 2 || c.DeployFailures || c.Rules[0].For != 30*time.Second || c.Rules[0].ClearFor != DefaultClearFor || c.Rules[1].ClearFor != 5*time.Minute {
		t.Errorf("loaded %+v", c)
	}
	if c.Rules[0].Condition() != "> 70" {
		t.Errorf("condition %q", c.Rules[0].Condition())
	}
}

func TestLoadErrors(t *testing.T) {
	tests := map[string]string{
		"rules:\n  - name: a\n    metric: temperature_celsius\n    abve: 70\n":                                                 "abve",
		"rules:\n  - metric: temperature_celsius\n    above: 70\n":                                                             "name is required",
		"rules:\n  - name: a\n    above: 70\n":                                                                                 "metric is required",
		"rules:\n  - name: a\n    metric: temprature_celsius\n    above: 70\n":                                                 `unknown metric "temprature_celsius"`,
		"rules:\n  - name: a\n    metric: temperature_celsius\n":                                                               "exactly one of above and below",
		"rules:\n  - name: a\n    metric: temperature_celsius\n    above: 1\n    below: 0\n":                                   "exactly one of above and below",
		"rules:\n  - name: a\n    metric: temperature_celsius\n    above: 1\n    for: soon\n":                                  "for:",
		"rules:\n  - name: a\n    metric: temperature_celsius\n    above: 1\n    clear_for: 48h\n":                             "between 0s and 24h",
		"rules:\n  - {name: a, metric: temperature_celsius, above: 1}\n  - {name: a, metric: temperature_celsius, above: 2}\n": "used twice",
	}
	for body, want := range tests {
		_, err := parse([]byte(body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", body, err, want)
		}
	}
}

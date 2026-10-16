// Package alert watches the metrics and deploys for trouble, and says when
// it starts and when it ends (docs/ALERTS.md).
//
// Each rule and series is a small state machine:
//
//	ok ──breach──► pending ──breach for `for`──► firing ──ok──► clearing ──ok for `clear_for`──► resolved
//	      ▲             │                            ▲              │
//	      └─────ok──────┘                            └───breach─────┘
//
// Only firing and resolved are announced. Waiting `for` before firing and
// `clear_for` before resolving is what keeps a value hovering at the
// threshold from sending a notification every few seconds.
package alert

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ofk0/piway/internal/tsdb"
)

// State is where an alert is in its life.
type State string

const (
	Pending  State = "pending"  // breached, waiting for `for`
	Firing   State = "firing"   // announced
	Clearing State = "clearing" // firing, but back to normal; waiting for `clear_for`
	Resolved State = "resolved" // only in events
)

// Alert is one rule applied to one series (or one app, for deploys).
type Alert struct {
	Rule    string     `json:"rule"`
	Series  string     `json:"series"` // the metric series, or deploy:<repo>@<branch>
	Summary string     `json:"summary"`
	State   State      `json:"state"`
	Value   *float64   `json:"value,omitempty"` // the latest value, for metric alerts
	Since   time.Time  `json:"since"`           // when the current state began
	FiredAt *time.Time `json:"fired_at,omitempty"`
}

// Event is an alert firing or resolving: what gets announced.
type Event struct {
	Alert
	At time.Time `json:"at"`
}

// DeployRule names deploy-failure alerts.
const DeployRule = "Deploy failed"

// CrashRule names app-crash alerts.
const CrashRule = "App crashed"

// CrashQuiet is how long an app must run without crashing again before
// its crash alert resolves.
const CrashQuiet = 10 * time.Minute

const keepEvents = 50

// Engine evaluates rules on every snapshot.
type Engine struct {
	cfg Config

	mu        sync.Mutex
	instances map[string]*Alert // by rule + "\x00" + series
	clearing  map[string]time.Time
	crashes   map[string]crashState // by app
	events    []Event               // newest last
	listeners []func(Event)
}

// New returns an Engine for cfg.
func New(cfg Config) *Engine {
	return &Engine{cfg: cfg, instances: map[string]*Alert{}, clearing: map[string]time.Time{}, crashes: map[string]crashState{}}
}

type crashState struct {
	last  time.Time
	count int
}

// OnEvent registers f to run for every firing and resolved event, outside
// the engine's lock. Call it before the first Observe.
func (e *Engine) OnEvent(f func(Event)) { e.listeners = append(e.listeners, f) }

// Rules returns the rules in use.
func (e *Engine) Rules() Config { return e.cfg }

func key(rule, series string) string { return rule + "\x00" + series }

// Observe evaluates every rule against the points of one snapshot taken
// at at.
func (e *Engine) Observe(points []tsdb.Point, at time.Time) {
	e.mu.Lock()
	var out []Event
	for _, r := range e.cfg.Rules {
		seen := map[string]bool{}
		for _, p := range points {
			if !matches(r.Metric, p.Series) {
				continue
			}
			seen[p.Series] = true
			if ev, ok := e.step(r, p.Series, p.Value, at); ok {
				out = append(out, ev)
			}
		}
		// A pending series that vanished (an unmounted disk) cannot be
		// confirmed; a firing one stays until it is seen to be fine.
		for k, a := range e.instances {
			if a.Rule == r.Name && a.State == Pending && !seen[a.Series] {
				delete(e.instances, k)
			}
		}
	}
	out = append(out, e.quietCrashes(at)...)
	e.record(out)
	e.mu.Unlock()
	e.announce(out)
}

// AppCrashed fires app's crash alert with summary, or, while it fires,
// counts another crash in it.
func (e *Engine) AppCrashed(app, summary string, at time.Time) {
	if !e.cfg.AppCrashes {
		return
	}
	e.mu.Lock()
	series := "app:" + app
	k := key(CrashRule, series)
	st := e.crashes[app]
	st.last, st.count = at, st.count+1
	e.crashes[app] = st
	var out []Event
	if a, firing := e.instances[k]; firing {
		a.Summary = fmt.Sprintf("%s (%d times in a row)", summary, st.count) // no new announcement
	} else {
		a := &Alert{Rule: CrashRule, Series: series, Summary: summary}
		e.instances[k] = a
		out = append(out, e.fire(a, at))
	}
	e.record(out)
	e.mu.Unlock()
	e.announce(out)
}

// quietCrashes resolves crash alerts of apps that ran CrashQuiet since
// their last crash; caller holds e.mu.
func (e *Engine) quietCrashes(at time.Time) []Event {
	var out []Event
	for app, st := range e.crashes {
		if at.Sub(st.last) < CrashQuiet {
			continue
		}
		delete(e.crashes, app)
		k := key(CrashRule, "app:"+app)
		if a, ok := e.instances[k]; ok {
			a.Summary = fmt.Sprintf("%s has run for %s without crashing", app, CrashQuiet)
			out = append(out, e.resolve(k, a, at))
		}
	}
	return out
}

// matches reports whether a rule's metric covers series: exactly, or as
// the unlabelled name of a labelled series.
func matches(metric, series string) bool {
	if metric == series {
		return true
	}
	base, _, labelled := strings.Cut(series, "{")
	return labelled && !strings.Contains(metric, "{") && base == metric
}

// step advances one rule and series; caller holds e.mu.
func (e *Engine) step(r Rule, series string, v float64, at time.Time) (Event, bool) {
	k := key(r.Name, series)
	a, ok := e.instances[k]
	breached := r.Breached(v)
	if ok {
		a.Value = &v
		a.Summary = summary(r, series, v)
	}
	switch {
	case !ok && breached:
		a = &Alert{Rule: r.Name, Series: series, Summary: summary(r, series, v), State: Pending, Value: &v, Since: at}
		e.instances[k] = a
		if r.For == 0 {
			return e.fire(a, at), true
		}
	case !ok:
	case a.State == Pending && !breached:
		delete(e.instances, k)
	case a.State == Pending && at.Sub(a.Since) >= r.For:
		return e.fire(a, at), true
	case a.State == Firing && !breached:
		if r.ClearFor == 0 {
			return e.resolve(k, a, at), true
		}
		a.State, e.clearing[k] = Clearing, at
	case a.State == Clearing && breached:
		a.State = Firing
		delete(e.clearing, k)
	case a.State == Clearing && at.Sub(e.clearing[k]) >= r.ClearFor:
		return e.resolve(k, a, at), true
	}
	return Event{}, false
}

func (e *Engine) fire(a *Alert, at time.Time) Event {
	a.State, a.Since, a.FiredAt = Firing, at, &at
	return Event{Alert: *a, At: at}
}

func (e *Engine) resolve(k string, a *Alert, at time.Time) Event {
	delete(e.instances, k)
	delete(e.clearing, k)
	ev := Event{Alert: *a, At: at}
	ev.State, ev.Since = Resolved, at
	return ev
}

// DeployFinished fires an alert when a deploy of target (repo@branch)
// fails and resolves it when target deploys again. name is what the
// summary calls it: the app's name, or target when the build failed
// before reading piway.yaml.
func (e *Engine) DeployFinished(target, name string, failed bool, reason string, at time.Time) {
	if !e.cfg.DeployFailures || target == "" {
		return
	}
	if name == "" {
		name = target
	}
	e.mu.Lock()
	series := "deploy:" + target
	k := key(DeployRule, series)
	a, firing := e.instances[k]
	var out []Event
	switch {
	case failed:
		s := fmt.Sprintf("Deploy of %s failed", name)
		if reason != "" {
			s += ": " + reason
		}
		if firing {
			a.Summary = s // still failing: no new announcement
			break
		}
		a = &Alert{Rule: DeployRule, Series: series, Summary: s}
		e.instances[k] = a
		out = append(out, e.fire(a, at))
	case firing:
		a.Summary = fmt.Sprintf("%s deploys again", name)
		out = append(out, e.resolve(k, a, at))
	}
	e.record(out)
	e.mu.Unlock()
	e.announce(out)
}

// record keeps the newest events; caller holds e.mu.
func (e *Engine) record(evs []Event) {
	e.events = append(e.events, evs...)
	if n := len(e.events) - keepEvents; n > 0 {
		e.events = slices.Delete(e.events, 0, n)
	}
}

func (e *Engine) announce(evs []Event) {
	for _, ev := range evs {
		for _, f := range e.listeners {
			f(ev)
		}
	}
}

// Active returns the pending, firing and clearing alerts, firing first,
// then by when they began.
func (e *Engine) Active() []Alert {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Alert, 0, len(e.instances))
	for _, a := range e.instances {
		out = append(out, *a)
	}
	rank := map[State]int{Firing: 0, Clearing: 1, Pending: 2}
	slices.SortFunc(out, func(a, b Alert) int {
		if d := rank[a.State] - rank[b.State]; d != 0 {
			return d
		}
		return a.Since.Compare(b.Since)
	})
	return out
}

// Events returns the latest firing and resolved events, newest first.
func (e *Engine) Events() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := slices.Clone(e.events)
	slices.Reverse(out)
	return out
}

// summary is one line for a notification: "Disk is almost full (/):
// 93.2 % (> 90 %)".
func summary(r Rule, series string, v float64) string {
	name := r.Name
	if _, labels, ok := strings.Cut(series, "{"); ok {
		var vals []string
		for kv := range strings.SplitSeq(strings.TrimSuffix(labels, "}"), ",") {
			if _, val, ok := strings.Cut(kv, "="); ok {
				vals = append(vals, strings.Trim(val, `"`))
			}
		}
		name += " (" + strings.Join(vals, ", ") + ")"
	}
	if strings.HasPrefix(r.Metric, "throttle_") {
		return name // a flag: its value says nothing more
	}
	unit := unitOf(r.Metric)
	return fmt.Sprintf("%s: %s%s (%s%s)", name, trimFloat(round(v)), unit, r.Condition(), unit)
}

// round keeps one decimal below 100: "78.2 °C", "1234 B/s".
func round(v float64) float64 {
	if math.Abs(v) >= 100 {
		return math.Round(v)
	}
	return math.Round(v*10) / 10
}

func unitOf(metric string) string {
	switch {
	case strings.HasSuffix(metric, "_celsius"):
		return " °C"
	case strings.HasSuffix(metric, "_percent"):
		return " %"
	case strings.HasSuffix(metric, "_bytes_per_second"):
		return " B/s"
	case strings.HasSuffix(metric, "_bytes"):
		return " B"
	}
	return ""
}

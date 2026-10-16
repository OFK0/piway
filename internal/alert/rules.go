package alert

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/ofk0/piway/internal/recorder"
)

// Rule fires when Metric stays above (or below) a threshold for For, and
// resolves when it stays back for ClearFor. A metric without labels, such
// as filesystem_used_percent, watches every labelled series separately:
// each filesystem is its own alert.
type Rule struct {
	Name     string
	Metric   string
	Above    *float64
	Below    *float64
	For      time.Duration
	ClearFor time.Duration
}

// Breached reports whether v crosses the rule's threshold.
func (r Rule) Breached(v float64) bool {
	if r.Above != nil {
		return v > *r.Above
	}
	return v < *r.Below
}

// Condition describes the threshold, such as "> 75".
func (r Rule) Condition() string {
	if r.Above != nil {
		return "> " + trimFloat(*r.Above)
	}
	return "< " + trimFloat(*r.Below)
}

func trimFloat(f float64) string { return strings.TrimSuffix(fmt.Sprintf("%.6g", f), ".0") }

// Config is the alerts file.
type Config struct {
	Rules []Rule
	// DeployFailures alerts when a deploy fails, and resolves when the
	// app deploys again.
	DeployFailures bool
	// AppCrashes alerts when an app's container exits on its own (a
	// crash, an OOM kill), and resolves after CrashQuiet without another.
	AppCrashes bool
}

// DefaultClearFor is how long a value must stay back before its alert
// resolves, unless a rule says otherwise.
const DefaultClearFor = time.Minute

// maxDuration bounds for and clear_for: state is kept in memory.
const maxDuration = 24 * time.Hour

func ptr(f float64) *float64 { return &f }

// Defaults are used when there is no alerts file.
func Defaults() Config {
	return Config{
		Rules: []Rule{
			{Name: "CPU is hot", Metric: "temperature_celsius", Above: ptr(75), For: 2 * time.Minute, ClearFor: DefaultClearFor},
			{Name: "CPU is throttled", Metric: "throttle_throttled", Above: ptr(0), For: time.Minute, ClearFor: DefaultClearFor},
			{Name: "Power supply is too weak", Metric: "throttle_under_voltage", Above: ptr(0), For: 10 * time.Second, ClearFor: DefaultClearFor},
			{Name: "Disk is almost full", Metric: "filesystem_used_percent", Above: ptr(90), For: 5 * time.Minute, ClearFor: DefaultClearFor},
			{Name: "Memory is almost full", Metric: "memory_used_percent", Above: ptr(90), For: 5 * time.Minute, ClearFor: DefaultClearFor},
		},
		DeployFailures: true,
		AppCrashes:     true,
	}
}

// Load reads the alerts file at path, or returns Defaults if it does not
// exist. Errors name the rule and field.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Defaults(), nil
	}
	if err != nil {
		return Config{}, err
	}
	c, err := parse(b)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

type fileRule struct {
	Name     string   `yaml:"name"`
	Metric   string   `yaml:"metric"`
	Above    *float64 `yaml:"above"`
	Below    *float64 `yaml:"below"`
	For      string   `yaml:"for"`
	ClearFor string   `yaml:"clear_for"`
}

func parse(b []byte) (Config, error) {
	var file struct {
		Rules          []fileRule `yaml:"rules"`
		DeployFailures *bool      `yaml:"deploy_failures"`
		AppCrashes     *bool      `yaml:"app_crashes"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true) // a typo such as "abve" is an error, not a rule that never fires
	if err := dec.Decode(&file); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, err
	}
	c := Config{DeployFailures: true, AppCrashes: true}
	if file.DeployFailures != nil {
		c.DeployFailures = *file.DeployFailures
	}
	if file.AppCrashes != nil {
		c.AppCrashes = *file.AppCrashes
	}
	seen := map[string]bool{}
	for i, fr := range file.Rules {
		r, err := fr.rule()
		if err != nil {
			return Config{}, fmt.Errorf("rules[%d] (%s): %w", i, orDefault(fr.Name, "unnamed"), err)
		}
		if seen[r.Name] {
			return Config{}, fmt.Errorf("rules[%d]: the name %q is used twice", i, r.Name)
		}
		seen[r.Name] = true
		c.Rules = append(c.Rules, r)
	}
	return c, nil
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func (fr fileRule) rule() (Rule, error) {
	r := Rule{Name: strings.TrimSpace(fr.Name), Metric: fr.Metric, Above: fr.Above, Below: fr.Below, ClearFor: DefaultClearFor}
	switch {
	case r.Name == "":
		return r, errors.New("name is required")
	case len(r.Name) > 80:
		return r, errors.New("name is longer than 80 characters")
	case r.Metric == "":
		return r, errors.New("metric is required")
	case (r.Above == nil) == (r.Below == nil):
		return r, errors.New("set exactly one of above and below")
	}
	if base, _, _ := strings.Cut(r.Metric, "{"); !slices.Contains(recorder.Metrics, base) {
		return r, fmt.Errorf("unknown metric %q (see docs/ALERTS.md for the list)", base)
	}
	var err error
	if r.For, err = duration(fr.For, 0); err != nil {
		return r, fmt.Errorf("for: %w", err)
	}
	if r.ClearFor, err = duration(fr.ClearFor, DefaultClearFor); err != nil {
		return r, fmt.Errorf("clear_for: %w", err)
	}
	return r, nil
}

func duration(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d < 0 || d > maxDuration {
		return 0, fmt.Errorf("must be between 0s and %v, got %v", maxDuration, d)
	}
	return d, nil
}

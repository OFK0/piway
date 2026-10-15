// Package setup checks a piway installation (piway doctor) and installs
// what is missing (piway setup). Both work through Host, so every check is
// tested without touching the machine.
package setup

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Status is the outcome of one check.
type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
	// Skip means the check does not apply here, such as the proxy checks
	// without a proxy_domain.
	Skip Status = "skip"
)

// Result is one checked item.
type Result struct {
	Section string `json:"section"`
	ID      string `json:"id"`
	Title   string `json:"title"`
	Status  Status `json:"status"`
	Detail  string `json:"detail"`
	// Fix is the command, or the step, that solves a warning or failure.
	Fix string `json:"fix,omitempty"`
}

// Report is the outcome of a doctor run.
type Report struct {
	Results []Result `json:"checks"`
}

// Counts returns how many results have each status.
func (r *Report) Counts() map[Status]int {
	n := map[Status]int{OK: 0, Warn: 0, Fail: 0, Skip: 0}
	for _, res := range r.Results {
		n[res.Status]++
	}
	return n
}

// Failed reports whether any check failed.
func (r *Report) Failed() bool { return r.Counts()[Fail] > 0 }

// Get returns the result with id, if there is one.
func (r *Report) Get(id string) (Result, bool) {
	for _, res := range r.Results {
		if res.ID == id {
			return res, true
		}
	}
	return Result{}, false
}

// WriteText prints the report for a terminal: one line per check, grouped
// by section, with the fix under each problem.
func (r *Report) WriteText(w io.Writer, footer string) error {
	var b strings.Builder
	width := 0
	for _, res := range r.Results {
		width = max(width, len(res.Title))
	}
	section := ""
	for _, res := range r.Results {
		if res.Section != section {
			if section != "" {
				b.WriteByte('\n')
			}
			section = res.Section
			b.WriteString(section + "\n")
		}
		fmt.Fprintf(&b, "  %-4s  %-*s  %s\n", res.Status, width, res.Title, res.Detail)
		if res.Fix != "" && (res.Status == Warn || res.Status == Fail) {
			for i, line := range strings.Split(res.Fix, "\n") {
				label := "fix:"
				if i > 0 {
					label = "    "
				}
				fmt.Fprintf(&b, "  %-4s  %-*s  %s %s\n", "", width, "", label, line)
			}
		}
	}
	n := r.Counts()
	fmt.Fprintf(&b, "\n%d ok, %d %s, %d %s", n[OK], n[Warn], plural(n[Warn], "warning"), n[Fail], plural(n[Fail], "failure"))
	if n[Skip] > 0 {
		fmt.Fprintf(&b, ", %d not applicable", n[Skip])
	}
	b.WriteString(".\n")
	if footer != "" && (n[Warn] > 0 || n[Fail] > 0) {
		b.WriteString(footer + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteJSON prints the report for scripts and for the dashboard.
func (r *Report) WriteJSON(w io.Writer) error {
	out := struct {
		Results []Result       `json:"checks"`
		Summary map[Status]int `json:"summary"`
	}{r.Results, r.Counts()}
	if out.Results == nil {
		out.Results = []Result{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

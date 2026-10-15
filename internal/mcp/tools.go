package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Tools are the tools piway offers, in the order clients list them.
func Tools() []Tool {
	return []Tool{getStatus()}
}

// noArgs is the schema of a tool without arguments.
func noArgs() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
}

// Status is get_status's structured result.
type Status struct {
	Version     string        `json:"version"`
	Time        time.Time     `json:"time"`
	CPUPercent  *float64      `json:"cpu_percent,omitempty"`
	TempCelsius *float64      `json:"temperature_celsius,omitempty"`
	Throttled   []string      `json:"throttled_now,omitempty"`
	Memory      *Usage        `json:"memory,omitempty"`
	Disks       []DiskUsage   `json:"filesystems,omitempty"`
	Alerts      []ActiveAlert `json:"alerts"`
	Apps        *int          `json:"apps,omitempty"` // nil when deploys are off
	Deploys     string        `json:"deploys"`        // "on" or why they are off
}

type Usage struct {
	UsedBytes  uint64  `json:"used_bytes"`
	TotalBytes uint64  `json:"total_bytes"`
	Percent    float64 `json:"used_percent"`
}

type DiskUsage struct {
	Path    string  `json:"path"`
	Percent float64 `json:"used_percent"`
	Avail   uint64  `json:"avail_bytes"`
}

type ActiveAlert struct {
	Rule    string `json:"rule"`
	State   string `json:"state"`
	Summary string `json:"summary"`
	Since   string `json:"since"`
}

func getStatus() Tool {
	return Tool{
		Name:  "get_status",
		Title: "Pi status",
		Description: "The Raspberry Pi's health right now: CPU load and temperature, throttling, memory and disk use, " +
			"active alerts, and how many apps are deployed. Call this first to see whether anything is wrong.",
		InputSchema: noArgs(),
		Annotations: Annotations{ReadOnly: true, Idempotent: true},
		Call: func(ctx context.Context, c *Client, _ json.RawMessage) (Result, error) {
			st, err := status(ctx, c)
			if err != nil {
				return Result{}, err
			}
			return Text(describeStatus(st), st), nil
		},
	}
}

type snapshot struct {
	Time time.Time `json:"time"`
	CPU  *struct {
		Total float64 `json:"total"`
	} `json:"cpu"`
	Memory *struct {
		Total uint64 `json:"total_bytes"`
		Used  uint64 `json:"used_bytes"`
	} `json:"memory"`
	Thermal *struct {
		Temp     *float64 `json:"temp_celsius"`
		Throttle *struct {
			Now map[string]bool `json:"now"`
		} `json:"throttle"`
	} `json:"thermal"`
	Disk *struct {
		Filesystems []struct {
			Path    string  `json:"path"`
			Avail   uint64  `json:"avail_bytes"`
			Percent float64 `json:"used_percent"`
		} `json:"filesystems"`
	} `json:"disk"`
}

func status(ctx context.Context, c *Client) (Status, error) {
	var st Status
	var health struct {
		Version string `json:"version"`
	}
	if err := c.Get(ctx, "/healthz", &health); err != nil {
		return st, err
	}
	st.Version = health.Version

	var snap snapshot
	if err := c.Get(ctx, "/api/v1/snapshot", &snap); err != nil {
		return st, err
	}
	st.Time = snap.Time
	if snap.CPU != nil {
		st.CPUPercent = &snap.CPU.Total
	}
	if t := snap.Thermal; t != nil {
		st.TempCelsius = t.Temp
		if t.Throttle != nil {
			for _, k := range []string{"under_voltage", "freq_capped", "throttled", "soft_temp_limit"} {
				if t.Throttle.Now[k] {
					st.Throttled = append(st.Throttled, k)
				}
			}
		}
	}
	if m := snap.Memory; m != nil && m.Total > 0 {
		st.Memory = &Usage{m.Used, m.Total, 100 * float64(m.Used) / float64(m.Total)}
	}
	if snap.Disk != nil {
		for _, fs := range snap.Disk.Filesystems {
			st.Disks = append(st.Disks, DiskUsage{fs.Path, fs.Percent, fs.Avail})
		}
	}

	var alerts struct {
		Active []ActiveAlert `json:"active"`
	}
	if err := c.Get(ctx, "/api/v1/alerts", &alerts); err != nil && !IsStatus(err, http.StatusServiceUnavailable) {
		return st, err
	}
	st.Alerts = alerts.Active
	if st.Alerts == nil {
		st.Alerts = []ActiveAlert{}
	}

	var apps struct {
		Apps []json.RawMessage `json:"apps"`
	}
	switch err := c.Get(ctx, "/api/v1/apps", &apps); {
	case IsStatus(err, http.StatusServiceUnavailable):
		st.Deploys = "off: " + err.(*ErrStatus).Message
	case err != nil:
		return st, err
	default:
		n := len(apps.Apps)
		st.Apps, st.Deploys = &n, "on"
	}
	return st, nil
}

func describeStatus(st Status) string {
	var b strings.Builder
	fmt.Fprintf(&b, "piway %s, snapshot at %s\n", st.Version, st.Time.Format(time.RFC3339))
	if st.CPUPercent != nil {
		fmt.Fprintf(&b, "CPU: %.0f%% busy", *st.CPUPercent)
		if st.TempCelsius != nil {
			fmt.Fprintf(&b, ", %.1f °C", *st.TempCelsius)
		}
		b.WriteString("\n")
	}
	if len(st.Throttled) > 0 {
		fmt.Fprintf(&b, "Throttling now: %s\n", strings.Join(st.Throttled, ", "))
	}
	if m := st.Memory; m != nil {
		fmt.Fprintf(&b, "Memory: %.0f%% used (%s of %s)\n", m.Percent, bytesString(m.UsedBytes), bytesString(m.TotalBytes))
	}
	for _, d := range st.Disks {
		fmt.Fprintf(&b, "Disk %s: %.0f%% used, %s free\n", d.Path, d.Percent, bytesString(d.Avail))
	}
	if len(st.Alerts) == 0 {
		b.WriteString("Alerts: none\n")
	} else {
		fmt.Fprintf(&b, "Alerts (%d):\n", len(st.Alerts))
		for _, a := range st.Alerts {
			fmt.Fprintf(&b, "- [%s] %s (since %s)\n", a.State, a.Summary, a.Since)
		}
	}
	if st.Apps != nil {
		fmt.Fprintf(&b, "Apps deployed: %d\n", *st.Apps)
	} else {
		fmt.Fprintf(&b, "Deploys: %s\n", st.Deploys)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func bytesString(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

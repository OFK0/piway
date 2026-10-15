package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// decode reads tool arguments strictly: an unknown or misspelled argument
// is an error the model can correct.
func decode(args json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("bad arguments: %w", err)
	}
	return nil
}

// confirmProp is the argument every tool that changes something takes.
var confirmProp = map[string]any{
	"type":        "boolean",
	"description": "Must be true. Ask the user before setting it: this changes what runs on the Pi.",
}

// notConfirmed is the answer to a changing call without confirm: true.
func notConfirmed(action string) error {
	return fmt.Errorf("not done: this would %s. Ask the user to confirm, then call again with confirm: true", action)
}

func appProp(what string) map[string]any {
	return map[string]any{"type": "string", "description": what, "pattern": "^[a-z][a-z0-9-]{0,39}$"}
}

// app is the API's view of an app (GET /api/v1/apps).
type app struct {
	Name      string `json:"name"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	URL       string `json:"url,omitempty"`
	Live      *build `json:"live,omitempty"`
	Last      *build `json:"last,omitempty"`
	Container *struct {
		State string `json:"state"`
		Image string `json:"image"`
	} `json:"container,omitempty"`
}

type build struct {
	ID       string    `json:"id"`
	App      string    `json:"app,omitempty"`
	Repo     string    `json:"repo"`
	Branch   string    `json:"branch"`
	Commit   string    `json:"commit"`
	Kind     string    `json:"kind,omitempty"`
	State    string    `json:"state"`
	Error    string    `json:"error,omitempty"`
	Site     string    `json:"site,omitempty"`
	Queued   time.Time `json:"queued_at"`
	Finished time.Time `json:"finished_at,omitzero"`
}

type target struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	App    string `json:"app,omitempty"`
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// deploysOff turns "deploys are off" into a plain answer instead of an
// error: the model should say so, not retry.
func deploysOff(err error) (Result, bool) {
	var e *ErrStatus
	if errors.As(err, &e) && e.Code == http.StatusServiceUnavailable {
		return Text("Deploys are off on this Pi: "+e.Message, map[string]any{"deploys": "off"}), true
	}
	return Result{}, false
}

func listApps() Tool {
	return Tool{
		Name:  "list_apps",
		Title: "List apps",
		Description: "The apps deployed on the Pi: what each runs (commit, container or static site), its address, " +
			"the state of its latest build, and repository branches in apps.yaml that were never deployed.",
		InputSchema: noArgs(),
		Annotations: Annotations{ReadOnly: true, Idempotent: true},
		Call: func(ctx context.Context, c *Client, args json.RawMessage) (Result, error) {
			if err := decode(args, &struct{}{}); err != nil {
				return Result{}, err
			}
			var apps struct {
				Apps []app `json:"apps"`
			}
			if err := c.Get(ctx, "/api/v1/apps", &apps); err != nil {
				if r, ok := deploysOff(err); ok {
					return r, nil
				}
				return Result{}, err
			}
			var targets struct {
				Targets []target `json:"targets"`
			}
			if err := c.Get(ctx, "/api/v1/targets", &targets); err != nil {
				return Result{}, err
			}
			var b strings.Builder
			if len(apps.Apps) == 0 {
				b.WriteString("No apps deployed yet.\n")
			}
			for _, a := range apps.Apps {
				fmt.Fprintf(&b, "%s (%s@%s)", a.Name, a.Repo, a.Branch)
				if a.URL != "" {
					fmt.Fprintf(&b, " at %s", a.URL)
				}
				b.WriteString("\n")
				switch {
				case a.Live == nil:
					b.WriteString("  never deployed successfully\n")
				case a.Live.Site != "":
					fmt.Fprintf(&b, "  serves a static site from commit %s, %s %s\n", short(a.Live.Commit), verb(a.Live), ago(a.Live.Finished))
				default:
					state := "not running"
					if a.Container != nil {
						state = a.Container.State
					}
					fmt.Fprintf(&b, "  runs commit %s (container %s), %s %s\n", short(a.Live.Commit), state, verb(a.Live), ago(a.Live.Finished))
				}
				if l := a.Last; l != nil && (a.Live == nil || l.ID != a.Live.ID) {
					fmt.Fprintf(&b, "  latest build %s: %s", l.ID, l.State)
					if l.Error != "" {
						fmt.Fprintf(&b, " (%s)", l.Error)
					}
					b.WriteString("\n")
				}
			}
			var waiting []string
			for _, t := range targets.Targets {
				if t.App == "" {
					waiting = append(waiting, t.Repo+"@"+t.Branch)
				}
			}
			if len(waiting) > 0 {
				fmt.Fprintf(&b, "Never deployed, in apps.yaml: %s (deploy them with deploy repo+branch)\n", strings.Join(waiting, ", "))
			}
			return Text(strings.TrimSuffix(b.String(), "\n"), map[string]any{"apps": apps.Apps, "targets": targets.Targets}), nil
		},
	}
}

func verb(b *build) string {
	switch b.Kind {
	case "rollback":
		return "rolled back"
	case "restart":
		return "restarted"
	}
	return "deployed"
}

func queued(what string, b build) Result {
	// Text only: the build's JSON would leave out what to do next.
	return Text(fmt.Sprintf("%s: build %s queued (commit %s). It runs in the background, one build at a time; "+
		"to see how it ends, call tail_logs with build=%q and wait_seconds=120.", what, b.ID, short(b.Commit), b.ID), nil)
}

func deployTool() Tool {
	return Tool{
		Name:  "deploy",
		Title: "Deploy",
		Description: "Build and release the newest commit of an app's branch (or an exact commit), as a git push would. " +
			"Give app for an app that was deployed before, or repo and branch for one in apps.yaml that never was. " +
			"Changes what runs: requires confirm: true. The release has no downtime and keeps the old version if the new one is unhealthy.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"app":     appProp("The app to deploy, as list_apps names it."),
				"repo":    map[string]any{"type": "string", "description": "owner/name of a repository in apps.yaml, with branch, instead of app."},
				"branch":  map[string]any{"type": "string", "description": "The branch, with repo."},
				"commit":  map[string]any{"type": "string", "description": "A full 40-character commit SHA. Omit for the branch's newest commit.", "pattern": "^[0-9a-f]{40}$"},
				"confirm": confirmProp,
			},
			"required":             []string{"confirm"},
			"additionalProperties": false,
		},
		Annotations: Annotations{Destructive: true, OpenWorld: true},
		Call: func(ctx context.Context, c *Client, args json.RawMessage) (Result, error) {
			var a struct {
				App, Repo, Branch, Commit string
				Confirm                   bool
			}
			if err := decode(args, &a); err != nil {
				return Result{}, err
			}
			what := ""
			switch {
			case a.App != "" && a.Repo == "" && a.Branch == "":
				what = "deploy " + a.App
			case a.App == "" && a.Repo != "" && a.Branch != "":
				what = "deploy " + a.Repo + "@" + a.Branch
			default:
				return Result{}, errors.New("give either app, or repo and branch")
			}
			if a.Commit != "" {
				what += " at commit " + short(a.Commit)
			} else {
				what += " at the branch's newest commit"
			}
			if !a.Confirm {
				return Result{}, notConfirmed(what)
			}
			var b build
			var err error
			if a.App != "" {
				body := map[string]string{}
				if a.Commit != "" {
					body["commit"] = a.Commit
				}
				err = c.Do(ctx, http.MethodPost, "/api/v1/apps/"+url.PathEscape(a.App)+"/deploy", body, &b)
			} else {
				err = c.Do(ctx, http.MethodPost, "/api/v1/deploys", map[string]string{"repo": a.Repo, "branch": a.Branch, "commit": a.Commit}, &b)
			}
			if err != nil {
				if r, ok := deploysOff(err); ok {
					return r, nil
				}
				return Result{}, err
			}
			return queued("Deploy of "+cmpStr(a.App, a.Repo+"@"+a.Branch), b), nil
		},
	}
}

func cmpStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// appAction is a tool that POSTs to /api/v1/apps/{app}/<action>.
func appAction(name, title, action, desc, what string) Tool {
	return Tool{
		Name:        name,
		Title:       title,
		Description: desc,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"app":     appProp("The app, as list_apps names it."),
				"confirm": confirmProp,
			},
			"required":             []string{"app", "confirm"},
			"additionalProperties": false,
		},
		Annotations: Annotations{Destructive: true},
		Call: func(ctx context.Context, c *Client, args json.RawMessage) (Result, error) {
			var a struct {
				App     string
				Confirm bool
			}
			if err := decode(args, &a); err != nil {
				return Result{}, err
			}
			if a.App == "" {
				return Result{}, errors.New("app is required")
			}
			if !a.Confirm {
				return Result{}, notConfirmed(fmt.Sprintf(what, a.App))
			}
			var b build
			if err := c.Do(ctx, http.MethodPost, "/api/v1/apps/"+url.PathEscape(a.App)+"/"+action, nil, &b); err != nil {
				if r, ok := deploysOff(err); ok {
					return r, nil
				}
				return Result{}, err
			}
			return queued(title+" of "+a.App, b), nil
		},
	}
}

func restartApp() Tool {
	return appAction("restart_app", "Restart", "restart",
		"Start a fresh container of the version an app runs now and switch to it once healthy, without rebuilding "+
			"and without downtime. For an app that hangs or leaks memory. Requires confirm: true.",
		"replace the running container of %s with a fresh one of the same version")
}

func rollbackApp() Tool {
	return appAction("rollback_app", "Roll back", "rollback",
		"Release the version an app ran before its current one, without rebuilding, after the same health check as a deploy. "+
			"Calling it twice returns to the newer version. Requires confirm: true.",
		"roll %s back to the version it ran before")
}

// maxLogLines bounds what tail_logs returns, to keep results readable.
const maxLogLines = 1000

func tailLogs() Tool {
	return Tool{
		Name:  "tail_logs",
		Title: "Tail logs",
		Description: "The last lines of an app's output (its running container's stdout and stderr, with timestamps), " +
			"or of a build's log with the build's state. Give app or build, not both.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"app":   appProp("An app whose running container to read."),
				"build": map[string]any{"type": "string", "description": "A build id, as deploy or list_apps report it."},
				"lines": map[string]any{"type": "integer", "minimum": 1, "maximum": maxLogLines, "default": 100, "description": "How many lines from the end."},
				"wait_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": maxWait, "default": 0,
					"description": "With build: wait up to this long for the build to finish before answering, instead of polling."},
			},
			"additionalProperties": false,
		},
		Annotations: Annotations{ReadOnly: true},
		Call: func(ctx context.Context, c *Client, args json.RawMessage) (Result, error) {
			var a struct {
				App, Build  string
				Lines       int
				WaitSeconds int `json:"wait_seconds"`
			}
			if err := decode(args, &a); err != nil {
				return Result{}, err
			}
			if a.Lines == 0 {
				a.Lines = 100
			}
			if a.Lines < 1 || a.Lines > maxLogLines {
				return Result{}, fmt.Errorf("lines must be between 1 and %d", maxLogLines)
			}
			if a.WaitSeconds < 0 || a.WaitSeconds > maxWait || (a.WaitSeconds > 0 && a.Build == "") {
				return Result{}, fmt.Errorf("wait_seconds is 0 to %d, and only with build", maxWait)
			}
			switch {
			case a.App != "" && a.Build == "":
				var out string
				err := c.Get(ctx, fmt.Sprintf("/api/v1/apps/%s/logs?lines=%d", url.PathEscape(a.App), a.Lines), &out)
				if err != nil {
					if r, ok := deploysOff(err); ok {
						return r, nil
					}
					return Result{}, err
				}
				if strings.TrimSpace(out) == "" {
					out = "(no output)"
				}
				return Text(fmt.Sprintf("Last %d lines of %s:\n%s", a.Lines, a.App, out), nil), nil
			case a.Build != "" && a.App == "":
				b, err := waitBuild(ctx, c, a.Build, time.Duration(a.WaitSeconds)*time.Second)
				if err != nil {
					if r, ok := deploysOff(err); ok {
						return r, nil
					}
					return Result{}, err
				}
				var log string
				if err := c.Get(ctx, "/api/v1/builds/"+url.PathEscape(a.Build)+"/log", &log); err != nil {
					return Result{}, err
				}
				head := fmt.Sprintf("Build %s of %s@%s (commit %s): %s", b.ID, b.Repo, b.Branch, short(b.Commit), b.State)
				if b.Error != "" {
					head += " (" + b.Error + ")"
				}
				return Text(head+"\n"+lastLines(log, a.Lines), nil), nil
			}
			return Result{}, errors.New("give app or build")
		},
	}
}

// maxWait bounds wait_seconds: clients time out tool calls after a while.
const maxWait = 300

// pollEvery is how often waitBuild asks; a variable for tests.
var pollEvery = 2 * time.Second

// waitBuild gets build id, asking again until it is finished or wait has
// passed.
func waitBuild(ctx context.Context, c *Client, id string, wait time.Duration) (build, error) {
	deadline := time.Now().Add(wait)
	for {
		var b build
		if err := c.Get(ctx, "/api/v1/builds/"+url.PathEscape(id), &b); err != nil {
			return b, err
		}
		if b.State != "queued" && b.State != "running" || !time.Now().Before(deadline) {
			return b, nil
		}
		select {
		case <-ctx.Done():
			return b, ctx.Err()
		case <-time.After(pollEvery):
		}
	}
}

func lastLines(s string, n int) string {
	s = strings.TrimSuffix(s, "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = append([]string{fmt.Sprintf("(%d earlier lines left out)", len(lines)-n)}, lines[len(lines)-n:]...)
	}
	return strings.Join(lines, "\n")
}

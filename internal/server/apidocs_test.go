package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ofk0/piway/internal/agent"
	"github.com/ofk0/piway/internal/alert"
	"github.com/ofk0/piway/internal/collector/cpu"
	"github.com/ofk0/piway/internal/collector/disk"
	"github.com/ofk0/piway/internal/collector/mem"
	"github.com/ofk0/piway/internal/collector/netdev"
	"github.com/ofk0/piway/internal/collector/thermal"
	"github.com/ofk0/piway/internal/deploy"
	"github.com/ofk0/piway/internal/tsdb"
)

// The examples in docs/API.md are what a client is told to expect. These
// tests read them out of the document and compare their fields and types
// with what the code answers, so a renamed or retyped field fails here
// instead of in someone's client.

var jsonBlock = regexp.MustCompile("(?s)```json\n(.*?)\n```")

// docExample is the first JSON block after the line heading in docs/API.md.
func docExample(t *testing.T, heading string) any {
	t.Helper()
	b, err := os.ReadFile("../../docs/API.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	i := strings.Index(doc, "\n"+heading+"\n")
	if i < 0 {
		t.Fatalf("docs/API.md has no line %q", heading)
	}
	m := jsonBlock.FindStringSubmatch(doc[i:])
	if m == nil {
		t.Fatalf("no JSON block after %q", heading)
	}
	var v any
	if err := json.Unmarshal([]byte(m[1]), &v); err != nil {
		t.Fatalf("the example under %q is not JSON: %v", heading, err)
	}
	return v
}

// shape lists the field paths of v with their JSON types. Array elements
// share one path ("[]"). A null has no type: it matches anything, since the
// API sends null where a collector failed.
func shape(v any, path string, out map[string]string) {
	switch v := v.(type) {
	case map[string]any:
		out[path] = "object"
		for k, x := range v {
			shape(x, path+"."+k, out)
		}
	case []any:
		out[path] = "array"
		for _, x := range v {
			shape(x, path+"[]", out)
		}
	case nil:
	default:
		out[path] = fmt.Sprintf("%T", v)
	}
}

// sameShape fails for a field the document shows and the API does not send,
// one the API sends and the document does not show, or one with another
// type. Paths under a prefix of skip are not compared (free-form maps).
func sameShape(t *testing.T, what string, doc, real any, skip ...string) {
	t.Helper()
	d, r := map[string]string{}, map[string]string{}
	shape(doc, "", d)
	shape(round(t, real), "", r)
	skipped := func(p string) bool {
		return slices.ContainsFunc(skip, func(s string) bool { return strings.HasPrefix(p, s) })
	}
	for p, typ := range d {
		if skipped(p) {
			continue
		}
		if rt, ok := r[p]; !ok {
			t.Errorf("%s: docs/API.md shows %s, the API does not send it", what, p)
		} else if rt != typ {
			t.Errorf("%s: %s is %s in docs/API.md and %s in the API", what, p, typ, rt)
		}
	}
	for p := range r {
		if _, ok := d[p]; !ok && !skipped(p) {
			t.Errorf("%s: the API sends %s, docs/API.md does not show it", what, p)
		}
	}
}

// round turns a decoded response, or a Go value, into generic JSON.
func round(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func getJSON(t *testing.T, h http.Handler, path string) any {
	t.Helper()
	rec := serve(t, h, http.MethodGet, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
	}
	var v any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestDocsHealthz(t *testing.T) {
	sameShape(t, "/healthz", docExample(t, "## `GET /healthz`"), getJSON(t, newServer(t, stubSource{}), "/healthz"))
}

func TestDocsSnapshot(t *testing.T) {
	temp := 45.0
	s := &agent.Snapshot{
		Seq: 1234, Time: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		CPU:     &cpu.Usage{Total: 12.5, PerCore: []float64{10, 15}},
		Memory:  &mem.Info{Total: 4 << 30, Used: 1 << 30},
		Thermal: &thermal.Reading{TempCelsius: &temp, Throttle: &thermal.Throttle{}},
		Disk: &disk.Stats{
			Devices:     []disk.Device{{Name: "mmcblk0"}},
			Filesystems: []disk.Filesystem{{Path: "/"}},
		},
		Network: []netdev.Interface{{Name: "eth0"}},
		Errors:  map[string]string{"thermal": "vcgencmd: executable file not found in $PATH"},
	}
	sameShape(t, "/api/v1/snapshot", docExample(t, "### Example"), getJSON(t, newServer(t, stubSource{s}), "/api/v1/snapshot"),
		".errors.") // free-form: collector name to message
}

func TestDocsSeries(t *testing.T) {
	h, err := New(Deps{Snapshots: stubSource{}, History: &fakeQuerier{}})
	if err != nil {
		t.Fatal(err)
	}
	sameShape(t, "/api/v1/series", docExample(t, "## `GET /api/v1/series`"), getJSON(t, h, "/api/v1/series?name=cpu_usage_percent"))
}

func TestDocsAlerts(t *testing.T) {
	above := 90.0
	e := alert.New(alert.Config{Rules: []alert.Rule{{Name: "Disk is almost full", Metric: "filesystem_used_percent", Above: &above, ClearFor: time.Minute}}})
	e.Observe([]tsdb.Point{{Series: `filesystem_used_percent{path="/"}`, Value: 93.2}}, time.Date(2026, 10, 15, 12, 5, 0, 0, time.UTC))
	h, err := New(Deps{Snapshots: stubSource{}, Alerts: e})
	if err != nil {
		t.Fatal(err)
	}
	// "events" is [] in the document: its elements are the alerts again.
	sameShape(t, "/api/v1/alerts", docExample(t, "## `GET /api/v1/alerts`"), getJSON(t, h, "/api/v1/alerts"), ".events[]")
}

func TestDocsBuildAndApp(t *testing.T) {
	at := time.Date(2026, 10, 14, 18, 58, 11, 0, time.UTC)
	// Only the fields the example shows: kind, rollback_of, site, error and
	// path are described in the text below it.
	b := deploy.Build{
		ID: "20261014T185759Z-b691d31", App: "guestbook", Repo: "o/guestbook", Branch: "main", Commit: "b691d31",
		Pusher: "someone", CloneURL: "https://example.com/o/guestbook.git", State: deploy.Succeeded, Image: "piway/guestbook:b691d31",
		Queued: at, Started: at, Finished: at,
	}
	sameShape(t, "Build", docExample(t, "## Builds"), b)

	app := deploy.App{
		Name: "guestbook", Repo: "o/guestbook", Branch: "main", Port: 23438, URL: "https://guestbook.pi.local:8443",
		Live: &b, Last: &b, Container: &deploy.Container{Name: "piway-guestbook", State: "running", Image: "piway/guestbook:b691d31", Started: at},
	}
	sameShape(t, "App", docExample(t, "An `App`:"), app, ".live.", ".last.") // shown as {"id", "state", "…"}
}

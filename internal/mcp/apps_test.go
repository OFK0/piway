package mcp

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// opsAPI fakes the deploy API and records the requests that change things.
type opsAPI struct {
	mu    sync.Mutex
	posts []string // "METHOD path body"
	off   bool     // deploys disabled
}

func (f *opsAPI) handler() http.Handler {
	mux := http.NewServeMux()
	j := func(w http.ResponseWriter, code int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		w.Write([]byte(body))
	}
	guard := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if f.off {
				j(w, 503, `{"error":"deploys are disabled (no Docker daemon)"}`)
				return
			}
			if r.Method == http.MethodPost {
				b, _ := io.ReadAll(r.Body)
				f.mu.Lock()
				f.posts = append(f.posts, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(b)))
				f.mu.Unlock()
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /api/v1/apps", guard(func(w http.ResponseWriter, _ *http.Request) {
		j(w, 200, `{"apps":[
			{"name":"blog","repo":"OFK0/blog","branch":"main","url":"https://blog.local:8443",
			 "live":{"id":"b2","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state":"succeeded","finished_at":"2026-10-15T12:00:00Z"},
			 "last":{"id":"b3","commit":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","state":"failed","error":"build: exit status 1"},
			 "container":{"state":"running","image":"piway/blog:aaaaaaaaaaaa"}},
			{"name":"docs","repo":"OFK0/docs","branch":"main",
			 "live":{"id":"d1","commit":"cccccccccccccccccccccccccccccccccccccccc","state":"succeeded","site":"d1","finished_at":"2026-10-15T12:00:00Z"}}]}`)
	}))
	mux.HandleFunc("GET /api/v1/targets", guard(func(w http.ResponseWriter, _ *http.Request) {
		j(w, 200, `{"targets":[{"repo":"OFK0/blog","branch":"main","app":"blog"},{"repo":"OFK0/shop","branch":"main"}]}`)
	}))
	queued := func(w http.ResponseWriter, _ *http.Request) {
		j(w, 202, `{"id":"20261015T120000Z-ddddddd","repo":"OFK0/blog","branch":"main","commit":"dddddddddddddddddddddddddddddddddddddddd","state":"queued"}`)
	}
	mux.HandleFunc("POST /api/v1/apps/blog/deploy", guard(queued))
	mux.HandleFunc("POST /api/v1/apps/blog/restart", guard(queued))
	mux.HandleFunc("POST /api/v1/apps/blog/rollback", guard(queued))
	mux.HandleFunc("POST /api/v1/apps/nope/restart", guard(func(w http.ResponseWriter, _ *http.Request) {
		j(w, 404, `{"error":"no app with that name is running a successful build"}`)
	}))
	mux.HandleFunc("POST /api/v1/deploys", guard(queued))
	mux.HandleFunc("GET /api/v1/apps/blog/logs", guard(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("lines=" + r.URL.Query().Get("lines") + "\nlistening on :3000\n"))
	}))
	mux.HandleFunc("GET /api/v1/builds/b3", guard(func(w http.ResponseWriter, _ *http.Request) {
		j(w, 200, `{"id":"b3","repo":"OFK0/blog","branch":"main","commit":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","state":"failed","error":"build: exit status 1"}`)
	}))
	mux.HandleFunc("GET /api/v1/builds/b3/log", guard(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		var b strings.Builder
		for i := 1; i <= 300; i++ {
			b.WriteString("step " + itoa(i) + "\n")
		}
		w.Write([]byte(b.String()))
	}))
	return mux
}

func itoa(i int) string { return strconv.Itoa(i) }

func (f *opsAPI) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.posts...)
}

// callTool calls a tool and returns its text and whether it is an error.
func callTool(t *testing.T, c *Client, name, args string) (string, bool) {
	t.Helper()
	r := rpc(t, server(), c, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`}}`)
	res, ok := r["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s: no result: %v", name, r)
	}
	isErr, _ := res["isError"].(bool)
	return res["content"].([]any)[0].(map[string]any)["text"].(string), isErr
}

func TestListApps(t *testing.T) {
	api := &opsAPI{}
	text, isErr := callTool(t, client(api.handler(), ""), "list_apps", `{}`)
	for _, want := range []string{
		"blog (OFK0/blog@main) at https://blog.local:8443",
		"runs commit aaaaaaa (container running), deployed",
		"latest build b3: failed (build: exit status 1)",
		"docs (OFK0/docs@main)",
		"serves a static site from commit ccccccc",
		"Never deployed, in apps.yaml: OFK0/shop@main",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
	if isErr {
		t.Error("isError")
	}
	api.off = true
	if text, isErr := callTool(t, client(api.handler(), ""), "list_apps", `{}`); isErr || !strings.Contains(text, "Deploys are off") {
		t.Errorf("deploys off: %q %v", text, isErr)
	}
}

func TestChangingToolsNeedConfirm(t *testing.T) {
	api := &opsAPI{}
	c := client(api.handler(), "")
	for _, tc := range []struct{ tool, args, says string }{
		{"deploy", `{"app":"blog"}`, "deploy blog at the branch's newest commit"},
		{"deploy", `{"app":"blog","confirm":false}`, "deploy blog"},
		{"deploy", `{"repo":"OFK0/shop","branch":"main","commit":"dddddddddddddddddddddddddddddddddddddddd"}`, "deploy OFK0/shop@main at commit ddddddd"},
		{"restart_app", `{"app":"blog"}`, "replace the running container of blog"},
		{"rollback_app", `{"app":"blog"}`, "roll blog back"},
	} {
		text, isErr := callTool(t, c, tc.tool, tc.args)
		if !isErr || !strings.Contains(text, "not done") || !strings.Contains(text, tc.says) || !strings.Contains(text, "confirm: true") {
			t.Errorf("%s %s: %q (isError %v)", tc.tool, tc.args, text, isErr)
		}
	}
	if got := api.requests(); len(got) != 0 {
		t.Errorf("unconfirmed calls reached the API: %v", got)
	}
}

func TestChangingToolsWithConfirm(t *testing.T) {
	api := &opsAPI{}
	c := client(api.handler(), "")
	calls := []struct{ tool, args, request string }{
		{"deploy", `{"app":"blog","confirm":true}`, "POST /api/v1/apps/blog/deploy {}"},
		{"deploy", `{"app":"blog","commit":"dddddddddddddddddddddddddddddddddddddddd","confirm":true}`, `POST /api/v1/apps/blog/deploy {"commit":"dddddddddddddddddddddddddddddddddddddddd"}`},
		{"deploy", `{"repo":"OFK0/shop","branch":"main","confirm":true}`, `POST /api/v1/deploys {"branch":"main","commit":"","repo":"OFK0/shop"}`},
		{"restart_app", `{"app":"blog","confirm":true}`, "POST /api/v1/apps/blog/restart"},
		{"rollback_app", `{"app":"blog","confirm":true}`, "POST /api/v1/apps/blog/rollback"},
	}
	for _, tc := range calls {
		text, isErr := callTool(t, c, tc.tool, tc.args)
		if isErr || !strings.Contains(text, "build 20261015T120000Z-ddddddd queued") || !strings.Contains(text, `tail_logs build="20261015T120000Z-ddddddd"`) {
			t.Errorf("%s: %q", tc.tool, text)
		}
	}
	got := api.requests()
	for i, tc := range calls {
		if i >= len(got) || got[i] != tc.request {
			t.Errorf("request %d = %q, want %q", i, at(got, i), tc.request)
		}
	}

	if text, isErr := callTool(t, c, "restart_app", `{"app":"nope","confirm":true}`); !isErr || !strings.Contains(text, "404") {
		t.Errorf("unknown app: %q", text)
	}
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}

func TestArgumentErrors(t *testing.T) {
	c := client((&opsAPI{}).handler(), "")
	for _, tc := range []struct{ tool, args, says string }{
		{"deploy", `{"app":"blog","repo":"x","confirm":true}`, "either app, or repo and branch"},
		{"deploy", `{"repo":"OFK0/shop","confirm":true}`, "either app, or repo and branch"},
		{"deploy", `{"ap":"blog","confirm":true}`, "unknown field"},
		{"restart_app", `{"confirm":true}`, "app is required"},
		{"tail_logs", `{}`, "give app or build"},
		{"tail_logs", `{"app":"blog","build":"b3"}`, "give app or build"},
		{"tail_logs", `{"app":"blog","lines":5000}`, "between 1 and 1000"},
		{"list_apps", `{"x":1}`, "unknown field"},
	} {
		if text, isErr := callTool(t, c, tc.tool, tc.args); !isErr || !strings.Contains(text, tc.says) {
			t.Errorf("%s %s: %q", tc.tool, tc.args, text)
		}
	}
}

func TestTailLogs(t *testing.T) {
	c := client((&opsAPI{}).handler(), "")
	text, _ := callTool(t, c, "tail_logs", `{"app":"blog","lines":20}`)
	if !strings.Contains(text, "Last 20 lines of blog:") || !strings.Contains(text, "lines=20") || !strings.Contains(text, "listening on :3000") {
		t.Errorf("app logs:\n%s", text)
	}
	text, _ = callTool(t, c, "tail_logs", `{"build":"b3","lines":3}`)
	want := "Build b3 of OFK0/blog@main (commit bbbbbbb): failed (build: exit status 1)\n(297 earlier lines left out)\nstep 298\nstep 299\nstep 300"
	if text != want {
		t.Errorf("build log:\n%s\nwant:\n%s", text, want)
	}
}

func TestEveryToolIsAnnotated(t *testing.T) {
	changing := map[string]bool{"deploy": true, "restart_app": true, "rollback_app": true}
	for _, tool := range Tools() {
		props, _ := tool.InputSchema["properties"].(map[string]any)
		_, hasConfirm := props["confirm"]
		if changing[tool.Name] != hasConfirm || changing[tool.Name] != tool.Annotations.Destructive || changing[tool.Name] == tool.Annotations.ReadOnly {
			t.Errorf("%s: confirm %v, annotations %+v", tool.Name, hasConfirm, tool.Annotations)
		}
		if changing[tool.Name] && !strings.Contains(tool.Description, "confirm: true") {
			t.Errorf("%s does not say it needs confirm", tool.Name)
		}
	}
}

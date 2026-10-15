package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAPI answers the API paths the tools use.
func fakeAPI(t *testing.T, deploysOff bool) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	reply := func(path, body string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer piway_test" {
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"sign in first"}`))
				return
			}
			w.Write([]byte(body))
		})
	}
	reply("GET /healthz", `{"status":"ok","version":"v1.2.3"}`)
	reply("GET /api/v1/snapshot", `{"time":"2026-10-15T12:00:00Z","cpu":{"total":12.5},
		"memory":{"total_bytes":4294967296,"used_bytes":1073741824},
		"thermal":{"temp_celsius":61.2,"throttle":{"now":{"under_voltage":true,"throttled":false}}},
		"disk":{"filesystems":[{"path":"/","avail_bytes":21474836480,"used_percent":26.3}]}}`)
	reply("GET /api/v1/alerts", `{"active":[{"rule":"Power supply is too weak","state":"firing","summary":"Power supply is too weak","since":"2026-10-15T11:59:00Z"}]}`)
	if deploysOff {
		mux.HandleFunc("GET /api/v1/apps", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":"deploys are disabled (no Docker daemon)"}`))
		})
	} else {
		reply("GET /api/v1/apps", `{"apps":[{"name":"blog"},{"name":"photos"}]}`)
	}
	return mux
}

func client(h http.Handler, token string) *Client {
	c := &Client{Base: "http://piway.internal", HTTP: &http.Client{Transport: InProcess{h}}}
	if token != "" {
		c.Authorization = "Bearer " + token
	}
	return c
}

func server() *Server { return &Server{Name: "piway", Version: "test", Tools: Tools()} }

// rpc sends one message and decodes the reply.
func rpc(t *testing.T, s *Server, c *Client, msg string) map[string]any {
	t.Helper()
	reply := s.Handle(context.Background(), c, []byte(msg))
	if reply == nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(reply, &out); err != nil {
		t.Fatalf("reply %s: %v", reply, err)
	}
	return out
}

func errCode(m map[string]any) float64 {
	e, _ := m["error"].(map[string]any)
	code, _ := e["code"].(float64)
	return code
}

func TestInitializeNegotiatesTheVersion(t *testing.T) {
	s, c := server(), client(fakeAPI(t, false), "piway_test")
	for asked, want := range map[string]string{"2025-03-26": "2025-03-26", "2099-01-01": ProtocolVersions[0], "": ProtocolVersions[0]} {
		r := rpc(t, s, c, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+asked+`","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
		res := r["result"].(map[string]any)
		if res["protocolVersion"] != want {
			t.Errorf("asked %q: got %v, want %s", asked, res["protocolVersion"], want)
		}
		if res["serverInfo"].(map[string]any)["name"] != "piway" || res["capabilities"].(map[string]any)["tools"] == nil {
			t.Errorf("initialize result %v", res)
		}
	}
}

func TestProtocolEdges(t *testing.T) {
	s, c := server(), client(fakeAPI(t, false), "piway_test")
	if r := rpc(t, s, c, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); r != nil {
		t.Errorf("a notification got a reply: %v", r)
	}
	if r := rpc(t, s, c, `{"jsonrpc":"2.0","id":7,"result":{}}`); r != nil {
		t.Errorf("a response got a reply: %v", r)
	}
	if r := rpc(t, s, c, `{"jsonrpc":"2.0","id":"a","method":"ping"}`); r["id"] != "a" || r["result"] == nil {
		t.Errorf("ping: %v", r)
	}
	tests := map[string]float64{
		`{nope`: codeParse,
		`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`:                           codeInvalidRequest,
		`{"jsonrpc":"1.0","id":1,"method":"ping"}`:                             codeInvalidRequest,
		`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`:                   codeMethodNotFound,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x"}}`: codeInvalidParams,
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`:                       codeInvalidParams,
	}
	for msg, want := range tests {
		if got := errCode(rpc(t, s, c, msg)); got != want {
			t.Errorf("%s: code %v, want %v", msg, got, want)
		}
	}
}

func TestToolsListHasSchemas(t *testing.T) {
	s, c := server(), client(fakeAPI(t, false), "")
	r := rpc(t, s, c, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := r["result"].(map[string]any)["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("no tools")
	}
	for _, x := range tools {
		tool := x.(map[string]any)
		schema, _ := tool["inputSchema"].(map[string]any)
		if tool["name"] == "" || tool["description"] == "" || schema["type"] != "object" || tool["annotations"] == nil {
			t.Errorf("tool %v", tool)
		}
	}
}

func TestGetStatus(t *testing.T) {
	s := server()
	r := rpc(t, s, client(fakeAPI(t, false), "piway_test"), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_status","arguments":{}}}`)
	res := r["result"].(map[string]any)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	for _, want := range []string{"piway v1.2.3", "CPU: 12% busy, 61.2 °C", "Throttling now: under_voltage", "Memory: 25% used (1.0 GiB of 4.0 GiB)", "Disk /: 26% used, 20.0 GiB free", "[firing] Power supply is too weak", "Apps deployed: 2"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	st := res["structuredContent"].(map[string]any)
	if st["apps"] != 2.0 || st["deploys"] != "on" || len(st["alerts"].([]any)) != 1 {
		t.Errorf("structured %v", st)
	}

	off := rpc(t, s, client(fakeAPI(t, true), "piway_test"), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_status"}}`)
	text = off["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "Deploys: off: deploys are disabled") {
		t.Errorf("deploys off:\n%s", text)
	}
}

func TestToolErrorsAreResultsNotProtocolErrors(t *testing.T) {
	r := rpc(t, server(), client(fakeAPI(t, false), "piway_wrong"), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_status","arguments":{}}}`)
	res, ok := r["result"].(map[string]any)
	if !ok || res["isError"] != true || !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), "401") {
		t.Errorf("bad token: %v", r)
	}
	var e *ErrStatus
	c := client(fakeAPI(t, false), "")
	if err := c.Get(context.Background(), "/healthz", nil); !errors.As(err, &e) || e.Code != 401 {
		t.Errorf("no token: %v", err)
	}
}

func TestStdio(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}

{"jsonrpc":"2.0","id":2,"method":"ping"}
`)
	var out bytes.Buffer
	if err := server().ServeStdio(context.Background(), client(fakeAPI(t, false), ""), in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"id":1`) || !strings.Contains(lines[1], `"id":2`) {
		t.Errorf("stdout:\n%s", out.String())
	}
}

func TestHTTPTransport(t *testing.T) {
	api := fakeAPI(t, false)
	h := server().HTTPHandler(func(r *http.Request) *Client {
		return client(api, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	})
	post := func(body, version string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer piway_test")
		if version != "" {
			req.Header.Set("MCP-Protocol-Version", version)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	w := post(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_status"}}`, "2025-06-18")
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/json" || !strings.Contains(w.Body.String(), "Apps deployed: 2") {
		t.Errorf("call: %d %s", w.Code, w.Body)
	}
	if w := post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, ""); w.Code != http.StatusAccepted {
		t.Errorf("notification: %d", w.Code)
	}
	if w := post(`{"jsonrpc":"2.0","id":1,"method":"ping"}`, "1999-01-01"); w.Code != http.StatusBadRequest {
		t.Errorf("unknown protocol version: %d", w.Code)
	}
	req := httptest.NewRequest("GET", "/mcp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", rec.Code)
	}
	req = httptest.NewRequest("POST", "/mcp", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "text/plain")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: %d", rec.Code)
	}
}

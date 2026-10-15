// Package mcp lets AI assistants such as Claude Code inspect and operate
// piway through the Model Context Protocol (docs/MCP.md).
//
// MCP is JSON-RPC 2.0. A client opens with "initialize", then lists tools
// ("tools/list") and calls them ("tools/call"). Server handles one message
// at a time and knows nothing about transports: stdio (piway mcp, for a
// client that starts piway as a subprocess) and streamable HTTP (POST /mcp
// on the running server) are thin wrappers around Handle.
//
// Every tool works through piway's HTTP API (Client), with the caller's
// credentials, so MCP can do exactly what the API allows the caller and
// no more.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
)

// ProtocolVersions are the MCP revisions piway speaks, newest first.
var ProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// Tool is one operation a client may call.
type Tool struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description"`
	// InputSchema is a JSON Schema object for the arguments.
	InputSchema map[string]any `json:"inputSchema"`
	Annotations Annotations    `json:"annotations"`
	// Call runs the tool. A returned error becomes a tool result with
	// isError set, which the model sees and can act on; it is not a
	// protocol error.
	Call func(ctx context.Context, c *Client, args json.RawMessage) (Result, error) `json:"-"`
}

// Annotations are hints for clients deciding whether to ask the user
// before a call (MCP 2025-03-26).
type Annotations struct {
	ReadOnly    bool `json:"readOnlyHint"`
	Destructive bool `json:"destructiveHint"`
	Idempotent  bool `json:"idempotentHint"`
	OpenWorld   bool `json:"openWorldHint"`
}

// Result is a tool's answer: text for the model, and optionally the same
// data as JSON. Set Structured only when it holds everything the text
// says: clients such as Claude Code give the model the JSON instead of
// the text when both are there.
type Result struct {
	Content    []Content `json:"content"`
	Structured any       `json:"structuredContent,omitempty"`
	IsError    bool      `json:"isError,omitempty"`
}

// Content is one block of a result. piway only returns text.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Text is a result with one text block and optional structured data.
func Text(s string, structured any) Result {
	return Result{Content: []Content{{Type: "text", Text: s}}, Structured: structured}
}

// Server answers MCP requests with a set of tools.
type Server struct {
	Name, Version string
	Tools         []Tool
	Log           *slog.Logger
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Handle processes one JSON-RPC message from the client and returns the
// response to send, or nil for a notification or a response, which get
// none. c is how this client reaches piway's API.
func (s *Server) Handle(ctx context.Context, c *Client, msg []byte) []byte {
	var req request
	if err := json.Unmarshal(msg, &req); err != nil {
		if len(msg) > 0 && msg[0] == '[' {
			return s.errorReply(nil, codeInvalidRequest, "batches are not supported")
		}
		return s.errorReply(nil, codeParse, "not valid JSON")
	}
	if req.JSONRPC != "2.0" {
		return s.errorReply(req.ID, codeInvalidRequest, `"jsonrpc" must be "2.0"`)
	}
	if req.Method == "" {
		return nil // a response to something we sent; we send nothing
	}
	if len(req.ID) == 0 || string(req.ID) == "null" {
		return nil // a notification, such as notifications/initialized
	}
	result, rerr := s.dispatch(ctx, c, req)
	if rerr != nil {
		return s.errorReply(req.ID, rerr.Code, rerr.Message)
	}
	b, err := json.Marshal(response{JSONRPC: "2.0", ID: req.ID, Result: result})
	if err != nil {
		return s.errorReply(req.ID, -32603, "cannot encode the result")
	}
	return b
}

func (s *Server) errorReply(id json.RawMessage, code int, msg string) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	b, _ := json.Marshal(response{JSONRPC: "2.0", ID: id, Error: &rpcError{code, msg}})
	return b
}

func (s *Server) dispatch(ctx context.Context, c *Client, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
			ClientInfo      struct {
				Name, Version string
			} `json:"clientInfo"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{codeInvalidParams, "initialize needs params"}
		}
		version := ProtocolVersions[0]
		if slices.Contains(ProtocolVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion // the client's, if we speak it
		}
		if s.Log != nil {
			s.Log.Info("MCP client connected", "client", p.ClientInfo.Name, "client_version", p.ClientInfo.Version, "protocol", version)
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]string{"name": s.Name, "version": s.Version},
			"instructions": "piway runs on a Raspberry Pi: it collects system metrics, deploys apps from git pushes, " +
				"and raises alerts. Start with get_status. Tools that change something ask for confirm: true.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.Tools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{codeInvalidParams, "tools/call needs a name and arguments"}
		}
		i := slices.IndexFunc(s.Tools, func(t Tool) bool { return t.Name == p.Name })
		if i < 0 {
			return nil, &rpcError{codeInvalidParams, fmt.Sprintf("unknown tool %q", p.Name)}
		}
		if len(p.Arguments) == 0 || string(p.Arguments) == "null" {
			p.Arguments = json.RawMessage("{}")
		}
		res, err := s.Tools[i].Call(ctx, c, p.Arguments)
		if err != nil {
			return Result{Content: []Content{{Type: "text", Text: err.Error()}}, IsError: true}, nil
		}
		if res.Content == nil {
			res.Content = []Content{}
		}
		return res, nil
	}
	return nil, &rpcError{codeMethodNotFound, fmt.Sprintf("method %q not found", req.Method)}
}

package mcp

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
)

// ServeStdio reads newline-delimited JSON-RPC messages from r and writes
// the responses to w, until r ends or ctx is cancelled (MCP stdio
// transport). Logs must go elsewhere: w carries only protocol messages.
func (s *Server) ServeStdio(ctx context.Context, c *Client, r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	out := bufio.NewWriter(w)
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if reply := s.Handle(ctx, c, line); reply != nil {
			out.Write(reply)
			out.WriteByte('\n')
			if err := out.Flush(); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

// maxHTTPMessage bounds one POSTed message.
const maxHTTPMessage = 1 << 20

// HTTPHandler serves the streamable HTTP transport at one path: the client
// POSTs each message and gets the response as application/json. piway
// never sends requests of its own, so there is no event stream: GET gets
// 405, as the spec allows. newClient makes the API client for a request,
// carrying its credentials.
func (s *Server) HTTPHandler(newClient func(*http.Request) *Client) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "POST JSON-RPC messages here; piway opens no event stream", http.StatusMethodNotAllowed)
			return
		}
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
			http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
		if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !slices.Contains(ProtocolVersions, v) {
			http.Error(w, "unsupported MCP-Protocol-Version "+v+"; piway speaks "+strings.Join(ProtocolVersions, ", "), http.StatusBadRequest)
			return
		}
		msg, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHTTPMessage))
		if err != nil {
			http.Error(w, "message too large", http.StatusRequestEntityTooLarge)
			return
		}
		reply := s.Handle(r.Context(), newClient(r), msg)
		if reply == nil {
			w.WriteHeader(http.StatusAccepted) // a notification or response
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(reply)
	})
}

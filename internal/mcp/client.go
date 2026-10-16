package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// UserAgent marks the MCP server's requests, so the API can keep secret
// values from it even when the MCP client signed in with a browser
// session.
const UserAgent = "piway-mcp"

// Client calls piway's HTTP API on behalf of one MCP client, with that
// client's credentials.
type Client struct {
	Base string // such as http://127.0.0.1:8080
	// Authorization and Cookie are copied to every request.
	Authorization string
	Cookie        string
	HTTP          *http.Client
	// RemoteAddr, for in-process calls, is the MCP client's address, so
	// piway's sign-in rules see who is really asking.
	RemoteAddr string
}

// ErrStatus is an API answer other than 2xx.
type ErrStatus struct {
	Code    int
	Message string
}

func (e *ErrStatus) Error() string {
	switch e.Code {
	case http.StatusUnauthorized:
		return "piway refused the credentials (401): check the API token: " + e.Message
	case http.StatusForbidden:
		return "piway refused this request (403): " + e.Message
	}
	return fmt.Sprintf("piway answered %d: %s", e.Code, e.Message)
}

// Do sends a request and decodes a JSON answer into out (unless nil).
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.Base, "/")+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Authorization != "" {
		req.Header.Set("Authorization", c.Authorization)
	}
	if c.Cookie != "" {
		req.Header.Set("Cookie", c.Cookie)
	}
	req.Header.Set("User-Agent", UserAgent)
	if c.RemoteAddr != "" {
		req.RemoteAddr = c.RemoteAddr
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach piway at %s: %w", c.Base, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return &ErrStatus{resp.StatusCode, msg}
	}
	if out == nil {
		return nil
	}
	if s, ok := out.(*string); ok {
		*s = string(data)
		return nil
	}
	return json.Unmarshal(data, out)
}

// Get is Do with GET.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.Do(ctx, http.MethodGet, path, nil, out)
}

// IsStatus reports whether err is an API answer with code.
func IsStatus(err error, code int) bool {
	var e *ErrStatus
	return errors.As(err, &e) && e.Code == code
}

// InProcess is an http.RoundTripper that hands requests to h directly:
// the HTTP transport's tools call piway's own API without a network hop,
// through the same handler (and so the same sign-in checks) as any other
// request.
type InProcess struct{ Handler http.Handler }

func (t InProcess) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := &recorder{header: http.Header{}, code: http.StatusOK}
	in := req.Clone(req.Context())
	in.RequestURI = req.URL.RequestURI()
	if in.Body == nil {
		in.Body = http.NoBody // as a real server gives a request without one
	}
	if in.RemoteAddr == "" {
		in.RemoteAddr = req.RemoteAddr
	}
	t.Handler.ServeHTTP(rec, in)
	return &http.Response{
		StatusCode: rec.code, Status: http.StatusText(rec.code),
		Header: rec.header, Body: io.NopCloser(&rec.body),
		Request: req, ProtoMajor: 1, ProtoMinor: 1,
	}, nil
}

type recorder struct {
	header http.Header
	code   int
	body   bytes.Buffer
	wrote  bool
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		r.code, r.wrote = code, true
	}
}
func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.body.Write(b)
}

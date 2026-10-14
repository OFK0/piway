// Package docker is a small client for the Docker Engine API over its unix
// socket: just what piway needs to run apps (docs/DOCKER.md).
//
// The Engine API is plain HTTP with JSON bodies; only the transport is
// unusual. Instead of TCP, every request is dialled to a unix socket, so the
// URL's host is ignored ("http://docker/..."). The official SDK pulls in
// dozens of modules for the same few calls.
//
// Every container piway creates carries the label piway.app=<name>, so
// piway only ever lists or removes its own containers.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// APIVersion is the Engine API version piway speaks. Docker 25 and later
// accept it: Debian 13's docker.io (26) and Docker CE (29, which on the Pi
// accepts 1.40 to 1.56).
const APIVersion = "1.44"

// LabelApp marks containers managed by piway; the value is the app name.
const LabelApp = "piway.app"

var (
	// ErrNotFound is returned for an unknown container or image.
	ErrNotFound = errors.New("docker: not found")
	// ErrConflict is returned when the name is taken or the container is
	// in the wrong state for the operation.
	ErrConflict = errors.New("docker: conflict")
)

// Client talks to one Docker daemon. It is safe for concurrent use.
type Client struct {
	http *http.Client
}

// New returns a client for the daemon listening on the unix socket at path.
func New(path string) *Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &Client{http: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", path)
		},
		MaxIdleConns:    4,
		IdleConnTimeout: 30 * time.Second,
	}}}
}

// SocketPath finds the daemon like the docker CLI does: $DOCKER_HOST when it
// is a unix:// address, else the rootless socket in $XDG_RUNTIME_DIR, else
// the system socket.
func SocketPath(getenv func(string) string) (string, error) {
	if h := getenv("DOCKER_HOST"); h != "" {
		p, ok := strings.CutPrefix(h, "unix://")
		if !ok {
			return "", fmt.Errorf("docker: DOCKER_HOST %q is not a unix:// socket", h)
		}
		return p, nil
	}
	if d := getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "docker.sock"), nil
	}
	return "/var/run/docker.sock", nil
}

// Version describes the daemon.
type Version struct {
	Version    string `json:"Version"`
	APIVersion string `json:"ApiVersion"`
	Arch       string `json:"Arch"`
	OS         string `json:"Os"`
}

// Version returns the daemon's version; it doubles as a reachability check.
func (c *Client) Version(ctx context.Context) (Version, error) {
	var v Version
	err := c.do(ctx, http.MethodGet, "/version", nil, nil, &v)
	return v, err
}

// Spec is what piway asks of a new container.
type Spec struct {
	Image  string
	Env    []string          // KEY=value
	Labels map[string]string // LabelApp is added by Create
	// Port is the container port to publish on 127.0.0.1:HostPort (only
	// Caddy on the same host should reach apps directly). Zero publishes
	// nothing.
	Port, HostPort int
	// Limits become cgroup v2 settings of the container's cgroup:
	// MemoryBytes is memory.max (the kernel reclaims, then OOM-kills
	// inside the container only); CPUs is cpu.max as quota/period, so 0.5
	// is 50 ms of CPU time every 100 ms across all cores. Zero is no limit.
	// Memory limits need the memory controller (docs/DOCKER.md).
	MemoryBytes int64
	CPUs        float64
}

type createBody struct {
	Image        string              `json:"Image"`
	Env          []string            `json:"Env,omitempty"`
	Labels       map[string]string   `json:"Labels"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
	HostConfig   createHostConfig    `json:"HostConfig"`
}

type createHostConfig struct {
	PortBindings  map[string][]portBinding `json:"PortBindings,omitempty"`
	RestartPolicy restartPolicy            `json:"RestartPolicy"`
	Memory        int64                    `json:"Memory,omitempty"`
	NanoCPUs      int64                    `json:"NanoCpus,omitempty"`
}

type portBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type restartPolicy struct {
	Name string `json:"Name"`
}

// Create creates (but does not start) a container named name for app.
func (c *Client) Create(ctx context.Context, name, app string, spec Spec) (id string, err error) {
	labels := map[string]string{LabelApp: app}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	body := createBody{
		Image:  spec.Image,
		Env:    spec.Env,
		Labels: labels,
		HostConfig: createHostConfig{
			// The daemon restarts a crashed app; piway decides about
			// everything else (deploys, rollbacks).
			RestartPolicy: restartPolicy{Name: "unless-stopped"},
			Memory:        spec.MemoryBytes,
			NanoCPUs:      int64(spec.CPUs * 1e9),
		},
	}
	if spec.Port > 0 {
		p := strconv.Itoa(spec.Port) + "/tcp"
		body.ExposedPorts = map[string]struct{}{p: {}}
		body.HostConfig.PortBindings = map[string][]portBinding{
			p: {{HostIP: "127.0.0.1", HostPort: strconv.Itoa(spec.HostPort)}},
		}
	}
	var out struct {
		ID string `json:"Id"`
	}
	err = c.do(ctx, http.MethodPost, "/containers/create", url.Values{"name": {name}}, body, &out)
	return out.ID, err
}

// Start starts a created or stopped container. Starting a running
// container is not an error.
func (c *Client) Start(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil, nil)
}

// Stop sends SIGTERM, then SIGKILL after grace. Stopping a stopped
// container is not an error.
func (c *Client) Stop(ctx context.Context, id string, grace time.Duration) error {
	q := url.Values{"t": {strconv.Itoa(int(grace.Seconds()))}}
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/stop", q, nil, nil)
}

// Remove deletes a container; force stops it first if it is running.
func (c *Client) Remove(ctx context.Context, id string, force bool) error {
	q := url.Values{"force": {strconv.FormatBool(force)}}
	return c.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), q, nil, nil)
}

// Container is a summary of one container.
type Container struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	State   string            `json:"State"`  // created, running, exited, ...
	Status  string            `json:"Status"` // "Up 3 minutes"
	Labels  map[string]string `json:"Labels"`
	Created int64             `json:"Created"`
}

// App returns the piway app the container belongs to, if any.
func (c Container) App() string { return c.Labels[LabelApp] }

// List returns piway's containers, including stopped ones.
func (c *Client) List(ctx context.Context) ([]Container, error) {
	filters, _ := json.Marshal(map[string][]string{"label": {LabelApp}})
	q := url.Values{"all": {"true"}, "filters": {string(filters)}}
	var out []Container
	err := c.do(ctx, http.MethodGet, "/containers/json", q, nil, &out)
	return out, err
}

// State is the runtime state of one container.
type State struct {
	Status     string    `json:"Status"`
	Running    bool      `json:"Running"`
	ExitCode   int       `json:"ExitCode"`
	OOMKilled  bool      `json:"OOMKilled"`
	StartedAt  time.Time `json:"StartedAt"`
	FinishedAt time.Time `json:"FinishedAt"`
	Health     *struct {
		Status string `json:"Status"` // starting, healthy, unhealthy
	} `json:"Health"`
}

// Inspect returns a container's state.
func (c *Client) Inspect(ctx context.Context, id string) (State, error) {
	var out struct {
		State State `json:"State"`
	}
	err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, nil, &out)
	return out.State, err
}

// Pull downloads image (name:tag). The daemon streams progress as JSON
// lines; an error can arrive in the stream after a 200, so it is read to
// the end and checked.
func (c *Client) Pull(ctx context.Context, image string) error {
	name, tag := image, "latest"
	if i := strings.LastIndexByte(image, ':'); i > strings.LastIndexByte(image, '/') {
		name, tag = image[:i], image[i+1:]
	}
	resp, err := c.request(ctx, http.MethodPost, "/images/create", url.Values{"fromImage": {name}, "tag": {tag}}, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var msg struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) == nil && msg.Error != "" {
			return fmt.Errorf("docker: pull %s: %s", image, msg.Error)
		}
	}
	return sc.Err()
}

// do sends a request and decodes a JSON response into out (if non-nil).
func (c *Client) do(ctx context.Context, method, path string, q url.Values, in, out any) error {
	resp, err := c.request(ctx, method, path, q, in)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("docker: %s %s: decode: %w", method, path, err)
	}
	return nil
}

// request sends a request and turns error statuses into errors. 304 (start
// of a running or stop of a stopped container) counts as success.
func (c *Client) request(ctx context.Context, method, path string, q url.Values, in any) (*http.Response, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	u := "http://docker/v" + APIVersion + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker: %w", err)
	}
	if resp.StatusCode < 400 {
		return resp, nil
	}
	defer resp.Body.Close()
	var msg struct {
		Message string `json:"message"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&msg)
	if msg.Message == "" {
		msg.Message = resp.Status
	}
	var kind error
	switch resp.StatusCode {
	case http.StatusNotFound:
		kind = ErrNotFound
	case http.StatusConflict:
		kind = ErrConflict
	default:
		return nil, fmt.Errorf("docker: %s %s: %s", method, path, msg.Message)
	}
	return nil, fmt.Errorf("%w: %s", kind, msg.Message)
}

// Available reports whether a daemon socket exists at path, for a clear
// message before the first request fails.
func Available(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

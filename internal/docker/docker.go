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
	"cmp"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// APIVersion is the Engine API version piway speaks. Docker 25 and later
// accept it: Docker CE 29, which docs/DOCKER.md installs (accepts 1.40 to
// 1.56), and Debian 13's older docker.io (26).
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

// Info is what the daemon supports. MemoryLimit is false when the memory
// cgroup controller is off (Raspberry Pi OS default, docs/DOCKER.md): the
// daemon then accepts a memory limit and silently does not apply it.
type Info struct {
	MemoryLimit bool   `json:"MemoryLimit"`
	CPUCfsQuota bool   `json:"CpuCfsQuota"`
	Driver      string `json:"Driver"`
}

// Info reports the daemon's capabilities.
func (c *Client) Info(ctx context.Context) (Info, error) {
	var i Info
	err := c.do(ctx, http.MethodGet, "/info", nil, nil, &i)
	return i, err
}

// Spec is what piway asks of a new container.
type Spec struct {
	Image  string
	Cmd    []string          // nil keeps the image's CMD
	Env    []string          // KEY=value
	Labels map[string]string // LabelApp is added by Create
	// Port is the container port to publish on 127.0.0.1:HostPort (only
	// piway's proxy on the same host should reach apps directly). Zero
	// publishes nothing; HostPort zero lets Docker choose a free port.
	Port, HostPort int
	// Limits become cgroup v2 settings of the container's cgroup:
	// MemoryBytes is memory.max (the kernel reclaims, then OOM-kills
	// inside the container only); CPUs is cpu.max as quota/period, so 0.5
	// is 50 ms of CPU time every 100 ms across all cores. Zero is no limit.
	// Memory limits need the memory controller (docs/DOCKER.md).
	MemoryBytes int64
	CPUs        float64
	// Mounts are named volumes, created by CreateVolume first.
	Mounts []Mount
	// Network is the network the container starts on; "" is Docker's
	// default bridge. Connect adds more.
	Network string
}

// Mount puts the named volume Volume at Target inside the container.
type Mount struct {
	Volume string
	Target string
}

type createBody struct {
	Image        string              `json:"Image"`
	Cmd          []string            `json:"Cmd,omitempty"`
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
	Mounts        []mountSpec              `json:"Mounts,omitempty"`
	NetworkMode   string                   `json:"NetworkMode,omitempty"`
}

type mountSpec struct {
	Type   string `json:"Type"`
	Source string `json:"Source"`
	Target string `json:"Target"`
}

type portBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type restartPolicy struct {
	Name string `json:"Name"`
}

// Create creates (but does not start) a container named name for app. An
// empty app makes a container that is not an app (List leaves it out).
func (c *Client) Create(ctx context.Context, name, app string, spec Spec) (id string, err error) {
	labels := map[string]string{}
	if app != "" {
		labels[LabelApp] = app
	}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	body := createBody{
		Image:  spec.Image,
		Cmd:    spec.Cmd,
		Env:    spec.Env,
		Labels: labels,
		HostConfig: createHostConfig{
			// The daemon restarts a crashed app; piway decides about
			// everything else (deploys, rollbacks).
			RestartPolicy: restartPolicy{Name: "unless-stopped"},
			Memory:        spec.MemoryBytes,
			NanoCPUs:      int64(spec.CPUs * 1e9),
			NetworkMode:   spec.Network,
		},
	}
	for _, m := range spec.Mounts {
		body.HostConfig.Mounts = append(body.HostConfig.Mounts, mountSpec{Type: "volume", Source: m.Volume, Target: m.Target})
	}
	if spec.Port > 0 {
		p := strconv.Itoa(spec.Port) + "/tcp"
		body.ExposedPorts = map[string]struct{}{p: {}}
		host := "" // Docker picks a free port; see Details.Ports
		if spec.HostPort > 0 {
			host = strconv.Itoa(spec.HostPort)
		}
		body.HostConfig.PortBindings = map[string][]portBinding{
			p: {{HostIP: "127.0.0.1", HostPort: host}},
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

// Volume is a named volume and the space it takes.
type Volume struct {
	Name      string            `json:"Name"`
	Labels    map[string]string `json:"Labels"`
	CreatedAt string            `json:"CreatedAt"`
	// Mountpoint is where the data is on this machine. With rootless
	// Docker it belongs to the user piway runs as.
	Mountpoint string `json:"Mountpoint"`
	UsageData  *struct {
		Size     int64 `json:"Size"`     // bytes; -1 if not computed
		RefCount int64 `json:"RefCount"` // containers that use it
	} `json:"UsageData"`
}

// CreateVolume creates the named volume with labels, or returns the one
// that exists already (Docker treats this as idempotent).
func (c *Client) CreateVolume(ctx context.Context, name string, labels map[string]string) (Volume, error) {
	body := map[string]any{"Name": name, "Labels": labels}
	var v Volume
	err := c.do(ctx, http.MethodPost, "/volumes/create", nil, body, &v)
	return v, err
}

// EnsureNetwork creates a bridge network unless it exists. An internal
// network has no route out: its containers reach each other only.
func (c *Client) EnsureNetwork(ctx context.Context, name string, internal bool, labels map[string]string) error {
	err := c.do(ctx, http.MethodGet, "/networks/"+url.PathEscape(name), nil, nil, nil)
	if err == nil || !errors.Is(err, ErrNotFound) {
		return err
	}
	body := map[string]any{"Name": name, "Driver": "bridge", "Internal": internal, "Labels": labels, "CheckDuplicate": true}
	err = c.do(ctx, http.MethodPost, "/networks/create", nil, body, nil)
	if errors.Is(err, ErrConflict) {
		return nil // created meanwhile
	}
	return err
}

// Connect attaches a container to a network, so it reaches the others
// on it by name. Connecting twice is not an error.
func (c *Client) Connect(ctx context.Context, network, container string) error {
	body := map[string]any{"Container": container}
	err := c.do(ctx, http.MethodPost, "/networks/"+url.PathEscape(network)+"/connect", nil, body, nil)
	if err != nil && strings.Contains(err.Error(), "already exists in network") {
		return nil
	}
	return err
}

// Volumes returns the volumes with label set, with their sizes. Sizes
// come from the daemon's disk usage report, which walks each volume.
func (c *Client) Volumes(ctx context.Context, label string) ([]Volume, error) {
	var out struct {
		Volumes     []Volume `json:"Volumes"`
		VolumeUsage *struct {
			Items []Volume `json:"Items"`
		} `json:"VolumeUsage"` // newer daemons
	}
	if err := c.do(ctx, http.MethodGet, "/system/df", url.Values{"type": {"volume"}}, nil, &out); err != nil {
		return nil, err
	}
	all := out.Volumes
	if len(all) == 0 && out.VolumeUsage != nil {
		all = out.VolumeUsage.Items
	}
	var vs []Volume
	for _, v := range all {
		if _, ok := v.Labels[label]; ok {
			vs = append(vs, v)
		}
	}
	return vs, nil
}

// RemoveVolume deletes a volume and its data. A volume a container uses
// is refused with ErrConflict.
func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(name), nil, nil, nil)
}

// Rename gives a container another name.
func (c *Client) Rename(ctx context.Context, id, name string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/rename", url.Values{"name": {name}}, nil, nil)
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
	d, err := c.Details(ctx, id)
	return d.State, err
}

// Details is what piway needs to know about one container.
type Details struct {
	ID     string
	Name   string // without the leading "/"
	Image  string // as created, e.g. piway/guestbook:0123456789ab
	Labels map[string]string
	State  State
	// Ports maps "3000/tcp" to the host port Docker published it on.
	Ports map[string]int
}

// Details inspects a container.
func (c *Client) Details(ctx context.Context, id string) (Details, error) {
	var out struct {
		ID     string `json:"Id"`
		Name   string `json:"Name"`
		State  State  `json:"State"`
		Config struct {
			Image  string            `json:"Image"`
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		NetworkSettings struct {
			Ports map[string][]portBinding `json:"Ports"`
		} `json:"NetworkSettings"`
	}
	if err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, nil, &out); err != nil {
		return Details{}, err
	}
	d := Details{ID: out.ID, Name: strings.TrimPrefix(out.Name, "/"), Image: out.Config.Image,
		Labels: out.Config.Labels, State: out.State, Ports: map[string]int{}}
	for port, bindings := range out.NetworkSettings.Ports {
		for _, b := range bindings {
			if n, err := strconv.Atoi(b.HostPort); err == nil && n > 0 {
				d.Ports[port] = n
				break
			}
		}
	}
	return d, nil
}

// Image is a summary of one local image.
type Image struct {
	ID       string            `json:"Id"`
	RepoTags []string          `json:"RepoTags"`
	Created  int64             `json:"Created"`
	Labels   map[string]string `json:"Labels"`
}

// Images lists the images built for app (labelled piway.app=app), newest
// first.
func (c *Client) Images(ctx context.Context, app string) ([]Image, error) {
	filters, _ := json.Marshal(map[string][]string{"label": {LabelApp + "=" + app}})
	var out []Image
	err := c.do(ctx, http.MethodGet, "/images/json", url.Values{"filters": {string(filters)}}, nil, &out)
	slices.SortFunc(out, func(a, b Image) int { return cmp.Compare(b.Created, a.Created) })
	return out, err
}

// ImageExists reports whether ref (name:tag) is present locally.
func (c *Client) ImageExists(ctx context.Context, ref string) (bool, error) {
	err := c.do(ctx, http.MethodGet, "/images/"+url.PathEscape(ref)+"/json", nil, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// RemoveImage deletes an image by id or reference. Images still used by a
// container are refused with ErrConflict.
func (c *Client) RemoveImage(ctx context.Context, ref string) error {
	return c.do(ctx, http.MethodDelete, "/images/"+url.PathEscape(ref), nil, nil, nil)
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

// Logs returns the last lines lines of what container id wrote to stdout
// and stderr, interleaved as written, with RFC 3339 timestamps.
func (c *Client) Logs(ctx context.Context, id string, lines int) (string, error) {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}, "tail": {strconv.Itoa(lines)}}
	resp, err := c.request(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/logs", q, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return demux(io.LimitReader(resp.Body, 8<<20))
}

// demux reads Docker's multiplexed log stream (containers without a TTY):
// frames of an 8-byte header (stream type, 3 zero bytes, big-endian
// length) and the payload.
func demux(r io.Reader) (string, error) {
	var out strings.Builder
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return out.String(), nil
			}
			return out.String(), fmt.Errorf("docker: logs: %w", err)
		}
		n := binary.BigEndian.Uint32(hdr[4:])
		if _, err := io.CopyN(&out, r, int64(n)); err != nil {
			return out.String(), fmt.Errorf("docker: logs: %w", err)
		}
	}
}

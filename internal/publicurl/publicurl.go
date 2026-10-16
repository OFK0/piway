// Package publicurl finds the address the internet reaches piway at, for
// telling GitHub where to send webhooks.
//
// The dashboard is usually opened at a tailnet-only address, which GitHub
// cannot reach, so the address in the browser is not the one to give it
// (#140). With Tailscale Funnel, `tailscale funnel status --json` says which
// host and port are public and what each path is proxied to; that is
// enough to find the public URL of piway's webhook endpoint. Anything else
// (a Cloudflare Tunnel, a reverse proxy) is configured as webhook_url.
package publicurl

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Sources of a URL.
const (
	FromConfig = "config"
	FromFunnel = "tailscale funnel"
)

// funnelStatus is the part of `tailscale funnel status --json` used here.
type funnelStatus struct {
	Web map[string]struct {
		Handlers map[string]struct {
			Proxy string `json:"Proxy"`
		} `json:"Handlers"`
	} `json:"Web"`
	AllowFunnel map[string]bool `json:"AllowFunnel"`
}

// Funnel returns the public https URL at which a Funnel-enabled host
// forwards path, unchanged, to piway listening on port; "" if none does.
// A handler mounted at P forwards P/rest to its proxy target's path plus
// rest, as Tailscale Serve does.
func Funnel(status []byte, port int, path string) string {
	var st funnelStatus
	if json.Unmarshal(status, &st) != nil {
		return ""
	}
	var found []string
	for hostport, public := range st.AllowFunnel {
		if !public {
			continue
		}
		for mount, h := range st.Web[hostport].Handlers {
			if !forwards(mount, h.Proxy, port, path) {
				continue
			}
			host := strings.TrimSuffix(hostport, ":443")
			found = append(found, "https://"+host+path)
		}
	}
	slices.Sort(found) // stable when several match
	if len(found) == 0 {
		return ""
	}
	return found[0]
}

// forwards reports whether a handler mounted at mount, proxying to
// target, sends path to piway's port as path.
func forwards(mount, target string, port int, path string) bool {
	base := strings.TrimSuffix(mount, "/")
	if base != "" && path != base && !strings.HasPrefix(path, base+"/") {
		return false
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "http" {
		return false
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
	default:
		return false
	}
	p := u.Port()
	if p == "" {
		p = "80"
	}
	if p != strconv.Itoa(port) {
		return false
	}
	return strings.TrimSuffix(u.Path, "/")+strings.TrimPrefix(path, base) == path
}

// Finder answers the webhook's public URL: the configured one, else the
// one Tailscale Funnel publishes, asked at most once a minute.
type Finder struct {
	Configured string // webhook_url; wins when set
	Addr       string // piway's listen address, such as :8080
	Path       string // the webhook endpoint's path
	run        func(ctx context.Context) ([]byte, error)

	mu     sync.Mutex
	cached string
	at     time.Time
}

const ttl = time.Minute

// URL returns the public URL and where it came from; "" and "" when
// piway knows of none.
func (f *Finder) URL(ctx context.Context) (string, string) {
	if f.Configured != "" {
		return f.Configured, FromConfig
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if time.Since(f.at) < ttl {
		return f.cached, source(f.cached)
	}
	run := f.run
	if run == nil {
		run = tailscaleStatus
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := run(ctx)
	f.cached, f.at = "", time.Now()
	if err == nil {
		f.cached = Funnel(out, port(f.Addr), f.Path)
	}
	return f.cached, source(f.cached)
}

func source(u string) string {
	if u == "" {
		return ""
	}
	return FromFunnel
}

func tailscaleStatus(ctx context.Context) ([]byte, error) {
	if _, err := exec.LookPath("tailscale"); err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, "tailscale", "funnel", "status", "--json").Output()
}

// port is the port of a listen address such as ":8080"; 8080 when it has
// none.
func port(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if n, perr := strconv.Atoi(p); err == nil && perr == nil {
		return n
	}
	return 8080
}

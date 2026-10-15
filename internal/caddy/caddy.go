// Package caddy publishes each app at <name>.<domain> through a Caddy
// server, configured over Caddy's admin API.
//
// piway owns one HTTP server in Caddy's config, named "piway", and leaves
// the rest of the config alone. Every sync reads the whole config, replaces
// that server with one route per app (host <name>.<domain> proxied to the
// app's stable 127.0.0.1 port, see internal/router) and loads the result
// only if it changed. Syncing the full set rather than adding and deleting
// single routes means a missed update, or a Caddy restart that dropped the
// config, heals on the next sync.
//
// Certificates are Caddy's business: names that cannot get a public
// certificate (.local, .internal, .home.arpa) get one from Caddy's local
// CA, public names get one over ACME.
package caddy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ServerName is the key of piway's server in Caddy's apps.http.servers.
const ServerName = "piway"

// Options configure a Proxy.
type Options struct {
	Admin  string // Caddy's admin endpoint, such as "localhost:2019"
	Domain string // apps are served at <name>.<Domain>
	Listen string // the address Caddy serves apps on, such as ":8443"
	TLS    bool   // serve HTTPS; false serves plain HTTP
	Logger *slog.Logger
	Client *http.Client // default: 10 second timeout
}

// Proxy keeps Caddy's routes in line with piway's apps.
type Proxy struct {
	opts  Options
	admin string // base URL of the admin API
}

// New returns a Proxy. It does not contact Caddy.
func New(opts Options) (*Proxy, error) {
	if opts.Domain == "" {
		return nil, errors.New("caddy: no domain")
	}
	if _, _, err := net.SplitHostPort(opts.Listen); err != nil {
		return nil, fmt.Errorf("caddy: listen address: %w", err)
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 10 * time.Second}
	}
	admin := opts.Admin
	if !strings.Contains(admin, "://") {
		admin = "http://" + admin
	}
	return &Proxy{opts: opts, admin: strings.TrimSuffix(admin, "/")}, nil
}

// Host is the name app is served at.
func (p *Proxy) Host(app string) string { return app + "." + p.opts.Domain }

// URL is where a browser reaches app, with the port left out when it is
// the scheme's default.
func (p *Proxy) URL(app string) string {
	scheme, def := "http", "80"
	if p.opts.TLS {
		scheme, def = "https", "443"
	}
	_, port, _ := net.SplitHostPort(p.opts.Listen)
	host := p.Host(app)
	if port != def {
		host = net.JoinHostPort(host, port)
	}
	return scheme + "://" + host
}

// Sync makes piway's server in Caddy route exactly apps, a map of app name
// to stable port. It reloads Caddy only when something changed.
func (p *Proxy) Sync(ctx context.Context, apps map[string]int) error {
	raw, err := p.do(ctx, http.MethodGet, "/config/", nil)
	if err != nil {
		return err
	}
	// Decode twice: one copy to compare against, one to change.
	var cur, next map[string]any
	if err := json.Unmarshal(raw, &cur); err != nil {
		return fmt.Errorf("caddy: config: %w", err)
	}
	_ = json.Unmarshal(raw, &next)
	if next == nil {
		next = map[string]any{}
	}
	// Round-trip what we add, so it compares equal to what Caddy returns.
	b, err := json.Marshal(p.server(apps))
	if err != nil {
		return err
	}
	var srv any
	_ = json.Unmarshal(b, &srv)
	child(child(child(next, "apps"), "http"), "servers")[ServerName] = srv
	if p.opts.TLS {
		// Without root, installing the local CA into the system trust
		// store fails (or waits on a sudo prompt). Clients trust it by hand
		// instead: docs/PROXY.md.
		ca := child(child(child(next, "apps"), "pki"), "certificate_authorities")
		if _, set := ca["local"]; !set {
			ca["local"] = map[string]any{"install_trust": false}
		}
	}
	if reflect.DeepEqual(cur, next) {
		return nil
	}
	body, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if _, err := p.do(ctx, http.MethodPost, "/load", body); err != nil {
		return err
	}
	p.opts.Logger.Info("caddy routes updated", "apps", len(apps))
	return nil
}

// child returns m[key] as an object, creating it if it is missing.
func child(m map[string]any, key string) map[string]any {
	if c, ok := m[key].(map[string]any); ok {
		return c
	}
	c := map[string]any{}
	m[key] = c
	return c
}

type server struct {
	Listen         []string       `json:"listen"`
	Routes         []route        `json:"routes"`
	AutomaticHTTPS map[string]any `json:"automatic_https"`
}

type route struct {
	ID       string           `json:"@id"`
	Match    []match          `json:"match,omitempty"`
	Handle   []map[string]any `json:"handle"`
	Terminal bool             `json:"terminal"`
}

type match struct {
	Host []string `json:"host"`
}

func (p *Proxy) server(apps map[string]int) server {
	names := make([]string, 0, len(apps))
	for n := range apps {
		names = append(names, n)
	}
	slices.Sort(names) // a stable order, so an unchanged set compares equal
	s := server{Listen: []string{p.opts.Listen}, Routes: []route{}}
	if p.opts.TLS {
		// Redirects would need port 80, which a user cannot bind.
		s.AutomaticHTTPS = map[string]any{"disable_redirects": true}
	} else {
		s.AutomaticHTTPS = map[string]any{"disable": true}
	}
	for _, n := range names {
		s.Routes = append(s.Routes, route{
			ID:    "piway-" + n,
			Match: []match{{Host: []string{p.Host(n)}}},
			Handle: []map[string]any{{
				"handler":   "reverse_proxy",
				"upstreams": []map[string]any{{"dial": net.JoinHostPort("127.0.0.1", strconv.Itoa(apps[n]))}},
			}},
			Terminal: true,
		})
	}
	// Any other name on this server: say so, rather than an empty 200.
	s.Routes = append(s.Routes, route{
		ID: "piway-unknown",
		Handle: []map[string]any{{
			"handler":     "static_response",
			"status_code": http.StatusNotFound,
			"body":        "No app is served at this name.\n",
		}},
		Terminal: true,
	})
	return s
}

func (p *Proxy) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.admin+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.opts.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("caddy: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("caddy: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return nil, fmt.Errorf("caddy: %s %s: %s", method, path, e.Error)
		}
		return nil, fmt.Errorf("caddy: %s %s: %s", method, path, resp.Status)
	}
	return b, nil
}

// Run syncs apps() when changed is signalled and every interval, so routes
// come back after Caddy restarts. It logs when syncing starts failing and
// when it recovers, not on every attempt.
func (p *Proxy) Run(ctx context.Context, changed <-chan struct{}, apps func() map[string]int, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	failing := false
	for {
		err := p.Sync(ctx, apps())
		switch {
		case ctx.Err() != nil:
			return
		case err != nil && !failing:
			p.opts.Logger.Warn("caddy unreachable; apps are not published", "admin", p.opts.Admin, "err", err, "retry", interval)
		case err == nil && failing:
			p.opts.Logger.Info("caddy reachable again", "admin", p.opts.Admin)
		}
		failing = err != nil
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-t.C:
		}
	}
}

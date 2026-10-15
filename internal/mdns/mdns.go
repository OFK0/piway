// Package mdns makes app names resolve on the LAN: it publishes each
// <name>.<domain> under .local as an mDNS alias of the Pi, through the avahi
// daemon that Raspberry Pi OS already runs.
//
// Each name is a CNAME record pointing at the Pi's own mDNS name (such as
// raspberrypi.local), so avahi keeps answering with whatever addresses the
// Pi has on each interface, and a changed address needs nothing from piway.
// Every name gets its own avahi entry group, published as unique: avahi
// probes the LAN first, and a name another device already uses fails alone
// (reported as a collision) instead of taking the others down with it.
package mdns

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"
)

// Avahi is the part of the avahi daemon a Publisher uses.
type Avahi interface {
	// HostName is the Pi's own mDNS name, such as "raspberrypi.local".
	HostName() (string, error)
	// Alias publishes name as a CNAME of target in a new entry group.
	Alias(name, target string) (Group, error)
}

// Group is one published entry group.
type Group interface {
	State() (State, error)
	Free() error
}

// State is an avahi entry group's state.
type State int32

// The states of avahi's AvahiEntryGroupState.
const (
	Uncommitted State = iota
	Registering
	Established
	Collision
	Failure
)

func (s State) String() string {
	switch s {
	case Uncommitted:
		return "uncommitted"
	case Registering:
		return "registering"
	case Established:
		return "established"
	case Collision:
		return "collision"
	case Failure:
		return "failure"
	}
	return fmt.Sprintf("state %d", int32(s))
}

// Publishes reports whether names under domain can be published over mDNS:
// only .local names are resolved by it.
func Publishes(domain string) bool {
	return domain == "local" || strings.HasSuffix(domain, ".local")
}

// Publisher keeps one alias per app published.
type Publisher struct {
	avahi  Avahi
	domain string
	log    *slog.Logger

	host    string            // the CNAME target of every group
	groups  map[string]Group  // by app
	reports map[string]string // by app: the last problem logged
}

// New returns a Publisher that publishes <app>.<domain>.
func New(avahi Avahi, domain string, log *slog.Logger) *Publisher {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Publisher{avahi: avahi, domain: domain, log: log, groups: map[string]Group{}, reports: map[string]string{}}
}

// Sync publishes exactly apps. A group that avahi dropped (it restarted)
// or that points at an old host name (the Pi was renamed after a
// collision) is published again. Per-name collisions are logged, not
// returned: they need a different app name or domain, not a retry.
func (p *Publisher) Sync(apps []string) error {
	host, err := p.avahi.HostName()
	if err != nil {
		return err
	}
	if host != p.host && p.host != "" {
		p.log.Info("mdns host name changed; republishing", "from", p.host, "to", host)
		p.freeAll()
	}
	p.host = host

	want := map[string]bool{}
	for _, a := range apps {
		want[a] = true
	}
	for app, g := range p.groups {
		if !want[app] {
			p.free(app, g)
		}
	}
	var errs []error
	for _, app := range slices.Sorted(maps.Keys(want)) {
		if err := p.ensure(app); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errs[0] // the rest are almost always the same daemon problem
	}
	return nil
}

func (p *Publisher) name(app string) string { return app + "." + p.domain }

// ensure publishes app if it is not, and reports a collision once.
func (p *Publisher) ensure(app string) error {
	if strings.EqualFold(p.name(app), strings.TrimSuffix(p.host, ".")) {
		// A CNAME of itself would loop, and the Pi's own name is taken.
		p.report(app, "host", "mdns name not published", "name", p.name(app),
			"reason", "it is the Pi's own name; rename the app or change proxy_domain")
		return nil
	}
	if g, ok := p.groups[app]; ok {
		st, err := g.State()
		switch {
		case err != nil:
			// avahi restarted, and the group went with it.
			delete(p.groups, app)
		case st == Collision || st == Failure:
			p.report(app, st.String(), "mdns name not published", "name", p.name(app), "state", st,
				"hint", "another device on the LAN answers to this name")
			return nil
		default:
			p.report(app, "", "")
			return nil
		}
	}
	g, err := p.avahi.Alias(p.name(app), p.host)
	if err != nil {
		return fmt.Errorf("mdns: publish %s: %w", p.name(app), err)
	}
	p.groups[app] = g
	p.log.Info("mdns name published", "name", p.name(app), "alias_of", p.host)
	return nil
}

// report logs msg once until the problem for app changes; problem "" clears it.
func (p *Publisher) report(app, problem, msg string, args ...any) {
	if p.reports[app] == problem {
		return
	}
	p.reports[app] = problem
	if problem != "" {
		p.log.Warn(msg, args...)
	}
}

func (p *Publisher) free(app string, g Group) {
	if err := g.Free(); err != nil {
		p.log.Debug("free mdns group", "name", p.name(app), "err", err)
	}
	delete(p.groups, app)
	delete(p.reports, app)
}

func (p *Publisher) freeAll() {
	for app, g := range p.groups {
		p.free(app, g)
	}
}

// Close withdraws every name.
func (p *Publisher) Close() { p.freeAll() }

// Run syncs apps() when changed is signalled and every interval, and
// withdraws every name when ctx ends. Like caddy.Proxy.Run, it logs when
// syncing starts failing and when it recovers, not every attempt.
func (p *Publisher) Run(ctx context.Context, changed <-chan struct{}, apps func() []string, interval time.Duration) {
	defer p.Close()
	t := time.NewTicker(interval)
	defer t.Stop()
	failing := false
	for {
		err := p.Sync(apps())
		switch {
		case err != nil && !failing:
			p.log.Warn("avahi unreachable; app names are not published on the LAN", "err", err, "retry", interval)
		case err == nil && failing:
			p.log.Info("avahi reachable again")
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

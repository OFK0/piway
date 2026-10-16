package setup

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ofk0/piway/internal/config"
	"github.com/ofk0/piway/internal/docker"
	"github.com/ofk0/piway/internal/gitauth"
	"github.com/ofk0/piway/internal/webhook"
)

// Env is what the checks need to know about this installation.
type Env struct {
	Config     config.Config // effective settings; defaults if the file is broken
	ConfigFile string        // "" if there is none
	ConfigErr  error         // why the config could not be loaded
	User       string
	Home       string
	// Repos are the repositories in apps.yaml, and TestRepo checks that
	// piway can read a private one (gitauth.Access.Test). Without
	// TestRepo, repository access is not checked.
	Repos    []webhook.Target
	ReposErr error // why apps.yaml could not be read
	TestRepo func(ctx context.Context, src gitauth.Source) error
	// WebhookURL is the public address of the webhook endpoint ("" when
	// piway knows none) and Hooks lists the payload URLs of a
	// repository's webhooks on GitHub. Without Hooks the webhooks are
	// not checked.
	WebhookURL func(ctx context.Context) string
	Hooks      func(ctx context.Context, repo string) ([]string, error)
	// GitLabWebhookURL is the same for GitLab's endpoint. GitLab's
	// webhooks cannot be read without a token, so only the address is
	// checked.
	GitLabWebhookURL func(ctx context.Context) string
}

// Supported Raspberry Pi OS (Debian) releases.
var supportedCodenames = []string{"bookworm", "trixie"}

const (
	minFree  = 1 << 30 // below this, builds and history fail
	warnFree = 4 << 30
)

// Doctor checks the installation and returns one result per item, in a
// stable order: system, piway, Docker, the app proxy, remote access,
// private repositories.
func Doctor(ctx context.Context, h Host, env Env) *Report {
	c := &checker{ctx: ctx, h: h, env: env}
	c.system()
	c.piway()
	c.docker()
	c.proxy()
	c.remote()
	c.repos()
	c.webhooks()
	c.gitlabWebhooks()
	return &Report{Results: c.results}
}

type checker struct {
	ctx     context.Context
	h       Host
	env     Env
	section string
	results []Result
}

func (c *checker) add(id, title string, s Status, detail, fix string) {
	c.results = append(c.results, Result{Section: c.section, ID: id, Title: title, Status: s, Detail: detail, Fix: fix})
}

func (c *checker) run(name string, args ...string) (string, error) {
	return c.h.Run(c.ctx, name, args...)
}

// unit is the path of a systemd user unit.
// startsAfter reports whether a systemd unit orders itself after dep.
func startsAfter(unit []byte, dep string) bool {
	for line := range strings.SplitSeq(string(unit), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && strings.TrimSpace(k) == "After" && slices.Contains(strings.Fields(v), dep) {
			return true
		}
	}
	return false
}

func (c *checker) unit(name string) string {
	return filepath.Join(c.env.Home, ".config", "systemd", "user", name+".service")
}

func (c *checker) userActive(unit string) bool {
	out, _ := c.run("systemctl", "--user", "is-active", unit)
	return out == "active"
}

func (c *checker) userEnabled(unit string) bool {
	out, _ := c.run("systemctl", "--user", "is-enabled", unit)
	return out == "enabled"
}

func (c *checker) system() {
	c.section = "System"

	arch := c.h.Arch()
	osName, codename := c.osRelease()
	switch {
	case arch != "arm64":
		c.add("platform", "Platform", Warn, fmt.Sprintf("%s on %s; piway is built for a Raspberry Pi running 64-bit Raspberry Pi OS", osName, arch), "")
	case !slices.Contains(supportedCodenames, codename):
		c.add("platform", "Platform", Warn, fmt.Sprintf("%s, arm64; tested on Raspberry Pi OS based on Debian %s", osName, strings.Join(supportedCodenames, " or ")), "")
	default:
		c.add("platform", "Platform", OK, osName+", arm64", "")
	}

	switch out, err := c.run("timedatectl", "show", "--property=NTPSynchronized", "--value"); {
	case err != nil:
		c.add("clock", "Clock", Warn, "cannot tell whether the clock is synchronised (timedatectl)", "")
	case out == "yes":
		c.add("clock", "Clock", OK, "synchronised over NTP", "")
	default:
		// A Pi has no battery-backed clock: until NTP syncs, times in
		// metrics, logs and TLS checks are wrong.
		c.add("clock", "Clock", Warn, "not synchronised; a Pi has no battery-backed clock", "sudo timedatectl set-ntp true")
	}

	dir := c.env.Config.DataDir
	if dir == "" {
		dir = c.env.Home
	}
	for dir != "/" && dir != "." {
		if _, err := c.h.Stat(dir); err == nil {
			break
		}
		dir = filepath.Dir(dir) // not created yet: check the disk it will be on
	}
	switch free, err := c.h.DiskFree(dir); {
	case err != nil:
		c.add("disk", "Disk space", Warn, "cannot read free space: "+err.Error(), "")
	case free < minFree:
		c.add("disk", "Disk space", Fail, fmt.Sprintf("%s free on %s; builds and history need at least 1 GiB", gib(free), dir), "docker system prune   # removes unused images and build cache")
	case free < warnFree:
		c.add("disk", "Disk space", Warn, fmt.Sprintf("%s free on %s", gib(free), dir), "docker system prune   # removes unused images and build cache")
	default:
		c.add("disk", "Disk space", OK, fmt.Sprintf("%s free on %s", gib(free), dir), "")
	}
}

func (c *checker) osRelease() (name, codename string) {
	b, err := c.h.ReadFile("/etc/os-release")
	if err != nil {
		return "unknown OS", ""
	}
	fields := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if ok {
			fields[k] = strings.Trim(v, `"'`)
		}
	}
	name = fields["PRETTY_NAME"]
	if name == "" {
		name = "unknown OS"
	}
	return name, fields["VERSION_CODENAME"]
}

func (c *checker) piway() {
	c.section = "piway"
	cfg := c.env.Config

	if path, err := c.h.LookPath("piway"); err == nil {
		c.add("binary", "Binary", OK, path, "")
	} else if local := filepath.Join(c.env.Home, ".local", "bin", "piway"); c.exists(local) {
		c.add("binary", "Binary", Warn, local+" is not on your PATH", `echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.profile`)
	} else {
		c.add("binary", "Binary", Fail, "piway is not installed in ~/.local/bin", "curl -fsSL https://raw.githubusercontent.com/OFK0/piway/main/install.sh | sh")
	}

	switch {
	case c.env.ConfigErr != nil:
		c.add("config", "Config", Fail, c.env.ConfigErr.Error(), "fix the file, then: piway -print-config")
	case c.env.ConfigFile == "":
		c.add("config", "Config", Warn, "no config file; using the defaults", "piway setup   # writes ~/.config/piway/config.yaml")
	default:
		c.add("config", "Config", OK, c.env.ConfigFile, "")
	}

	if c.exists(cfg.PasswordFile) {
		c.add("password", "Password", OK, "set", "")
	} else {
		c.add("password", "Password", Warn, "not set; only the Pi itself can open the dashboard, and Tailscale is refused", "piway -set-password")
	}

	switch {
	case !c.exists(c.unit("piway")):
		c.add("service", "Service", Fail, "no user service at "+c.unit("piway"), "piway setup   # or the steps in docs/RELEASING.md")
	case !c.userEnabled("piway"):
		c.add("service", "Service", Fail, "installed but not enabled; piway will not start at boot", "systemctl --user enable --now piway")
	case !c.userActive("piway"):
		c.add("service", "Service", Fail, "enabled but not running", "systemctl --user restart piway\njournalctl --user -u piway -n 50   # why it stopped")
	default:
		c.add("service", "Service", OK, "enabled and running", "")
	}
	// Units installed before piway waited for Docker at boot (#104): it
	// then starts first, finds no daemon and keeps deploys off.
	if b, err := c.h.ReadFile(c.unit("piway")); err == nil && !startsAfter(b, "docker.service") {
		c.add("unit", "Boot order", Warn, "the service does not wait for Docker at boot, so deploys can stay off after a reboot", "piway setup   # installs the current unit")
	}

	switch out, err := c.run("loginctl", "show-user", c.env.User, "--property=Linger", "--value"); {
	case err == nil && out == "yes":
		c.add("linger", "Lingering", OK, "user services run without a login and start at boot", "")
	default:
		c.add("linger", "Lingering", Fail, "off; piway and Docker stop when you log out and do not start at boot", "sudo loginctl enable-linger "+c.env.User)
	}

	url := "http://" + loopback(cfg.Addr) + "/healthz"
	switch status, err := c.h.HTTPStatus(c.ctx, url); {
	case err != nil:
		c.add("http", "Responding", Fail, "no answer at "+url, "journalctl --user -u piway -n 50")
	case status != 200:
		c.add("http", "Responding", Fail, fmt.Sprintf("%s answered %d", url, status), "journalctl --user -u piway -n 50")
	default:
		c.add("http", "Responding", OK, url, "")
	}
}

func (c *checker) docker() {
	c.section = "Docker (apps)"
	const guide = "docs/DOCKER.md, or: piway setup"

	if _, err := c.h.LookPath("docker"); err != nil {
		c.add("docker", "Docker", Fail, "not installed; apps cannot be built or run", guide)
		return
	}
	c.add("docker", "Docker", OK, "installed", "")

	if c.idMapped("/etc/subuid") && c.idMapped("/etc/subgid") {
		c.add("subids", "Sub-IDs", OK, "rootless containers can map user IDs", "")
	} else {
		u := c.env.User
		c.add("subids", "Sub-IDs", Fail, "no /etc/subuid and /etc/subgid range for "+u+"; rootless Docker cannot start",
			fmt.Sprintf("sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 %s", u))
	}

	// piway uses rootless Docker; a root daemon next to it is a second,
	// more powerful way in (docs/DOCKER.md, HARDENING.md).
	if out, _ := c.run("systemctl", "is-active", "docker.service"); out == "active" {
		c.add("root-daemon", "Root daemon", Warn, "the root Docker daemon is running; piway uses rootless Docker", "sudo systemctl disable --now docker.service docker.socket")
	}

	socket, err := docker.SocketPath(c.h.Getenv)
	if err != nil {
		c.add("daemon", "Daemon", Fail, err.Error(), "unset DOCKER_HOST")
		return
	}
	info, err := c.h.DockerInfo(c.ctx, socket)
	if err != nil {
		c.add("daemon", "Daemon", Fail, "no rootless daemon at "+socket, "systemctl --user enable --now docker\ndockerd-rootless-setuptool.sh check   # if it does not start")
		c.add("memory-limit", "Memory limits", Skip, "needs a running daemon", "")
		return
	}
	c.add("daemon", "Daemon", OK, "reachable at "+socket, "")
	if info.MemoryLimit {
		c.add("memory-limit", "Memory limits", OK, "enforced (memory cgroup on)", "")
	} else {
		c.add("memory-limit", "Memory limits", Warn, "not enforced: the memory cgroup is off, so run.memory in piway.yaml has no effect",
			"sudo sed -i '1 s/$/ cgroup_enable=memory/' /boot/firmware/cmdline.txt\nsudo reboot")
	}
}

// idMapped reports whether file has a range for the user.
func (c *checker) idMapped(file string) bool {
	b, err := c.h.ReadFile(file)
	if err != nil {
		return false
	}
	for line := range strings.Lines(string(b)) {
		if name, _, ok := strings.Cut(line, ":"); ok && name == c.env.User {
			return true
		}
	}
	return false
}

func (c *checker) proxy() {
	c.section = "App proxy"
	cfg := c.env.Config
	if cfg.ProxyDomain == "" {
		c.add("proxy", "Proxy", Skip, "proxy_domain is not set; apps are reachable on their 127.0.0.1 ports only", "")
		return
	}

	if _, err := c.h.LookPath("caddy"); err == nil || c.exists(filepath.Join(c.env.Home, ".local", "bin", "caddy")) {
		c.add("caddy", "Caddy", OK, "installed", "")
	} else {
		c.add("caddy", "Caddy", Fail, "not installed; apps are not published at <name>."+cfg.ProxyDomain, "docs/PROXY.md, or: piway setup")
	}

	switch {
	case !c.exists(c.unit("caddy")):
		c.add("caddy-service", "Caddy service", Fail, "no user service at "+c.unit("caddy"), "piway setup   # or the steps in docs/PROXY.md")
	case !c.userActive("caddy"):
		c.add("caddy-service", "Caddy service", Fail, "not running", "systemctl --user enable --now caddy\njournalctl --user -u caddy -n 50")
	default:
		c.add("caddy-service", "Caddy service", OK, "running", "")
	}

	admin := "http://" + cfg.CaddyAdmin + "/config/"
	if status, err := c.h.HTTPStatus(c.ctx, admin); err == nil && status == 200 {
		c.add("caddy-admin", "Caddy admin API", OK, cfg.CaddyAdmin, "")
	} else {
		c.add("caddy-admin", "Caddy admin API", Fail, "no answer at "+admin+"; piway cannot publish apps", "systemctl --user restart caddy")
	}

	if port := portOf(cfg.ProxyListen); port > 0 && port < 1024 {
		start := 1024
		if b, err := c.h.ReadFile("/proc/sys/net/ipv4/ip_unprivileged_port_start"); err == nil {
			start, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		if start <= port {
			c.add("low-port", "Port "+strconv.Itoa(port), OK, "Caddy may listen on it without root", "")
		} else {
			c.add("low-port", "Port "+strconv.Itoa(port), Fail, fmt.Sprintf("only root may listen below %d", start),
				fmt.Sprintf("echo 'net.ipv4.ip_unprivileged_port_start=%d' | sudo tee /etc/sysctl.d/50-unprivileged-ports.conf\nsudo sysctl --system", port))
		}
	}

	if cfg.ProxyDomain == "local" || strings.HasSuffix(cfg.ProxyDomain, ".local") {
		if out, _ := c.run("systemctl", "is-active", "avahi-daemon"); out == "active" {
			c.add("avahi", "mDNS", OK, "avahi publishes <name>."+cfg.ProxyDomain+" on the LAN", "")
		} else {
			c.add("avahi", "mDNS", Fail, "avahi-daemon is not running; .local names will not resolve", "sudo apt install avahi-daemon\nsudo systemctl enable --now avahi-daemon")
		}
	}
}

func (c *checker) remote() {
	c.section = "Remote access"
	if _, err := c.h.LookPath("tailscale"); err != nil {
		c.add("tailscale", "Tailscale", Skip, "not installed (optional; docs/REMOTE.md)", "")
		return
	}
	out, err := c.run("tailscale", "serve", "status")
	port := strconv.Itoa(portOf(c.env.Config.Addr))
	switch {
	case err != nil:
		c.add("tailscale", "Tailscale", Warn, "installed, but `tailscale serve status` failed: "+firstLine(out), "sudo tailscale up\nsudo tailscale set --operator="+c.env.User)
	case !strings.Contains(out, ":"+port):
		c.add("tailscale", "Tailscale", Warn, "installed, but not serving piway", "tailscale serve --bg "+port)
	case !c.exists(c.env.Config.PasswordFile):
		c.add("tailscale", "Tailscale", Fail, "serves piway, but without a password piway refuses those requests", "piway -set-password")
	default:
		c.add("tailscale", "Tailscale", OK, "serves piway over HTTPS", "")
	}
}

// repos checks that each private repository in apps.yaml can be read.
// Public repositories need nothing, and with none private there is no
// section.
func (c *checker) repos() {
	c.section = "Private repositories"
	if c.env.ReposErr != nil {
		c.add("repos", "apps.yaml", Fail, c.env.ReposErr.Error(), "fix the file, or list repositories with: piway repo list")
		return
	}
	if c.env.TestRepo == nil {
		return
	}
	seen := map[string]bool{}
	for _, t := range c.env.Repos {
		key := strings.ToLower(t.Repo)
		if t.Auth == gitauth.Public || seen[key] {
			continue
		}
		seen[key] = true
		id := "repo:" + key
		err := c.env.TestRepo(c.ctx, t.Source(""))
		if err == nil {
			c.add(id, t.Repo, OK, "readable with its "+authLabel(t.Auth), "")
			continue
		}
		fix := "piway repo test " + t.Repo
		var e *gitauth.Error
		if errors.As(err, &e) {
			switch e.Kind {
			case gitauth.NoKey, gitauth.KeyRejected:
				fix = "piway repo key " + t.Repo + "\n" + fix
			case gitauth.NoToken, gitauth.TokenRejected:
				fix = "piway repo token set\n" + fix
			}
		}
		c.add(id, t.Repo, Fail, err.Error(), fix)
	}
}

// webhooks checks that each listed repository's webhook on GitHub points
// at the address GitHub reaches piway at. The address the dashboard is
// opened at is often tailnet-only, so a webhook copied from it fails with
// EOF (#140).
func (c *checker) webhooks() {
	if c.env.Hooks == nil || c.env.ReposErr != nil || len(c.env.Repos) == 0 {
		return
	}
	c.section = "GitHub webhooks"
	want := ""
	if c.env.WebhookURL != nil {
		want = c.env.WebhookURL(c.ctx)
	}
	if want == "" {
		c.add("webhook-url", "Public address", Warn, "piway knows no address GitHub can reach, so a webhook cannot deliver",
			"publish the webhook with Tailscale Funnel (docs/REMOTE.md), or set webhook_url")
		return
	}
	c.add("webhook-url", "Public address", OK, want, "")
	seen := map[string]bool{}
	for _, t := range c.env.Repos {
		key := strings.ToLower(t.Repo)
		if seen[key] || t.IsGitLab() {
			continue
		}
		seen[key] = true
		id := "webhook:" + key
		urls, err := c.env.Hooks(c.ctx, t.Repo)
		switch {
		case err != nil:
			c.add(id, t.Repo, Skip, "cannot read its webhooks: "+err.Error(), "")
		case slices.ContainsFunc(urls, func(u string) bool {
			return strings.EqualFold(strings.TrimSuffix(u, "/"), strings.TrimSuffix(want, "/"))
		}):
			c.add(id, t.Repo, OK, "its webhook points at the public address", "")
		case len(urls) == 0:
			c.add(id, t.Repo, Warn, "no webhook on GitHub, so pushes do not deploy",
				"add one: https://github.com/"+t.Repo+"/settings/hooks/new\nPayload URL "+want)
		default:
			c.add(id, t.Repo, Fail, "its webhook points at "+strings.Join(urls, ", ")+", not at "+want+", so GitHub cannot deliver (\"We couldn't deliver this payload\")",
				"change its Payload URL to "+want+": https://github.com/"+t.Repo+"/settings/hooks")
		}
	}
}

// gitlabWebhooks checks that piway knows an address GitLab can reach, when
// a GitLab project is listed. The project's own webhooks are not read.
func (c *checker) gitlabWebhooks() {
	if c.env.GitLabWebhookURL == nil || c.env.ReposErr != nil {
		return
	}
	found := false
	for _, t := range c.env.Repos {
		found = found || t.IsGitLab()
	}
	if !found {
		return
	}
	c.section = "GitLab webhooks"
	if want := c.env.GitLabWebhookURL(c.ctx); want != "" {
		c.add("gitlab-webhook-url", "Public address", OK, want+" (on each project: Settings > Webhooks, with the Secret token and the Push events trigger)", "")
		return
	}
	c.add("gitlab-webhook-url", "Public address", Warn, "piway knows no address GitLab can reach, so a webhook cannot deliver",
		"publish /api/v1/webhooks/gitlab with Tailscale Funnel (docs/REMOTE.md), or set webhook_url")
}

func authLabel(auth string) string {
	if auth == gitauth.Token {
		return "GitHub token"
	}
	return "deploy key"
}

func (c *checker) exists(path string) bool {
	if path == "" {
		return false
	}
	_, err := c.h.Stat(path)
	return err == nil
}

// loopback turns a listen address into one to dial: ":8080" and
// "0.0.0.0:8080" are reached at 127.0.0.1:8080.
func loopback(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func portOf(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}

func gib(b uint64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

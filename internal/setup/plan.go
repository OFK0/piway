package setup

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	files "github.com/ofk0/piway/deploy"
	"github.com/ofk0/piway/internal/config"
)

// Action is one thing a step does. Exactly one of Argv, Path or Func is set.
type Action struct {
	// Argv is a command; privileged ones start with "sudo", so the plan
	// shows exactly what runs as root.
	Argv  []string
	Stdin string // fed to the command, such as the file a `sudo tee` writes

	// Path is a file written as the user.
	Path    string
	Content []byte
	Mode    fs.FileMode

	// Func is work done in piway itself, described by Desc.
	Func func(ctx context.Context) error
	Desc string
}

// String is how the action appears in the plan.
func (a Action) String() string {
	switch {
	case a.Argv != nil:
		s := "$ " + shellJoin(a.Argv)
		if a.Stdin != "" {
			s += " <<EOF\n" + strings.TrimRight(a.Stdin, "\n") + "\nEOF"
		}
		return s
	case a.Path != "":
		return fmt.Sprintf("write %s (%d lines)", a.Path, bytes.Count(a.Content, []byte("\n")))
	default:
		return a.Desc
	}
}

// Step is a group of actions that installs or fixes one thing.
type Step struct {
	ID      string
	Title   string
	Why     string // what doctor found, or why the step is needed
	Actions []Action
	// Manual is what setup cannot do for you, printed after the step.
	Manual string
	// Reboot means the step takes effect after a reboot.
	Reboot bool
}

// Options are the answers to setup's questions.
type Options struct {
	// ProxyDomain publishes apps at <name>.<ProxyDomain> through Caddy; ""
	// leaves the proxy out.
	ProxyDomain string
	Docker      bool // install rootless Docker, needed to deploy apps
	Tailscale   bool // remote access (docs/REMOTE.md)
	// SetPassword asks for the dashboard password; nil skips the step.
	SetPassword func(ctx context.Context) error
}

// CaddyVersion is the Caddy release setup installs (docs/PROXY.md).
const CaddyVersion = "2.11.7"

// Plan works out what is missing, from the same checks as doctor, and
// returns the steps that install it, in the order they must run. A
// complete installation gives no steps, so setup can always run again.
func Plan(ctx context.Context, h Host, env Env, opt Options) ([]Step, error) {
	if env.ConfigFile == "" {
		// The config setup is about to write decides what is needed, such
		// as the proxy checks.
		env.Config.ProxyDomain = opt.ProxyDomain
	}
	r := Doctor(ctx, h, env)
	st := func(id string) Status {
		res, _ := r.Get(id)
		return res.Status
	}
	if h.Arch() != "arm64" {
		return nil, fmt.Errorf("this machine is %s; setup installs piway on a Raspberry Pi running 64-bit Raspberry Pi OS", h.Arch())
	}
	if env.ConfigErr != nil {
		return nil, fmt.Errorf("fix the config file first: %w", env.ConfigErr)
	}

	p := planner{ctx: ctx, h: h, env: env, opt: opt}
	home := env.Home
	bin := filepath.Join(home, ".local", "bin")
	unitDir := filepath.Join(home, ".config", "systemd", "user")

	// 1. The binary, where the service runs it from.
	if st("binary") != OK {
		s := Step{ID: "binary", Title: "Install piway in ~/.local/bin", Why: detail(r, "binary")}
		target := filepath.Join(bin, "piway")
		if exe, err := h.Executable(); err == nil && exe != target {
			s.Actions = append(s.Actions, Action{Argv: []string{"install", "-D", "-m", "755", exe, target}})
		}
		if !p.onPath(bin) {
			s.Actions = append(s.Actions, Action{Argv: []string{"sh", "-c", `echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.profile`}})
			s.Manual = "Open a new shell (or run `. ~/.profile`) so `piway` is on your PATH."
		}
		p.add(s)
	}

	// 2. Config, written once: an existing file is never overwritten.
	cfg := env.Config
	wroteConfig := false
	if env.ConfigFile == "" {
		p.add(Step{
			ID: "config", Title: "Write the config file", Why: "no config file yet",
			Actions: []Action{{Path: filepath.Join(home, ".config", "piway", "config.yaml"), Content: configFile(opt.ProxyDomain), Mode: 0o644}},
		})
		wroteConfig = true
	} else if opt.ProxyDomain != cfg.ProxyDomain {
		p.note("config", fmt.Sprintf("%s sets proxy_domain to %q; edit it to change that, then run setup again.", env.ConfigFile, cfg.ProxyDomain))
	}

	// 3. Dashboard password.
	if st("password") != OK && opt.SetPassword != nil {
		p.add(Step{ID: "password", Title: "Set the dashboard password", Why: detail(r, "password"),
			Actions: []Action{{Func: opt.SetPassword, Desc: "piway -set-password   # asks for it twice"}}})
	}

	// 4. Lingering: user services run at boot and without a login.
	if st("linger") != OK {
		p.add(Step{ID: "linger", Title: "Keep piway running without a login", Why: detail(r, "linger"),
			Actions: []Action{{Argv: []string{"sudo", "loginctl", "enable-linger", env.User}}}})
	}

	// 5. Rootless Docker (docs/DOCKER.md).
	if opt.Docker {
		p.docker(r)
	}

	// 6. Caddy and mDNS (docs/PROXY.md).
	if cfg.ProxyDomain != "" {
		p.proxy(r, cfg.ProxyDomain, cfg.ProxyListen, bin, unitDir)
	}

	// 7. The piway service, last, so Docker and Caddy are up when it starts.
	p.service(r, unitDir, wroteConfig)

	// 8. Remote access (docs/REMOTE.md).
	if opt.Tailscale {
		p.tailscale(r, cfg)
	}
	return p.steps, nil
}

type planner struct {
	ctx   context.Context
	h     Host
	env   Env
	opt   Options
	steps []Step
}

func (p *planner) add(s Step) { p.steps = append(p.steps, s) }

// note is a step with nothing to run, only something to tell.
func (p *planner) note(id, msg string) { p.add(Step{ID: id + "-note", Title: "Note", Manual: msg}) }

func (p *planner) onPath(dir string) bool {
	for _, d := range filepath.SplitList(p.h.Getenv("PATH")) {
		if d == dir {
			return true
		}
	}
	return false
}

func (p *planner) docker(r *Report) {
	st := func(id string) Status { res, _ := r.Get(id); return res.Status }
	installing := st("docker") != OK

	if installing {
		codename, arch := p.debianRelease()
		p.add(Step{
			ID: "docker", Title: "Install Docker from Docker's repository", Why: "Docker is not installed; apps are built and run in containers",
			Actions: []Action{
				{Argv: []string{"sudo", "apt-get", "update"}},
				{Argv: []string{"sudo", "apt-get", "install", "-y", "ca-certificates", "curl"}},
				{Argv: []string{"sudo", "install", "-m", "0755", "-d", "/etc/apt/keyrings"}},
				{Argv: []string{"sudo", "curl", "-fsSL", "https://download.docker.com/linux/debian/gpg", "-o", "/etc/apt/keyrings/docker.asc"}},
				{Argv: []string{"sudo", "chmod", "a+r", "/etc/apt/keyrings/docker.asc"}},
				{Argv: []string{"sudo", "tee", "/etc/apt/sources.list.d/docker.sources"}, Stdin: fmt.Sprintf(
					"Types: deb\nURIs: https://download.docker.com/linux/debian\nSuites: %s\nComponents: stable\nArchitectures: %s\nSigned-By: /etc/apt/keyrings/docker.asc\n", codename, arch)},
				{Argv: []string{"sudo", "apt-get", "update"}},
				{Argv: []string{"sudo", "apt-get", "install", "-y", "docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin",
					"docker-ce-rootless-extras", "uidmap", "slirp4netns", "dbus-user-session"}},
			},
		})
	}

	// The package starts a root daemon; piway uses rootless Docker only.
	if installing || st("root-daemon") == Warn {
		p.add(Step{ID: "root-daemon", Title: "Turn off the root Docker daemon", Why: "piway uses rootless Docker; the root daemon is a second way to become root",
			Actions: []Action{{Argv: []string{"sudo", "systemctl", "disable", "--now", "docker.service", "docker.socket"}}}})
	}

	// Read directly: doctor stops at a missing docker CLI before this check.
	c := &checker{h: p.h, env: p.env}
	if !c.idMapped("/etc/subuid") || !c.idMapped("/etc/subgid") {
		p.add(Step{ID: "subids", Title: "Give your user sub-IDs for rootless containers", Why: "no /etc/subuid and /etc/subgid range for " + p.env.User,
			Actions: []Action{{Argv: []string{"sudo", "usermod", "--add-subuids", "100000-165535", "--add-subgids", "100000-165535", p.env.User}}}})
	}

	if installing || st("daemon") != OK {
		s := Step{ID: "rootless", Title: "Start rootless Docker", Why: "no rootless daemon yet",
			Actions: []Action{
				{Argv: []string{"dockerd-rootless-setuptool.sh", "install"}},
				{Argv: []string{"systemctl", "--user", "enable", "--now", "docker"}},
			}}
		// The docker CLI in your own shells talks to the same daemon.
		if b, _ := p.h.ReadFile(filepath.Join(p.env.Home, ".bashrc")); !bytes.Contains(b, []byte("DOCKER_HOST=")) {
			s.Actions = append(s.Actions, Action{Argv: []string{"sh", "-c", `echo 'export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/docker.sock' >> ~/.bashrc`}})
		}
		p.add(s)
	}

	// Memory limits need the memory cgroup, off by default on a Pi.
	const cmdline = "/boot/firmware/cmdline.txt"
	b, err := p.h.ReadFile(cmdline)
	needCgroup := st("memory-limit") == Warn || (installing && err == nil)
	if needCgroup && err == nil && !bytes.Contains(b, []byte("cgroup_enable=memory")) {
		p.add(Step{ID: "memory-cgroup", Title: "Turn on memory limits for containers", Why: "the memory cgroup is off, so run.memory in piway.yaml would have no effect",
			Actions: []Action{{Argv: []string{"sudo", "sed", "-i", "1 s/$/ cgroup_enable=memory/", cmdline}}},
			Reboot:  true})
	}
}

// debianRelease returns the Debian codename and package architecture for
// Docker's apt source.
func (p *planner) debianRelease() (codename, arch string) {
	_, codename = (&checker{h: p.h}).osRelease()
	if codename == "" {
		codename = "trixie"
	}
	arch = "arm64"
	if out, err := p.h.Run(p.ctx, "dpkg", "--print-architecture"); err == nil && out != "" {
		arch = out
	}
	return codename, arch
}

func (p *planner) proxy(r *Report, domain, listen, bin, unitDir string) {
	st := func(id string) Status {
		res, ok := r.Get(id)
		if !ok {
			return Fail
		}
		return res.Status
	}

	if st("caddy") != OK {
		v := CaddyVersion
		script := strings.Join([]string{
			"set -e",
			`cd "$(mktemp -d)"`,
			"V=" + v,
			"curl -fsSLO https://github.com/caddyserver/caddy/releases/download/v$V/caddy_${V}_linux_arm64.tar.gz",
			"curl -fsSLO https://github.com/caddyserver/caddy/releases/download/v$V/caddy_${V}_checksums.txt",
			"grep linux_arm64.tar.gz caddy_${V}_checksums.txt | sha512sum -c -",
			"tar xzf caddy_${V}_linux_arm64.tar.gz caddy",
			"install -D -m 755 caddy " + filepath.Join(bin, "caddy"),
		}, "\n")
		p.add(Step{ID: "caddy", Title: "Install Caddy " + v, Why: "Caddy publishes each app at <name>." + domain,
			Actions: []Action{{Argv: []string{"sh", "-c", script}}}})
	}

	if port := portOf(listen); port > 0 && port < 1024 && st("low-port") == Fail {
		p.add(Step{ID: "low-port", Title: "Let Caddy listen on port " + strconv.Itoa(port), Why: "only root may listen below 1024 by default",
			Actions: []Action{
				{Argv: []string{"sudo", "tee", "/etc/sysctl.d/50-unprivileged-ports.conf"}, Stdin: fmt.Sprintf("net.ipv4.ip_unprivileged_port_start=%d\n", port)},
				{Argv: []string{"sudo", "sysctl", "--system"}},
			}})
	}

	if st("caddy-service") != OK {
		s := Step{ID: "caddy-service", Title: "Run Caddy as a user service", Why: detail(r, "caddy-service")}
		unit := filepath.Join(unitDir, "caddy.service")
		if _, err := p.h.Stat(unit); err != nil {
			s.Actions = append(s.Actions, Action{Path: unit, Content: files.CaddyService, Mode: 0o644},
				Action{Argv: []string{"systemctl", "--user", "daemon-reload"}})
		}
		s.Actions = append(s.Actions, Action{Argv: []string{"systemctl", "--user", "enable", "--now", "caddy"}})
		p.add(s)
	}

	if (domain == "local" || strings.HasSuffix(domain, ".local")) && st("avahi") == Fail {
		p.add(Step{ID: "avahi", Title: "Run avahi for .local names", Why: detail(r, "avahi"),
			Actions: []Action{
				{Argv: []string{"sudo", "apt-get", "install", "-y", "avahi-daemon"}},
				{Argv: []string{"sudo", "systemctl", "enable", "--now", "avahi-daemon"}},
			}})
	}
}

func (p *planner) service(r *Report, unitDir string, wroteConfig bool) {
	res, _ := r.Get("service")
	unit := filepath.Join(unitDir, "piway.service")
	s := Step{ID: "service", Title: "Run piway as a user service", Why: res.Detail}
	switch {
	case res.Status != OK:
		if _, err := p.h.Stat(unit); err != nil {
			s.Actions = append(s.Actions, Action{Path: unit, Content: files.PiwayService, Mode: 0o644},
				Action{Argv: []string{"systemctl", "--user", "daemon-reload"}})
		}
		s.Actions = append(s.Actions, Action{Argv: []string{"systemctl", "--user", "enable", "--now", "piway"}})
	case wroteConfig || len(p.steps) > 0:
		// Running already: restart so it picks up the new config, Docker
		// and Caddy.
		s.Why = "running; restart it to pick up the changes above"
		s.Actions = append(s.Actions, Action{Argv: []string{"systemctl", "--user", "restart", "piway"}})
	default:
		return
	}
	url := "http://" + loopback(p.env.Config.Addr) + "/healthz"
	s.Actions = append(s.Actions, Action{Desc: "wait until " + url + " answers", Func: func(ctx context.Context) error {
		return waitHTTP(ctx, p.h, url, 20*time.Second)
	}})
	p.add(s)
}

func (p *planner) tailscale(r *Report, cfg config.Config) {
	res, _ := r.Get("tailscale")
	if res.Status == OK {
		return
	}
	port := strconv.Itoa(portOf(cfg.Addr))
	s := Step{ID: "tailscale", Title: "Publish piway over Tailscale", Why: res.Detail}
	if res.Status == Skip { // not installed
		s.Actions = append(s.Actions, Action{Argv: []string{"sh", "-c", "curl -fsSL https://tailscale.com/install.sh | sh"}})
	}
	if res.Status == Skip || strings.Contains(res.Detail, "failed") {
		s.Actions = append(s.Actions,
			Action{Argv: []string{"sudo", "tailscale", "up"}},
			Action{Argv: []string{"sudo", "tailscale", "set", "--operator=" + p.env.User}})
	}
	s.Actions = append(s.Actions, Action{Argv: []string{"tailscale", "serve", "--bg", port}})
	s.Manual = "`sudo tailscale up` prints a link: open it to add the Pi to your tailnet.\n" +
		"In the Tailscale admin console, turn on DNS › MagicDNS and HTTPS Certificates (once per tailnet).\n" +
		"Then open the address `tailscale serve status` shows, from any device in your tailnet."
	p.add(s)
}

// configFile is the documented example config with proxy_domain set.
func configFile(proxyDomain string) []byte {
	out := []byte("# Written by piway setup. Every setting is explained below and in docs/CONFIG.md.\n")
	b := files.ConfigExample
	if proxyDomain != "" {
		b = bytes.Replace(b, []byte(`# proxy_domain: ""`), []byte(fmt.Sprintf("proxy_domain: %q", proxyDomain)), 1)
	}
	return append(out, b...)
}

func waitHTTP(ctx context.Context, h Host, url string, limit time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	for {
		if status, err := h.HTTPStatus(ctx, url); err == nil && status == 200 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s did not answer within %v; see journalctl --user -u piway -n 50", url, limit)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func detail(r *Report, id string) string {
	res, _ := r.Get(id)
	return res.Detail
}

// shellJoin quotes arguments that need it, for display.
func shellJoin(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\n\"'$|&;<>*?()`\\") {
			if strings.Contains(a, "\n") {
				out[i] = "'\n  " + strings.ReplaceAll(a, "\n", "\n  ") + "\n'"
			} else {
				out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
			}
		} else {
			out[i] = a
		}
	}
	return strings.Join(out, " ")
}

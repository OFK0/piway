package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"

	files "github.com/ofk0/piway/deploy"
	"github.com/ofk0/piway/internal/config"
	"github.com/ofk0/piway/internal/docker"
	"github.com/ofk0/piway/internal/gitauth"
	"github.com/ofk0/piway/internal/webhook"
)

// fakeHost is a machine described by maps. Commands not listed fail.
type fakeHost struct {
	arch   string
	root   bool
	cmds   map[string]string // "name args..." -> output; missing means exit 1
	files  map[string]string // path -> content; listed paths exist
	paths  map[string]bool   // executables on PATH
	env    map[string]string
	http   map[string]int // url -> status; missing means unreachable
	docker *docker.Info   // nil: no daemon
	free   uint64

	// What setup did, in order: "$ argv..." for commands, "write path" and
	// "rm path" for files.
	did     []string
	stdins  map[string]string // command -> stdin it was given
	written map[string]string
	failOn  string // a command that fails when run
}

func (f *fakeHost) Exec(_ context.Context, argv []string, stdin string) error {
	cmd := strings.Join(argv, " ")
	f.did = append(f.did, "$ "+cmd)
	if stdin != "" {
		if f.stdins == nil {
			f.stdins = map[string]string{}
		}
		f.stdins[cmd] = stdin
	}
	if f.failOn != "" && strings.Contains(cmd, f.failOn) {
		return errors.New("exit status 100")
	}
	return nil
}

func (f *fakeHost) WriteFile(name string, data []byte, _ fs.FileMode) error {
	f.did = append(f.did, "write "+name)
	if f.written == nil {
		f.written = map[string]string{}
	}
	f.written[name] = string(data)
	if f.files == nil {
		f.files = map[string]string{}
	}
	f.files[name] = string(data)
	return nil
}

func (f *fakeHost) RemoveAll(name string) error {
	f.did = append(f.did, "rm "+name)
	delete(f.files, name)
	return nil
}

func (f *fakeHost) Executable() (string, error) { return "/tmp/piway-install/piway", nil }

func (f *fakeHost) Run(_ context.Context, name string, args ...string) (string, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	if out, ok := f.cmds[key]; ok {
		return out, nil
	}
	return "", errors.New("exit status 1")
}

func (f *fakeHost) ReadFile(name string) ([]byte, error) {
	if s, ok := f.files[name]; ok {
		return []byte(s), nil
	}
	return nil, fs.ErrNotExist
}

func (f *fakeHost) Stat(name string) (fs.FileInfo, error) {
	if _, ok := f.files[name]; ok {
		return nil, nil
	}
	return nil, fs.ErrNotExist
}

func (f *fakeHost) LookPath(file string) (string, error) {
	if f.paths[file] {
		return "/usr/bin/" + file, nil
	}
	return "", errors.New("not found")
}

func (f *fakeHost) Getenv(key string) string { return f.env[key] }

func (f *fakeHost) HTTPStatus(_ context.Context, url string) (int, error) {
	if s, ok := f.http[url]; ok {
		return s, nil
	}
	return 0, errors.New("connection refused")
}

func (f *fakeHost) DockerInfo(_ context.Context, socket string) (docker.Info, error) {
	if f.docker == nil || socket != "/run/user/1000/docker.sock" {
		return docker.Info{}, errors.New("no daemon")
	}
	return *f.docker, nil
}

func (f *fakeHost) DiskFree(string) (uint64, error) { return f.free, nil }
func (f *fakeHost) Arch() string                    { return f.arch }
func (f *fakeHost) Root() bool                      { return f.root }

const osRelease = `PRETTY_NAME="Debian GNU/Linux 13 (trixie)"
ID=debian
VERSION_CODENAME=trixie
`

func testEnv() Env {
	cfg := config.Default()
	cfg.DataDir = "/home/pi/.local/state/piway"
	cfg.PasswordFile = "/home/pi/.config/piway/password"
	cfg.Addr = "127.0.0.1:8080"
	return Env{Config: cfg, ConfigFile: "/home/pi/.config/piway/config.yaml", User: "pi", Home: "/home/pi"}
}

// healthyPi is a Pi where setup has run and everything works.
func healthyPi() *fakeHost {
	return &fakeHost{
		arch: "arm64",
		cmds: map[string]string{
			"timedatectl show --property=NTPSynchronized --value": "yes",
			"systemctl --user is-enabled piway":                   "enabled",
			"systemctl --user is-active piway":                    "active",
			"systemctl --user is-active caddy":                    "active",
			"systemctl is-active docker.service":                  "inactive",
			"systemctl is-active avahi-daemon":                    "active",
			"loginctl show-user pi --property=Linger --value":     "yes",
			"tailscale serve status":                              "https://pi.tail1234.ts.net (tailnet only)\n|-- / proxy http://127.0.0.1:8080",
		},
		files: map[string]string{
			"/etc/os-release":                 osRelease,
			"/etc/subuid":                     "pi:100000:65536\n",
			"/etc/subgid":                     "pi:100000:65536\n",
			"/home/pi/.config/piway/password": "$argon2id$...",
			"/home/pi/.config/systemd/user/piway.service":   string(files.PiwayService),
			"/home/pi/.config/systemd/user/caddy.service":   "",
			"/home/pi/.local/state/piway":                   "",
			"/proc/sys/net/ipv4/ip_unprivileged_port_start": "443\n",
		},
		paths:  map[string]bool{"piway": true, "docker": true, "caddy": true, "tailscale": true},
		env:    map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"},
		http:   map[string]int{"http://127.0.0.1:8080/healthz": 200, "http://localhost:2019/config/": 200},
		docker: &docker.Info{MemoryLimit: true},
		free:   20 << 30,
	}
}

func statuses(r *Report) map[string]Status {
	m := map[string]Status{}
	for _, res := range r.Results {
		m[res.ID] = res.Status
	}
	return m
}

func wantStatus(t *testing.T, r *Report, want map[string]Status) {
	t.Helper()
	got := statuses(r)
	for id, s := range want {
		if got[id] != s {
			res, _ := r.Get(id)
			t.Errorf("%s = %q (%s), want %q", id, got[id], res.Detail, s)
		}
	}
}

func TestDoctorHealthyPi(t *testing.T) {
	env := testEnv()
	env.Config.ProxyDomain = "local"
	env.Config.ProxyListen = ":443"
	r := Doctor(context.Background(), healthyPi(), env)
	for _, res := range r.Results {
		if res.Status != OK {
			t.Errorf("%s: %s (%s), want ok", res.ID, res.Status, res.Detail)
		}
	}
	if n := len(r.Results); n != 19 {
		t.Errorf("%d checks, want 19", n)
	}
	if r.Failed() {
		t.Error("Failed() on a healthy Pi")
	}
}

func TestDoctorFreshPi(t *testing.T) {
	// Raspberry Pi OS just flashed: nothing of piway's is installed.
	h := &fakeHost{
		arch:  "arm64",
		cmds:  map[string]string{"timedatectl show --property=NTPSynchronized --value": "yes", "systemctl is-active avahi-daemon": "active"},
		files: map[string]string{"/etc/os-release": osRelease},
		free:  25 << 30,
	}
	env := testEnv()
	env.ConfigFile = ""
	r := Doctor(context.Background(), h, env)
	wantStatus(t, r, map[string]Status{
		"platform": OK, "clock": OK, "disk": OK,
		"binary": Fail, "config": Warn, "password": Warn, "service": Fail, "linger": Fail, "http": Fail,
		"docker": Fail, "proxy": Skip, "tailscale": Skip,
	})
	if res, _ := r.Get("linger"); res.Fix != "sudo loginctl enable-linger pi" {
		t.Errorf("linger fix = %q", res.Fix)
	}
	if !r.Failed() {
		t.Error("Failed() is false on a fresh Pi")
	}
}

func TestDoctorDockerProblems(t *testing.T) {
	h := healthyPi()
	h.docker = &docker.Info{MemoryLimit: false}
	h.cmds["systemctl is-active docker.service"] = "active"
	delete(h.files, "/etc/subgid")
	r := Doctor(context.Background(), h, testEnv())
	wantStatus(t, r, map[string]Status{"subids": Fail, "root-daemon": Warn, "daemon": OK, "memory-limit": Warn})
	if res, _ := r.Get("memory-limit"); !strings.Contains(res.Fix, "cgroup_enable=memory") || !strings.Contains(res.Fix, "sudo reboot") {
		t.Errorf("memory-limit fix = %q", res.Fix)
	}

	h.docker = nil
	r = Doctor(context.Background(), h, testEnv())
	wantStatus(t, r, map[string]Status{"daemon": Fail, "memory-limit": Skip})
}

func TestDoctorProxyAndRemote(t *testing.T) {
	h := healthyPi()
	h.files["/proc/sys/net/ipv4/ip_unprivileged_port_start"] = "1024\n"
	h.cmds["systemctl is-active avahi-daemon"] = "inactive"
	delete(h.http, "http://localhost:2019/config/")
	delete(h.files, "/home/pi/.config/piway/password")
	env := testEnv()
	env.Config.ProxyDomain = "local"
	env.Config.ProxyListen = ":443"
	r := Doctor(context.Background(), h, env)
	wantStatus(t, r, map[string]Status{"low-port": Fail, "avahi": Fail, "caddy-admin": Fail, "password": Warn, "tailscale": Fail})
	if res, _ := r.Get("low-port"); !strings.Contains(res.Fix, "ip_unprivileged_port_start=443") {
		t.Errorf("low-port fix = %q", res.Fix)
	}

	// A high port needs no sysctl, and a non-.local domain no avahi.
	env.Config.ProxyDomain = "apps.example.com"
	env.Config.ProxyListen = ":8443"
	r = Doctor(context.Background(), h, env)
	if _, ok := r.Get("low-port"); ok {
		t.Error("low-port checked for :8443")
	}
	if _, ok := r.Get("avahi"); ok {
		t.Error("avahi checked for a non-.local domain")
	}
}

func TestDoctorPrivateRepos(t *testing.T) {
	env := testEnv()
	env.Repos = []webhook.Target{
		{Repo: "OFK0/public", Branch: "main"},
		{Repo: "OFK0/keyed", Branch: "main", Auth: gitauth.DeployKey},
		{Repo: "OFK0/keyed", Branch: "dev", Auth: gitauth.DeployKey},
		{Repo: "OFK0/tokened", Branch: "main", Auth: gitauth.Token},
	}
	var tested []string
	env.TestRepo = func(_ context.Context, src gitauth.Source) error {
		tested = append(tested, src.Repo+" "+src.Auth)
		if src.Auth == gitauth.Token {
			return &gitauth.Error{Kind: gitauth.TokenRejected, Repo: src.Repo}
		}
		return nil
	}
	r := Doctor(context.Background(), healthyPi(), env)
	wantStatus(t, r, map[string]Status{"repo:ofk0/keyed": OK, "repo:ofk0/tokened": Fail})
	if len(tested) != 2 {
		t.Errorf("tested %q: each private repository once, public ones never", tested)
	}
	if res, _ := r.Get("repo:ofk0/tokened"); !strings.Contains(res.Fix, "piway repo token set") {
		t.Errorf("fix = %q", res.Fix)
	}

	env.ReposErr = errors.New("apps.yaml: deploy[0]: branch is required")
	r = Doctor(context.Background(), healthyPi(), env)
	wantStatus(t, r, map[string]Status{"repos": Fail})
}

func TestDoctorWebhooks(t *testing.T) {
	const want = "https://pi.example.ts.net:10000/api/v1/webhooks/github"
	env := testEnv()
	env.Repos = []webhook.Target{
		{Repo: "OFK0/good", Branch: "main"},
		{Repo: "OFK0/good", Branch: "dev"}, // checked once
		{Repo: "OFK0/wrong", Branch: "main"},
		{Repo: "OFK0/none", Branch: "main"},
		{Repo: "OFK0/hidden", Branch: "main"},
	}
	hooks := map[string][]string{
		"OFK0/good":  {want},
		"OFK0/wrong": {"https://pi.example.ts.net/api/v1/webhooks/github"}, // the tailnet-only address
		"OFK0/none":  nil,
	}
	asked := 0
	env.Hooks = func(_ context.Context, repo string) ([]string, error) {
		asked++
		if repo == "OFK0/hidden" {
			return nil, errors.New("HTTP 404: Not Found")
		}
		return hooks[repo], nil
	}
	env.WebhookURL = func(context.Context) string { return want }
	r := Doctor(context.Background(), healthyPi(), env)
	wantStatus(t, r, map[string]Status{
		"webhook-url": OK, "webhook:ofk0/good": OK, "webhook:ofk0/wrong": Fail, "webhook:ofk0/none": Warn, "webhook:ofk0/hidden": Skip,
	})
	if asked != 4 {
		t.Errorf("GitHub asked %d times, want once per repository", asked)
	}
	if res, _ := r.Get("webhook:ofk0/wrong"); !strings.Contains(res.Fix, want) {
		t.Errorf("fix %q does not say where to point it", res.Fix)
	}

	// Without a public address, one warning and no calls to GitHub.
	asked = 0
	env.WebhookURL = func(context.Context) string { return "" }
	r = Doctor(context.Background(), healthyPi(), env)
	wantStatus(t, r, map[string]Status{"webhook-url": Warn})
	if _, ok := r.Get("webhook:ofk0/good"); ok || asked != 0 {
		t.Errorf("checked webhooks with no address (asked %d)", asked)
	}
}

func TestDoctorSystem(t *testing.T) {
	h := healthyPi()
	h.arch = "amd64"
	h.cmds["timedatectl show --property=NTPSynchronized --value"] = "no"
	h.free = 512 << 20
	r := Doctor(context.Background(), h, testEnv())
	wantStatus(t, r, map[string]Status{"platform": Warn, "clock": Warn, "disk": Fail})

	h = healthyPi()
	h.files["/etc/os-release"] = "PRETTY_NAME=\"Debian GNU/Linux 11 (bullseye)\"\nVERSION_CODENAME=bullseye\n"
	h.free = 2 << 30
	r = Doctor(context.Background(), h, testEnv())
	wantStatus(t, r, map[string]Status{"platform": Warn, "disk": Warn})
}

func TestDoctorServiceStates(t *testing.T) {
	h := healthyPi()
	h.cmds["systemctl --user is-active piway"] = "failed"
	r := Doctor(context.Background(), h, testEnv())
	if res, _ := r.Get("service"); res.Status != Fail || !strings.Contains(res.Fix, "journalctl --user -u piway") {
		t.Errorf("service = %+v", res)
	}
	h.cmds["systemctl --user is-enabled piway"] = "disabled"
	r = Doctor(context.Background(), h, testEnv())
	if res, _ := r.Get("service"); res.Fix != "systemctl --user enable --now piway" {
		t.Errorf("service fix = %q", res.Fix)
	}

	env := testEnv()
	env.ConfigErr = errors.New("config.yaml:3: unknown setting \"adress\"")
	r = Doctor(context.Background(), healthyPi(), env)
	wantStatus(t, r, map[string]Status{"config": Fail})
}

func secureHost() *fakeHost {
	return &fakeHost{
		root: true,
		cmds: map[string]string{
			"sshd -T":                                     "port 22\npasswordauthentication no\nkbdinteractiveauthentication no\npermitrootlogin no\nx11forwarding no",
			"getent passwd pi":                            "pi:x:1000:1000:,,,:/home/pi:/bin/bash",
			"ufw status verbose":                          "Status: active\nDefault: deny (incoming), allow (outgoing)\n\nTo  Action  From\n22/tcp  LIMIT IN  192.168.1.0/24",
			"systemctl is-active fail2ban":                "active",
			"fail2ban-client status sshd":                 "Status for the jail: sshd",
			"apt-config dump":                             "APT::Periodic::Unattended-Upgrade \"1\";\nUnattended-Upgrade::Origins-Pattern:: \"origin=Raspberry Pi Foundation,codename=trixie\";",
			"systemctl is-active apt-daily-upgrade.timer": "active",
			"systemctl is-active docker.service":          "inactive",
			"id -u pi":                                    "1000",
			"runuser -u pi -- env XDG_RUNTIME_DIR=/run/user/1000 systemctl --user is-active docker": "active",
		},
		files: map[string]string{"/home/pi/.ssh/authorized_keys": "ssh-ed25519 AAAA... me@laptop\n", "/usr/bin/unattended-upgrade": ""},
		paths: map[string]bool{"ufw": true, "fail2ban-client": true},
	}
}

func TestSecurity(t *testing.T) {
	r := Security(context.Background(), secureHost(), "pi")
	for _, res := range r.Results {
		if res.Status != OK {
			t.Errorf("%s: %s (%s), want ok", res.ID, res.Status, res.Detail)
		}
	}

	h := secureHost()
	h.cmds["sshd -T"] = "passwordauthentication yes\nkbdinteractiveauthentication no\npermitrootlogin prohibit-password\nx11forwarding yes"
	h.cmds["systemctl is-active docker.service"] = "active"
	h.cmds["systemctl is-active ufw-safety.timer"] = "active"
	delete(h.files, "/home/pi/.ssh/authorized_keys")
	r = Security(context.Background(), h, "pi")
	wantStatus(t, r, map[string]Status{
		"ssh-passwordauthentication": Fail, "ssh-permitrootlogin": Fail, "ssh-x11forwarding": Warn,
		"ssh-key": Fail, "root-daemon": Fail, "ufw-safety": Warn,
	})
	if res, _ := r.Get("ssh-passwordauthentication"); !strings.Contains(res.Fix, "10-hardening.conf") {
		t.Errorf("fix = %q, want the file HARDENING.md uses", res.Fix)
	}

	h = secureHost()
	h.root = false
	r = Security(context.Background(), h, "pi")
	if len(r.Results) != 1 || r.Results[0].Fix != "sudo ~/.local/bin/piway doctor --security" {
		t.Errorf("not root: %+v", r.Results)
	}
}

func TestReportText(t *testing.T) {
	r := &Report{Results: []Result{
		{Section: "System", ID: "clock", Title: "Clock", Status: Warn, Detail: "not synchronised", Fix: "sudo timedatectl set-ntp true"},
		{Section: "System", ID: "disk", Title: "Disk space", Status: OK, Detail: "20.0 GiB free on /"},
		{Section: "piway", ID: "service", Title: "Service", Status: Fail, Detail: "not running", Fix: "systemctl --user restart piway\njournalctl --user -u piway -n 50"},
		{Section: "App proxy", ID: "proxy", Title: "Proxy", Status: Skip, Detail: "proxy_domain is not set"},
	}}
	var b bytes.Buffer
	if err := r.WriteText(&b, "piway setup fixes most of these."); err != nil {
		t.Fatal(err)
	}
	want := `System
  warn  Clock       not synchronised
                    fix: sudo timedatectl set-ntp true
  ok    Disk space  20.0 GiB free on /

piway
  fail  Service     not running
                    fix: systemctl --user restart piway
                         journalctl --user -u piway -n 50

App proxy
  skip  Proxy       proxy_domain is not set

1 ok, 1 warning, 1 failure, 1 not applicable.
piway setup fixes most of these.
`
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}
}

func TestReportJSON(t *testing.T) {
	var b bytes.Buffer
	if err := (&Report{}).WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Checks  []Result       `json:"checks"`
		Summary map[string]int `json:"summary"`
	}
	if err := json.Unmarshal(b.Bytes(), &out); err != nil || out.Checks == nil || out.Summary["fail"] != 0 {
		t.Errorf("empty report JSON = %s (%v)", b.String(), err)
	}
}

func TestLoopback(t *testing.T) {
	for in, want := range map[string]string{":8080": "127.0.0.1:8080", "0.0.0.0:80": "127.0.0.1:80", "[::]:8080": "127.0.0.1:8080", "127.0.0.1:9000": "127.0.0.1:9000", "192.168.1.5:8080": "192.168.1.5:8080"} {
		if got := loopback(in); got != want {
			t.Errorf("loopback(%q) = %q, want %q", in, got, want)
		}
	}
}

// The real System must satisfy the checks' expectations about errors.
func TestSystemHost(t *testing.T) {
	var h Host = System{}
	if _, err := h.Stat(t.TempDir() + "/missing"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Stat(missing) = %v", err)
	}
	if out, err := h.Run(context.Background(), "sh", "-c", "echo ' hi '; exit 3"); out != "hi" || err == nil {
		t.Errorf("Run = %q, %v; want trimmed output and an error", out, err)
	}
	if free, err := h.DiskFree(t.TempDir()); err != nil || free == 0 {
		t.Errorf("DiskFree = %d, %v", free, err)
	}
}

// A unit from before #104 starts piway without waiting for Docker: doctor
// warns, and setup replaces it without restarting piway for that alone.
func TestOldUnitIsUpdated(t *testing.T) {
	env := testEnv()
	env.Config.ProxyDomain = "local"
	env.Config.ProxyListen = ":443"
	h := healthyPi()
	h.env["PATH"] = "/home/pi/.local/bin:/usr/bin"
	h.files["/home/pi/.config/systemd/user/piway.service"] = "[Unit]\nDescription=piway agent\nAfter=network-online.target\n\n[Service]\nExecStart=%h/.local/bin/piway\n"
	r := Doctor(context.Background(), h, env)
	if res, _ := r.Get("unit"); res.Status != Warn {
		t.Fatalf("unit check = %+v, want a warning", res)
	}
	steps, err := Plan(context.Background(), h, env, Options{ProxyDomain: "local", Docker: true})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	var unit Step
	for _, s := range steps {
		if len(s.Actions) > 0 {
			ids = append(ids, s.ID)
		}
		if s.ID == "unit" {
			unit = s
		}
	}
	if strings.Join(ids, " ") != "unit" {
		t.Fatalf("steps = %v, want only the unit update (no restart)", ids)
	}
	if a := unit.Actions[0]; !bytes.Equal(a.Content, files.PiwayService) {
		t.Errorf("wrote %q", a.Content)
	}
}

func TestStartsAfter(t *testing.T) {
	for unit, want := range map[string]bool{
		"[Unit]\nAfter=network-online.target docker.service\n":        true,
		"[Unit]\nAfter = docker.service\n":                            true,
		"[Unit]\nAfter=network-online.target\nAfter=docker.service\n": true,
		"[Unit]\nAfter=network-online.target\n":                       false,
		"[Unit]\n# After=docker.service\n":                            false,
		"[Unit]\nWants=docker.service\n":                              false,
	} {
		if got := startsAfter([]byte(unit), "docker.service"); got != want {
			t.Errorf("%q: %v, want %v", unit, got, want)
		}
	}
}

// Turning on the memory cgroup takes effect at the next boot: setup must
// not restart piway for it (seen on a real Pi: "restart it to pick up the
// changes above" with nothing to pick up).
func TestNoRestartForRebootOnlySteps(t *testing.T) {
	env := testEnv()
	env.Config.ProxyDomain = "local"
	env.Config.ProxyListen = ":443"
	h := healthyPi()
	h.env["PATH"] = "/home/pi/.local/bin:/usr/bin"
	h.docker = &docker.Info{MemoryLimit: false}
	h.files["/boot/firmware/cmdline.txt"] = "console=serial0,115200 root=PARTUUID=0000-02 rootwait\n"
	steps, err := Plan(context.Background(), h, env, Options{ProxyDomain: "local", Docker: true})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	reboot := false
	for _, s := range steps {
		if len(s.Actions) > 0 {
			ids = append(ids, s.ID)
		}
		reboot = reboot || s.Reboot
	}
	if strings.Join(ids, " ") != "memory-cgroup" || !reboot {
		t.Errorf("steps = %v (reboot %v), want only memory-cgroup, which needs a reboot, and no restart", ids, reboot)
	}
}

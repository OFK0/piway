package setup

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	files "github.com/ofk0/piway/deploy"
	"github.com/ofk0/piway/internal/docker"
)

// freshPi is Raspberry Pi OS just after flashing, with piway's binary
// downloaded by install.sh to a temporary directory.
func freshPi() *fakeHost {
	return &fakeHost{
		arch: "arm64",
		cmds: map[string]string{
			"timedatectl show --property=NTPSynchronized --value": "yes",
			"systemctl is-active avahi-daemon":                    "active",
			"dpkg --print-architecture":                           "arm64",
		},
		files: map[string]string{
			"/etc/os-release":            osRelease,
			"/boot/firmware/cmdline.txt": "console=serial0,115200 root=PARTUUID=abc rootwait\n",
		},
		env:  map[string]string{"PATH": "/usr/local/bin:/usr/bin:/bin", "XDG_RUNTIME_DIR": "/run/user/1000"},
		free: 25 << 30,
	}
}

func freshEnv() Env {
	env := testEnv()
	env.ConfigFile = ""
	return env
}

var noPassword = func(context.Context) error { return nil }

func stepIDs(steps []Step) []string {
	var ids []string
	for _, s := range steps {
		ids = append(ids, s.ID)
	}
	return ids
}

func TestPlanFreshPi(t *testing.T) {
	h := freshPi()
	steps, err := Plan(context.Background(), h, freshEnv(), Options{ProxyDomain: "local", Docker: true, SetPassword: noPassword})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"binary", "config", "password", "linger", "docker", "root-daemon", "subids", "rootless", "memory-cgroup", "caddy", "caddy-service", "service"}
	if got := stepIDs(steps); !slices.Equal(got, want) {
		t.Fatalf("steps = %v\nwant    %v", got, want)
	}

	byID := map[string]Step{}
	for _, s := range steps {
		byID[s.ID] = s
	}
	if a := byID["binary"].Actions; len(a) != 2 || a[0].String() != "$ install -D -m 755 /tmp/piway-install/piway /home/pi/.local/bin/piway" {
		t.Errorf("binary actions = %v", a)
	}
	if c := string(byID["config"].Actions[0].Content); !strings.Contains(c, "\nproxy_domain: \"local\"\n") || !strings.Contains(c, "# addr: \":8080\"") {
		t.Errorf("config does not set proxy_domain in the documented file:\n%s", c)
	}
	src := byID["docker"].Actions[5]
	if !strings.Contains(src.Stdin, "Suites: trixie\n") || !strings.Contains(src.Stdin, "Architectures: arm64\n") {
		t.Errorf("docker.sources = %q", src.Stdin)
	}
	for _, a := range byID["docker"].Actions {
		if a.Argv[0] != "sudo" {
			t.Errorf("a docker package step runs without sudo: %v", a.Argv)
		}
	}
	if !byID["memory-cgroup"].Reboot {
		t.Error("the memory cgroup step does not ask for a reboot")
	}
	if a := byID["caddy"].Actions[0].Argv[2]; !strings.Contains(a, "sha512sum -c -") || !strings.Contains(a, "V="+CaddyVersion) {
		t.Errorf("caddy script does not verify the download:\n%s", a)
	}
	if a := byID["service"].Actions[0]; a.Path != "/home/pi/.config/systemd/user/piway.service" || !bytes.Equal(a.Content, files.PiwayService) {
		t.Errorf("service unit = %s, want the shipped piway.service", a.Path)
	}
	if last := byID["service"].Actions[len(byID["service"].Actions)-1]; last.Func == nil {
		t.Error("the service step does not wait for /healthz")
	}
}

func TestPlanHealthyPiDoesNothing(t *testing.T) {
	env := testEnv()
	env.Config.ProxyDomain = "local"
	env.Config.ProxyListen = ":443"
	h := healthyPi()
	h.env["PATH"] = "/home/pi/.local/bin:/usr/bin"
	steps, err := Plan(context.Background(), h, env, Options{ProxyDomain: "local", Docker: true, Tailscale: true, SetPassword: noPassword})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 0 {
		t.Errorf("a healthy Pi still has steps: %v", stepIDs(steps))
	}
}

func TestPlanOnlyWhatIsMissing(t *testing.T) {
	// Rootless Docker stopped after a reboot without lingering.
	h := healthyPi()
	h.docker = nil
	h.files["/home/pi/.bashrc"] = "export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/docker.sock\n"
	steps, err := Plan(context.Background(), h, testEnv(), Options{Docker: true, SetPassword: noPassword})
	if err != nil {
		t.Fatal(err)
	}
	if got := stepIDs(steps); !slices.Equal(got, []string{"rootless", "service"}) {
		t.Fatalf("steps = %v, want rootless then a piway restart", got)
	}
	if a := steps[0].Actions; len(a) != 2 {
		t.Errorf("rootless adds the DOCKER_HOST line again: %v", a)
	}
	if a := steps[1].Actions[0].String(); a != "$ systemctl --user restart piway" {
		t.Errorf("service action = %q", a)
	}
}

func TestPlanRefuses(t *testing.T) {
	h := freshPi()
	h.arch = "amd64"
	if _, err := Plan(context.Background(), h, freshEnv(), Options{}); err == nil || !strings.Contains(err.Error(), "amd64") {
		t.Errorf("amd64: %v", err)
	}
	env := freshEnv()
	env.ConfigErr = errors.New("config.yaml:2: unknown setting")
	if _, err := Plan(context.Background(), freshPi(), env, Options{}); err == nil {
		t.Error("a broken config was not refused")
	}
}

func TestPlanKeepsExistingConfig(t *testing.T) {
	env := testEnv() // has a config file, proxy_domain ""
	steps, _ := Plan(context.Background(), healthyPi(), env, Options{ProxyDomain: "local", SetPassword: noPassword})
	if len(steps) == 0 || steps[0].ID != "config-note" || len(steps[0].Actions) != 0 {
		t.Fatalf("steps = %v, want only a note about proxy_domain", stepIDs(steps))
	}
}

func TestPlanTailscale(t *testing.T) {
	h := healthyPi()
	delete(h.paths, "tailscale")
	steps, _ := Plan(context.Background(), h, testEnv(), Options{Tailscale: true})
	if got := stepIDs(steps); !slices.Equal(got, []string{"tailscale"}) {
		t.Fatalf("steps = %v", got)
	}
	var cmds []string
	for _, a := range steps[0].Actions {
		cmds = append(cmds, a.String())
	}
	want := []string{"$ sh -c 'curl -fsSL https://tailscale.com/install.sh | sh'", "$ sudo tailscale up", "$ sudo tailscale set --operator=pi", "$ tailscale serve --bg 8080"}
	if !slices.Equal(cmds, want) {
		t.Errorf("tailscale = %q", cmds)
	}
	if !strings.Contains(steps[0].Manual, "MagicDNS") {
		t.Error("no note about the admin console")
	}
}

func runner(h *fakeHost, answers string) (*Runner, *bytes.Buffer) {
	var out bytes.Buffer
	return &Runner{Host: h, Out: &out, In: bufio.NewReader(strings.NewReader(answers))}, &out
}

func TestRunnerDryRun(t *testing.T) {
	h := freshPi()
	steps, _ := Plan(context.Background(), h, freshEnv(), Options{ProxyDomain: "local", Docker: true, SetPassword: noPassword})
	r, out := runner(h, "")
	r.DryRun = true
	reboot, err := r.Run(context.Background(), steps)
	if err != nil || !reboot {
		t.Fatalf("Run = %v, %v", reboot, err)
	}
	if len(h.did) != 0 {
		t.Errorf("a dry run did %v", h.did)
	}
	t.Log("\n" + out.String()) // go test -v -run DryRun shows the plan for a fresh Pi
	for _, want := range []string{"The plan: 12 steps.", "$ sudo loginctl enable-linger pi", "$ sudo tee /etc/apt/sources.list.d/docker.sources <<EOF\n      Types: deb", "write /home/pi/.config/systemd/user/piway.service", "Nothing was changed."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry run output lacks %q", want)
		}
	}
}

func TestRunnerYesRunsEverythingInOrder(t *testing.T) {
	h := freshPi()
	steps, _ := Plan(context.Background(), h, freshEnv(), Options{ProxyDomain: "", Docker: false, SetPassword: noPassword})
	h.http = map[string]int{"http://127.0.0.1:8080/healthz": 200}
	r, _ := runner(h, "")
	r.Yes = true
	if _, err := r.Run(context.Background(), steps); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"$ install -D -m 755 /tmp/piway-install/piway /home/pi/.local/bin/piway",
		`$ sh -c echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.profile`,
		"write /home/pi/.config/piway/config.yaml",
		"$ sudo loginctl enable-linger pi",
		"write /home/pi/.config/systemd/user/piway.service",
		"$ systemctl --user daemon-reload",
		"$ systemctl --user enable --now piway",
	}
	if !slices.Equal(h.did, want) {
		t.Errorf("did\n%s\nwant\n%s", strings.Join(h.did, "\n"), strings.Join(want, "\n"))
	}
}

func TestRunnerStops(t *testing.T) {
	steps := []Step{
		{ID: "a", Title: "A", Actions: []Action{{Argv: []string{"true"}}}},
		{ID: "b", Title: "B", Actions: []Action{{Argv: []string{"sudo", "apt-get", "install", "x"}}, {Argv: []string{"after"}}}},
		{ID: "c", Title: "C", Actions: []Action{{Argv: []string{"never"}}}},
	}

	// Declining a step stops before it.
	h := freshPi()
	r, _ := runner(h, "y\nn\n")
	if _, err := r.Run(context.Background(), steps); !errors.Is(err, ErrDeclined) {
		t.Errorf("declined: err = %v", err)
	}
	if !slices.Equal(h.did, []string{"$ true"}) {
		t.Errorf("declined: did %v", h.did)
	}

	// A failing command stops the run and names the step and command.
	h = freshPi()
	h.failOn = "apt-get"
	r, _ = runner(h, "")
	r.Yes = true
	_, err := r.Run(context.Background(), steps)
	if err == nil || !strings.Contains(err.Error(), "B: $ sudo apt-get install x") || !strings.Contains(err.Error(), "run piway setup again") {
		t.Errorf("failure: err = %v", err)
	}
	if !slices.Equal(h.did, []string{"$ true", "$ sudo apt-get install x"}) {
		t.Errorf("failure: did %v", h.did)
	}

	// No answer at all (stdin closed) is not taken as yes.
	h = freshPi()
	r, _ = runner(h, "")
	if _, err := r.Run(context.Background(), steps); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("closed stdin: err = %v", err)
	}
	if len(h.did) != 0 {
		t.Errorf("closed stdin: did %v", h.did)
	}
}

func TestUninstallPlan(t *testing.T) {
	h := healthyPi()
	h.files["/home/pi/.local/bin/piway"] = ""
	h.files["/home/pi/.config/piway"] = ""
	steps := UninstallPlan(context.Background(), h, testEnv(), false)
	if got := stepIDs(steps); !slices.Equal(got, []string{"remove-piway", "remove-caddy", "remove-binaries", "uninstall-note"}) {
		t.Errorf("steps = %v", got)
	}
	if !strings.Contains(steps[len(steps)-1].Manual, "--purge") {
		t.Error("the note does not say how to delete the data")
	}
	steps = UninstallPlan(context.Background(), h, testEnv(), true)
	purge := steps[len(steps)-2]
	if purge.ID != "purge" || len(purge.Actions) != 2 {
		t.Errorf("purge = %+v", purge)
	}
}

var _ = docker.Info{}

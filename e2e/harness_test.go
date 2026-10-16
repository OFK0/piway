//go:build e2e

// Package e2e runs the real piway binary against real Docker: example apps
// built from local git repositories and deployed by signed webhooks, with
// no GitHub in the way. See README.md.
package e2e

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const webhookSecret = "e2e-webhook-secret-0123456789abcdef"

// piway is one running piway process with its own directories.
type piway struct {
	t       *testing.T
	bin     string
	home    string // HOME, config and state live under it
	addr    string // 127.0.0.1:<port>
	cmd     *exec.Cmd
	log     *bytes.Buffer
	secrets map[string]string // app -> secret value written before it deploys
	apps    []string          // apps deployed, for cleanup
	repos   map[string]string // owner/name -> directory
}

func (p *piway) url(path string) string { return "http://" + p.addr + path }
func (p *piway) cfgDir() string         { return filepath.Join(p.home, ".config", "piway") }

func (p *piway) env() []string {
	return append(os.Environ(),
		"HOME="+p.home,
		"XDG_CONFIG_HOME="+filepath.Join(p.home, ".config"),
		"XDG_STATE_HOME="+filepath.Join(p.home, ".state"),
	)
}

// findBinary returns the piway binary to test: $PIWAY_BIN, else bin/piway.
func findBinary(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("PIWAY_BIN")
	if bin == "" {
		bin = filepath.Join("..", "bin", "piway")
	}
	abs, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("no piway binary at %s: run make build (or set PIWAY_BIN)", abs)
	}
	return abs
}

// skipUnlessCI skips the test, except in CI, where a missing prerequisite
// is a failure: a release gate that skips everything proves nothing.
func skipUnlessCI(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}

// requireDocker skips or fails when there is no Docker, and refuses to
// share a daemon with a piway that is in use, unless E2E_FORCE is set:
// the engines and the database network have fixed names.
func requireDocker(t *testing.T) {
	t.Helper()
	if out, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		skipUnlessCI(t, "no Docker daemon: %v: %s", err, bytes.TrimSpace(out))
	}
	if os.Getenv("E2E_FORCE") != "" {
		// Other piway containers are fine, but what cleanup removes by name
		// must not be somebody's: refuse rather than delete it.
		for _, c := range collisions() {
			t.Fatalf("E2E_FORCE: %s already exists and the run would remove it at the end; remove or rename it first", c)
		}
		return
	}
	names, _ := exec.Command("docker", "ps", "-a", "--format", "{{.Names}}").Output()
	for _, n := range strings.Fields(string(names)) {
		if strings.HasPrefix(n, "piway-") {
			t.Fatalf("this Docker daemon already runs piway containers (%s): the e2e run uses the engines' fixed names. Use a clean daemon (CI does), or set E2E_FORCE=1 to run anyway", n)
		}
	}
}

// collisions lists the Docker objects the run creates and cleanup removes
// by name that already exist: the engines, the database network and
// volumes, and containers or volumes of an example app.
func collisions() []string {
	list := func(args ...string) []string {
		out, _ := exec.Command("docker", args...).Output()
		return strings.Fields(string(out))
	}
	var found []string
	for _, e := range []string{"postgres", "mariadb", "redis"} {
		for _, n := range list("ps", "-a", "--format", "{{.Names}}", "--filter", "name=^piway-"+e+"$") {
			found = append(found, "container "+n)
		}
		for _, v := range list("volume", "ls", "-q", "--filter", "name=piway-db-"+e) {
			found = append(found, "volume "+v)
		}
	}
	for _, n := range list("network", "ls", "--format", "{{.Name}}", "--filter", "name=^piway-db$") {
		found = append(found, "network "+n)
	}
	for _, ex := range examples {
		for _, n := range list("ps", "-a", "--format", "{{.Names}}", "--filter", "label=piway.app="+ex.name) {
			found = append(found, "container "+n)
		}
		for _, v := range list("volume", "ls", "-q", "--filter", "name=piway-"+ex.name+"_") {
			found = append(found, "volume "+v)
		}
	}
	return found
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// newPiway prepares the directories of a piway; start runs it.
func newPiway(t *testing.T) *piway {
	t.Helper()
	p := &piway{
		t: t, bin: findBinary(t), home: t.TempDir(), log: &bytes.Buffer{},
		addr:    fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		secrets: map[string]string{},
		repos:   map[string]string{},
	}
	if err := os.MkdirAll(filepath.Join(p.cfgDir(), "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "webhook_secret: " + webhookSecret + "\n"
	write(t, filepath.Join(p.cfgDir(), "config.yaml"), cfg, 0o600)
	return p
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// start runs piway and waits until it answers.
func (p *piway) start() {
	p.t.Helper()
	p.cmd = exec.Command(p.bin, "-addr", p.addr)
	p.cmd.Env = p.env()
	p.cmd.Stdout, p.cmd.Stderr = p.log, p.log
	if err := p.cmd.Start(); err != nil {
		p.t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(p.url("/healthz")); err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	p.t.Fatalf("piway did not answer in 30 s; log:\n%s", p.log)
}

// stop ends piway gently, or with SIGKILL.
func (p *piway) stop(kill bool) {
	if p.cmd == nil || p.cmd.Process == nil {
		return
	}
	if kill {
		p.cmd.Process.Signal(syscall.SIGKILL)
	} else {
		p.cmd.Process.Signal(syscall.SIGINT)
	}
	done := make(chan struct{})
	go func() { p.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		p.cmd.Process.Kill()
		<-done
	}
	p.cmd = nil
}

// cli runs a piway subcommand in this piway's directories.
func (p *piway) cli(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.bin, args...)
	cmd.Env = p.env()
	cmd.WaitDelay = 10 * time.Second // a child that keeps the pipe open must not hang the run
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// cleanup removes what the run made in Docker. Containers, volumes and
// the database network are removed by name, only those this run created.
func (p *piway) cleanup() {
	run := func(args ...string) { exec.Command("docker", args...).Run() }
	for _, app := range p.apps {
		out, _ := exec.Command("docker", "ps", "-aq", "--filter", "label=piway.app="+app).Output()
		for _, id := range strings.Fields(string(out)) {
			run("rm", "-f", id)
		}
		vols, _ := exec.Command("docker", "volume", "ls", "-q", "--filter", "name=piway-"+app+"_").Output()
		for _, v := range strings.Fields(string(vols)) {
			run("volume", "rm", v)
		}
		// The images piway built, which would fill a small disk run by run.
		imgs, _ := exec.Command("docker", "images", "-q", "piway/"+app).Output()
		for _, id := range strings.Fields(string(imgs)) {
			run("rmi", "-f", id)
		}
	}
	for _, e := range []string{"postgres", "mariadb", "redis"} {
		run("rm", "-f", "piway-"+e)
		vols, _ := exec.Command("docker", "volume", "ls", "-q", "--filter", "name=piway-db-"+e).Output()
		for _, v := range strings.Fields(string(vols)) {
			run("volume", "rm", v)
		}
	}
	run("network", "rm", "piway-db")
}

// api calls piway's API and decodes a JSON answer into out (unless nil).
func (p *piway) api(method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, p.url(path), rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: %v: %s", method, path, err, b)
		}
	}
	return resp.StatusCode, nil
}

// build is the part of a build record the tests read.
type build struct {
	ID     string `json:"id"`
	App    string `json:"app"`
	Repo   string `json:"repo"`
	Commit string `json:"commit"`
	State  string `json:"state"`
	Kind   string `json:"kind"`
	Error  string `json:"error"`
}

// waitBuild waits until a build matching match has finished, and fails
// the test with its log unless it succeeded.
func (p *piway) waitBuild(what string, timeout time.Duration, match func(build) bool) build {
	p.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var list struct {
			Builds []build `json:"builds"`
		}
		if _, err := p.api("GET", "/api/v1/builds", nil, &list); err == nil {
			for i := len(list.Builds) - 1; i >= 0; i-- {
				b := list.Builds[i]
				if !match(b) {
					continue
				}
				switch b.State {
				case "succeeded":
					return b
				case "failed", "cancelled":
					p.t.Fatalf("%s: build %s %s: %s\n%s", what, b.ID, b.State, b.Error, p.buildLog(b.ID))
				}
				break
			}
		}
		time.Sleep(2 * time.Second)
	}
	p.t.Fatalf("%s: no finished build in %s", what, timeout)
	return build{}
}

func (p *piway) buildLog(id string) string {
	resp, err := http.Get(p.url("/api/v1/builds/" + id + "/log"))
	if err != nil {
		return err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if len(b) > 6000 {
		b = append([]byte("...\n"), b[len(b)-6000:]...)
	}
	return string(b)
}

// repo makes a git repository (owner/name) with the files of dir at its
// root, lists it in apps.yaml and returns it.
func (p *piway) repo(name, dir string) string {
	p.t.Helper()
	repoDir := filepath.Join(p.home, "repos", name)
	if err := os.MkdirAll(filepath.Dir(repoDir), 0o755); err != nil {
		p.t.Fatal(err)
	}
	if out, err := exec.Command("cp", "-r", dir, repoDir).CombinedOutput(); err != nil {
		p.t.Fatalf("copy %s: %v: %s", dir, err, out)
	}
	p.git(repoDir, "init", "-q", "-b", "main")
	p.git(repoDir, "config", "uploadpack.allowAnySHA1InWant", "true")
	p.git(repoDir, "add", "-A")
	p.git(repoDir, "commit", "-q", "-m", "first")
	p.repos[name] = repoDir
	apps := p.appsYAML()
	apps += fmt.Sprintf("  - repo: %s\n    branch: main\n", name)
	write(p.t, filepath.Join(p.cfgDir(), "apps.yaml"), apps, 0o600)
	return repoDir
}

func (p *piway) appsYAML() string {
	b, err := os.ReadFile(filepath.Join(p.cfgDir(), "apps.yaml"))
	if err != nil {
		return "deploy:\n"
	}
	return string(b)
}

func (p *piway) git(dir string, args ...string) string {
	p.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=e2e", "GIT_AUTHOR_EMAIL=e2e@example.com",
		"GIT_COMMITTER_NAME=e2e", "GIT_COMMITTER_EMAIL=e2e@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		p.t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// push sends piway a signed GitHub push event for repo's current commit,
// as if it had just been pushed, and returns the commit.
func (p *piway) push(repo string) string {
	p.t.Helper()
	dir := p.repos[repo]
	sha := p.git(dir, "rev-parse", "HEAD")
	body, _ := json.Marshal(map[string]any{
		"ref": "refs/heads/main", "after": sha, "deleted": false,
		"repository": map[string]string{"full_name": repo, "clone_url": "file://" + dir},
		"pusher":     map[string]string{"name": "e2e"},
	})
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(body)
	req, _ := http.NewRequest("POST", p.url("/api/v1/webhooks/github"), bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", fmt.Sprintf("e2e-%d", time.Now().UnixNano()))
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		p.t.Fatalf("webhook for %s: %d %s", repo, resp.StatusCode, b)
	}
	return sha
}

// appURL is the app's stable address, which piway serves on 127.0.0.1.
func (p *piway) appURL(app string) string {
	p.t.Helper()
	var list struct {
		Apps []struct {
			Name string `json:"name"`
			Port int    `json:"port"`
		} `json:"apps"`
	}
	if _, err := p.api("GET", "/api/v1/apps", nil, &list); err != nil {
		p.t.Fatal(err)
	}
	for _, a := range list.Apps {
		if a.Name == app {
			return fmt.Sprintf("http://127.0.0.1:%d", a.Port)
		}
	}
	p.t.Fatalf("no app %q", app)
	return ""
}

// getJSON GETs url and decodes the JSON answer.
// client has a timeout: a hung app must fail a check, not the whole run.
var client = &http.Client{Timeout: 20 * time.Second}

// tryGet fetches url and returns the status and body; the error is for a
// request that did not complete. Use it in a poll: it never ends the test.
func tryGet(url string) (int, string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), err
}

// tryJSON is tryGet for a JSON answer with status 200.
func tryJSON(url string, out any) error {
	code, body, err := tryGet(url)
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("status %d: %s", code, body)
	}
	return json.Unmarshal([]byte(body), out)
}

func getJSON(t *testing.T, url string, out any) {
	t.Helper()
	if err := tryJSON(url, out); err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
}

// eventually retries f until it returns "" (success) or the time is up.
func eventually(t *testing.T, what string, timeout time.Duration, f func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		if last = f(); last == "" {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("%s: %s (after %s)", what, last, timeout)
}

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const buildTimeout = 30 * time.Minute // the first Java build pulls images and dependencies

// examples are deployed in this order: the fast ones first.
var examples = []struct {
	name, secret string
}{
	{"loadlab", "LOADLAB_TOKEN"},
	{"thumbnailer", "THUMB_TOKEN"},
	{"chat", "CHAT_TOKEN"},
	{"feeds", "FEEDS_TOKEN"},
	{"events", "EVENTS_TOKEN"},
	{"docs-site", ""},
	{"notes", "NOTES_TOKEN"},
	{"reports", "REPORTS_TOKEN"},
}

type info struct {
	App    string `json:"app"`
	Env    struct{ GREETING string }
	Secret struct {
		Name   string `json:"name"`
		Set    bool   `json:"set"`
		Length int    `json:"length"`
	} `json:"secret"`
}

func secretValue(app string) string { return "e2e-secret-" + app }

// TestE2E runs the scenarios in order against one piway: later ones use
// what earlier ones deployed. Select some with -run 'TestE2E/(deploy-loadlab|traffic)': the
// subtests are deploy-<app>, volume, traffic, rollback, secrets, db-mariadb,
// db-redis, kill9, private-repo and dashboard.
func TestE2E(t *testing.T) {
	requireDocker(t)
	p := newPiway(t)
	t.Cleanup(p.cleanup)
	t.Cleanup(func() {
		p.stop(false)
		if t.Failed() {
			t.Logf("piway's log:\n%s", tail(p.log.String(), 8000))
		}
	})

	// One local git repository per example, listed in apps.yaml, with the
	// secret its piway.yaml declares written where piway reads it.
	write(t, filepath.Join(p.cfgDir(), "apps.yaml"), "deploy:\n", 0o600)
	for _, ex := range examples {
		p.repo("e2e/"+ex.name, filepath.Join("..", "examples", ex.name))
		p.apps = append(p.apps, ex.name)
		if ex.secret != "" {
			write(t, filepath.Join(p.cfgDir(), "secrets", ex.name+".env"), ex.secret+"="+secretValue(ex.name)+"\n", 0o600)
		}
	}
	p.start()

	for _, ex := range examples {
		t.Run("deploy-"+ex.name, func(t *testing.T) {
			deployExample(t, p, ex.name, ex.secret != "")
		})
	}
	t.Run("volume", func(t *testing.T) { volumeSurvivesDeploy(t, p) })
	t.Run("traffic", func(t *testing.T) { redeployUnderTraffic(t, p) })
	t.Run("rollback", func(t *testing.T) { rollback(t, p) })
	t.Run("secrets", func(t *testing.T) { secrets(t, p) })
	t.Run("db-mariadb", func(t *testing.T) { databaseMariaDB(t, p) })
	t.Run("db-redis", func(t *testing.T) { databaseRedis(t, p) })
	t.Run("kill9", func(t *testing.T) { killNine(t, p) })
	t.Run("private-repo", func(t *testing.T) { privateRepo(t, p) })
	t.Run("dashboard", func(t *testing.T) { dashboard(t, p) })
}

func tail(s string, n int) string {
	if len(s) > n {
		return "...\n" + s[len(s)-n:]
	}
	return s
}

// deployExample pushes the example's repository and checks that the app
// it makes answers on its stable address, with its environment and secret.
func deployExample(t *testing.T, p *piway, app string, hasSecret bool) {
	sha := p.push("e2e/" + app)
	p.waitBuild(app, buildTimeout, func(b build) bool { return b.Repo == "e2e/"+app && b.Commit == sha && b.Kind == "" })
	base := p.appURL(app)

	if app == "docs-site" { // a static site: no process, so its info is a file
		var i info
		getJSON(t, base+"/api/info.json", &i)
		if i.App != "docs-site" {
			t.Errorf("info.json: %+v", i)
		}
		if code, body := get(t, base+"/about"); code != 200 || !strings.Contains(body, `<main id="page">`) {
			t.Errorf("/about: %d, want the app shell", code)
		}
		if code, _ := get(t, base+"/assets/missing.js"); code != 404 {
			t.Errorf("a missing asset: %d, want 404 (#144)", code)
		}
		return
	}
	eventually(t, app+" /healthz", 2*time.Minute, func() string {
		if code, _, err := tryGet(base + "/healthz"); err != nil || code != 200 {
			return fmt.Sprintf("status %d %v", code, err)
		}
		return ""
	})
	var i info
	getJSON(t, base+"/api/info", &i)
	if i.App != app {
		t.Errorf("/api/info says app %q", i.App)
	}
	if i.Env.GREETING != "hello from piway.yaml" {
		t.Errorf("run.env did not arrive: GREETING = %q", i.Env.GREETING)
	}
	if hasSecret && (!i.Secret.Set || i.Secret.Length != len(secretValue(app))) {
		t.Errorf("the secret did not arrive: %+v (want length %d)", i.Secret, len(secretValue(app)))
	}
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	code, body, err := tryGet(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return code, body
}

func request(t *testing.T, method, url string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

type loadStats struct {
	DiskBytes int64 `json:"disk_bytes"`
}

// redeploy queues a redeploy of app and waits for it.
func redeploy(t *testing.T, p *piway, app string) build {
	t.Helper()
	var queued build
	if code, err := p.api("POST", "/api/v1/apps/"+app+"/redeploy", nil, &queued); err != nil || code != 202 {
		t.Fatalf("redeploy %s: %d %v", app, code, err)
	}
	return p.waitBuild("redeploy "+app, buildTimeout, func(b build) bool { return b.ID == queued.ID })
}

// volumeSurvivesDeploy writes to loadlab's volume and redeploys: the data
// is still there in the new container (#110).
func volumeSurvivesDeploy(t *testing.T, p *piway) {
	base := p.appURL("loadlab")
	if code, body := request(t, "POST", base+"/disk?mb=3"); code != 200 {
		t.Fatalf("write to the volume: %d %s", code, body)
	}
	var before loadStats
	getJSON(t, base+"/api/stats", &before)
	if before.DiskBytes != 3<<20 {
		t.Fatalf("disk_bytes = %d before the deploy, want 3 MiB", before.DiskBytes)
	}
	redeploy(t, p, "loadlab")
	eventually(t, "the redeployed loadlab", time.Minute, func() string {
		var after loadStats
		if err := tryJSON(base+"/api/stats", &after); err != nil {
			return err.Error()
		}
		if after.DiskBytes != before.DiskBytes {
			return fmt.Sprintf("disk_bytes = %d, want %d", after.DiskBytes, before.DiskBytes)
		}
		return ""
	})
}

// redeployUnderTraffic hits an app continuously while it is redeployed:
// not one request may fail (zero-downtime, blue/green).
func redeployUnderTraffic(t *testing.T, p *piway) {
	base := p.appURL("loadlab")
	var requests, failures atomic.Int64
	var firstFailure atomic.Value
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 8}}
			for {
				select {
				case <-stop:
					return
				default:
				}
				requests.Add(1)
				resp, err := client.Get(base + "/healthz")
				switch {
				case err != nil:
					failures.Add(1)
					firstFailure.CompareAndSwap(nil, err.Error())
				case resp.StatusCode != 200:
					failures.Add(1)
					firstFailure.CompareAndSwap(nil, fmt.Sprintf("status %d", resp.StatusCode))
					resp.Body.Close()
				default:
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}
	time.Sleep(time.Second)
	redeploy(t, p, "loadlab")
	time.Sleep(5 * time.Second) // the old container drains after the switch
	close(stop)
	wg.Wait()
	t.Logf("%d requests during a redeploy, %d failed", requests.Load(), failures.Load())
	if requests.Load() < 200 {
		t.Errorf("only %d requests: the traffic was too thin to mean anything", requests.Load())
	}
	if n := failures.Load(); n != 0 {
		t.Errorf("%d failed requests, the first: %v", n, firstFailure.Load())
	}
}

// rollback deploys a second version of loadlab, then rolls back to the
// first, and checks which one answers.
func rollback(t *testing.T, p *piway) {
	dir := p.repos["e2e/loadlab"]
	manifest := filepath.Join(dir, "piway.yaml")
	b, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	write(t, manifest, strings.Replace(string(b), "hello from piway.yaml", "the second version", 1), 0o644)
	p.git(dir, "commit", "-aq", "-m", "second")
	sha := p.push("e2e/loadlab")
	p.waitBuild("the second version", buildTimeout, func(b build) bool { return b.Commit == sha && b.Kind == "" })

	base := p.appURL("loadlab")
	greeting := func() string {
		var i info
		if err := tryJSON(base+"/api/info", &i); err != nil {
			return err.Error()
		}
		return i.Env.GREETING
	}
	eventually(t, "the second version", time.Minute, func() string {
		if g := greeting(); g != "the second version" {
			return "greeting " + g
		}
		return ""
	})

	var queued build
	if code, err := p.api("POST", "/api/v1/apps/loadlab/rollback", nil, &queued); err != nil || code != 202 {
		t.Fatalf("rollback: %d %v", code, err)
	}
	p.waitBuild("rollback", buildTimeout, func(b build) bool { return b.ID == queued.ID })
	eventually(t, "the first version", time.Minute, func() string {
		if g := greeting(); g != "hello from piway.yaml" {
			return "greeting " + g
		}
		return ""
	})
}

// secrets changes a secret through the API and restarts: the container
// gets the new value (#111).
func secrets(t *testing.T, p *piway) {
	const value = "a-longer-secret-value-42"
	if code, err := p.api("PUT", "/api/v1/apps/loadlab/secrets/LOADLAB_TOKEN", map[string]string{"value": value}, nil); err != nil || code != 200 {
		t.Fatalf("set the secret: %d %v", code, err)
	}
	var env struct {
		RestartNeeded bool `json:"restart_needed"`
	}
	if _, err := p.api("GET", "/api/v1/apps/loadlab/env", nil, &env); err != nil || !env.RestartNeeded {
		t.Errorf("after changing a secret: restart_needed = %v (%v)", env.RestartNeeded, err)
	}
	var queued build
	if code, err := p.api("POST", "/api/v1/apps/loadlab/restart", nil, &queued); err != nil || code != 202 {
		t.Fatalf("restart: %d %v", code, err)
	}
	p.waitBuild("restart", buildTimeout, func(b build) bool { return b.ID == queued.ID })
	base := p.appURL("loadlab")
	eventually(t, "the new secret", time.Minute, func() string {
		var i info
		if err := tryJSON(base+"/api/info", &i); err != nil {
			return err.Error()
		}
		if i.Secret.Length != len(value) {
			return fmt.Sprintf("secret length %d, want %d", i.Secret.Length, len(value))
		}
		return ""
	})
	if _, err := p.api("GET", "/api/v1/apps/loadlab/env", nil, &env); err != nil || env.RestartNeeded {
		t.Errorf("after the restart: restart_needed = %v (%v)", env.RestartNeeded, err)
	}
}

var csrf = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// databaseMariaDB makes a note in notes (PHP, MariaDB), backs the database
// up, deletes the note, restores, and finds the note again (#114).
func databaseMariaDB(t *testing.T, p *piway) {
	base := p.appURL("notes")
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	form := func(path string, v url.Values) (*http.Response, string) {
		resp, err := client.PostForm(base+path, v)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	page := func(path string) (int, string) {
		// The jar keeps the session the CSRF token belongs to.
		resp, err := client.Get(base + path)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	_, newPage := page("/notes/new")
	m := csrf.FindStringSubmatch(newPage)
	if m == nil {
		t.Fatalf("no CSRF token on /notes/new:\n%s", tail(newPage, 500))
	}
	resp, _ := form("/notes", url.Values{"csrf": {m[1]}, "title": {"Restore me"}, "body": {"in MariaDB"}})
	if resp.StatusCode != 200 || !strings.Contains(resp.Request.URL.Path, "/notes/") {
		t.Fatalf("creating a note ended at %s with %d", resp.Request.URL, resp.StatusCode)
	}
	notePath := resp.Request.URL.Path

	if out, err := p.cli("db", "backup", "notes"); err != nil {
		t.Fatalf("piway db backup notes: %v\n%s", err, out)
	}
	if resp, _ := form(notePath+"/delete", url.Values{"csrf": {m[1]}}); resp.StatusCode != 200 {
		t.Fatalf("deleting the note: %d", resp.StatusCode)
	}
	if code, _ := page(notePath); code != 404 {
		t.Fatalf("the deleted note: %d, want 404", code)
	}
	if out, err := p.cli("db", "restore", "-yes", "-engine", "mariadb", "notes"); err != nil {
		t.Fatalf("piway db restore: %v\n%s", err, out)
	}
	eventually(t, "the restored note", time.Minute, func() string {
		if code, body := page(notePath); code != 200 || !strings.Contains(body, "Restore me") {
			return fmt.Sprintf("%s: %d", notePath, code)
		}
		return ""
	})
}

// databaseRedis does the same with chat's history in Redis.
func databaseRedis(t *testing.T, p *piway) {
	base := p.appURL("chat")
	post := func(name string) {
		req, _ := http.NewRequest("POST", base+"/api/rooms/e2e/attachments", strings.NewReader("data"))
		req.Header.Set("X-Filename", name)
		req.Header.Set("X-Sender", "e2e")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 201 {
			t.Fatalf("posting to chat: %d", resp.StatusCode)
		}
	}
	history := func() (int, error) {
		var h []map[string]any
		err := tryJSON(base+"/api/rooms/e2e/history", &h)
		return len(h), err
	}
	post("one.txt")
	if out, err := p.cli("db", "backup", "chat"); err != nil {
		t.Fatalf("piway db backup chat: %v\n%s", err, out)
	}
	post("two.txt")
	if n, err := history(); err != nil || n != 2 {
		t.Fatalf("history has %d messages, want 2 (%v)", n, err)
	}
	if out, err := p.cli("db", "restore", "-yes", "-engine", "redis", "chat"); err != nil {
		t.Fatalf("piway db restore: %v\n%s", err, out)
	}
	eventually(t, "the restored history", time.Minute, func() string {
		// While chat reconnects to Redis, the answer is an error.
		if n, err := history(); err != nil || n != 1 {
			return fmt.Sprintf("history has %d messages, want 1 (%v)", n, err)
		}
		return ""
	})
}

// killNine kills piway without warning. The apps keep running in Docker;
// a new piway puts their routes back and remembers its builds.
func killNine(t *testing.T, p *piway) {
	var before struct {
		Builds []build `json:"builds"`
	}
	if _, err := p.api("GET", "/api/v1/builds", nil, &before); err != nil {
		t.Fatal(err)
	}
	p.stop(true)
	out, _ := exec.Command("docker", "ps", "-q", "--filter", "label=piway.app=loadlab", "--filter", "status=running").Output()
	if strings.TrimSpace(string(out)) == "" {
		t.Error("loadlab's container stopped with piway: apps should outlive it")
	}
	p.start()
	base := p.appURL("loadlab")
	eventually(t, "loadlab after kill -9", 30*time.Second, func() string {
		if code, _, err := tryGet(base + "/healthz"); err != nil || code != 200 {
			return fmt.Sprintf("status %d %v", code, err)
		}
		return ""
	})
	var after struct {
		Builds []build `json:"builds"`
	}
	if _, err := p.api("GET", "/api/v1/builds", nil, &after); err != nil {
		t.Fatal(err)
	}
	if len(after.Builds) != len(before.Builds) {
		t.Errorf("%d builds before kill -9, %d after", len(before.Builds), len(after.Builds))
	}
	// And it can still deploy.
	redeploy(t, p, "loadlab")
	if code, _ := get(t, base+"/healthz"); code != 200 {
		t.Errorf("after a redeploy by the new piway: %d", code)
	}
}

// privateRepo deploys a real private repository over SSH with a deploy
// key. It needs a test repository and its key in CI secrets
// (E2E_PRIVATE_REPO=owner/name, E2E_DEPLOY_KEY=the private key), and
// skips without them (#108).
func privateRepo(t *testing.T, p *piway) {
	repo, key := os.Getenv("E2E_PRIVATE_REPO"), os.Getenv("E2E_DEPLOY_KEY")
	if repo == "" || key == "" {
		t.Skip("E2E_PRIVATE_REPO and E2E_DEPLOY_KEY are not set")
	}
	file := strings.ToLower(strings.Replace(repo, "/", "_", 1))
	write(t, filepath.Join(p.cfgDir(), "keys", file), strings.TrimSpace(key)+"\n", 0o600)
	write(t, filepath.Join(p.cfgDir(), "apps.yaml"), p.appsYAML()+"  - repo: "+repo+"\n    branch: main\n    auth: deploy-key\n", 0o600)

	eventually(t, "piway reloading apps.yaml", 20*time.Second, func() string {
		if out, err := p.cli("repo", "test", repo); err != nil || !strings.Contains(out, "can read") {
			return fmt.Sprintf("%v: %s", err, strings.TrimSpace(out))
		}
		return ""
	})
	var queued build
	if code, err := p.api("POST", "/api/v1/deploys", map[string]string{"repo": repo, "branch": "main"}, &queued); err != nil || code != 202 {
		t.Fatalf("deploy %s: %d %v", repo, code, err)
	}
	b := p.waitBuild("private repository", buildTimeout, func(b build) bool { return b.ID == queued.ID })
	p.apps = append(p.apps, b.App)
}

// dashboard runs the Playwright checks against the running piway.
func dashboard(t *testing.T, p *piway) {
	dir := "dashboard"
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "playwright")); err != nil {
		skipUnlessCI(t, "e2e/dashboard has no node_modules: run npm ci there (make e2e-deps)")
	}
	runNode := func(script string, env ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "node", script)
		cmd.WaitDelay = 10 * time.Second
		cmd.Dir = dir
		cmd.Env = append(append(os.Environ(), "PIWAY_URL=http://"+p.addr), env...)
		out, err := cmd.CombinedOutput()
		t.Logf("%s", out)
		if err != nil {
			t.Fatalf("%s failed: %v", script, err)
		}
	}
	runNode("check.mjs")

	// Then what a person does, signed in: set a password (a running piway
	// picks it up at once) and run the flows against the app loadlab.
	const password = "e2e-dashboard-password"
	set := exec.Command(p.bin, "-set-password")
	set.Env = p.env()
	set.Stdin = strings.NewReader(password + "\n")
	if out, err := set.CombinedOutput(); err != nil {
		t.Fatalf("piway -set-password: %v: %s", err, out)
	}
	runNode("flows.mjs", "PIWAY_PASSWORD="+password, "PIWAY_APP=loadlab", "PIWAY_SECRET=LOADLAB_TOKEN")
}

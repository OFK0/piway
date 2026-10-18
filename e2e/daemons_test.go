//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// caddyProc is a Caddy of this test's own, with its own admin port and
// directories: restarting it touches nobody's service.
type caddyProc struct {
	t     *testing.T
	p     *piway
	admin string
	cfg   string
	cmd   *exec.Cmd
	log   bytes.Buffer
}

func (c *caddyProc) start() {
	c.t.Helper()
	c.cmd = exec.Command("caddy", "run", "--config", c.cfg)
	c.cmd.Env = c.p.env()
	c.cmd.Stdout, c.cmd.Stderr = &c.log, &c.log
	if err := c.cmd.Start(); err != nil {
		c.t.Fatal(err)
	}
	eventually(c.t, "Caddy's admin API", 30*time.Second, func() string {
		if code, _, err := tryGet("http://" + c.admin + "/config/"); err != nil || code != 200 {
			return fmt.Sprintf("status %d %v", code, err)
		}
		return ""
	})
}

// kill ends Caddy without warning, with everything it had loaded.
func (c *caddyProc) kill() {
	if c.cmd != nil && c.cmd.Process != nil {
		c.cmd.Process.Kill()
		c.cmd.Wait()
		c.cmd = nil
	}
}

// viaProxy asks Caddy's listener for path as the app's host name.
func viaProxy(listen, host, path string) (int, error) {
	req, err := http.NewRequest("GET", "http://"+listen+path, nil)
	if err != nil {
		return 0, err
	}
	req.Host = host
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// TestDaemons restarts what piway depends on while it runs: Caddy, and
// with E2E_RESTART_DOCKER=1 the Docker daemon. After each, the apps are
// reachable again by themselves and a deploy works. It needs `caddy` in
// PATH:
//
//	make e2e E2E_RUN='TestDaemons$'
//
// Restarting Docker stops every container of the daemon for a moment, and
// is `systemctl --user restart docker` (rootless Docker), so it is off
// unless asked for.
func TestDaemons(t *testing.T) {
	requireDocker(t)
	caddyBin, err := exec.LookPath("caddy")
	if err != nil {
		t.Skipf("no caddy in PATH: %v", err)
	}
	_ = caddyBin
	p := newPiway(t)
	t.Cleanup(p.cleanup)
	admin := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	const domain = "e2e.test"
	p.extra = []string{
		"PIWAY_PROXY_DOMAIN=" + domain, "PIWAY_PROXY_LISTEN=" + listen,
		"PIWAY_PROXY_TLS=false", "PIWAY_CADDY_ADMIN=" + admin,
	}
	cfg := filepath.Join(p.home, "caddy.json")
	write(t, cfg, fmt.Sprintf(`{"admin":{"listen":%q}}`, admin), 0o600)
	cd := &caddyProc{t: t, p: p, admin: admin, cfg: cfg}
	cd.start()
	t.Cleanup(cd.kill)
	t.Cleanup(func() {
		p.stop(false)
		if t.Failed() {
			t.Logf("piway's log:\n%s\nCaddy's log:\n%s", tail(p.log.String(), 6000), tail(cd.log.String(), 3000))
		}
	})

	write(t, filepath.Join(p.cfgDir(), "apps.yaml"), "deploy:\n", 0o600)
	p.repo("e2e/loadlab", filepath.Join("..", "examples", "loadlab"))
	p.apps = append(p.apps, "loadlab")
	write(t, filepath.Join(p.cfgDir(), "secrets", "loadlab.env"), "LOADLAB_TOKEN="+secretValue("loadlab")+"\n", 0o600)
	p.start()
	deployExample(t, p, "loadlab", true)

	host := "loadlab." + domain
	proxied := func() string {
		if code, err := viaProxy(listen, host, "/healthz"); err != nil || code != 200 {
			return fmt.Sprintf("status %d %v", code, err)
		}
		return ""
	}
	direct := func() string {
		if code, _, err := tryGet(p.appURL("loadlab") + "/healthz"); err != nil || code != 200 {
			return fmt.Sprintf("status %d %v", code, err)
		}
		return ""
	}
	eventually(t, "loadlab through Caddy", time.Minute, proxied)

	t.Run("caddy-restart", func(t *testing.T) {
		cd.kill()
		if proxied() == "" {
			t.Error("loadlab answers through Caddy after Caddy was killed")
		}
		// Apps stay reachable on their own addresses while Caddy is away.
		if msg := direct(); msg != "" {
			t.Errorf("loadlab on its own address while Caddy is down: %s", msg)
		}
		eventually(t, "piway to notice Caddy is gone", time.Minute, func() string {
			if !strings.Contains(p.log.String(), "caddy unreachable") {
				return "no log line yet"
			}
			return ""
		})
		cd.start() // a fresh Caddy that knows nothing: piway puts the routes back
		eventually(t, "loadlab through the new Caddy", 2*time.Minute, proxied)
		if !strings.Contains(p.log.String(), "caddy reachable again") {
			t.Error("piway did not log that Caddy is reachable again")
		}
		redeploy(t, p, "loadlab")
		eventually(t, "loadlab through Caddy after a redeploy", time.Minute, proxied)
	})

	t.Run("docker-restart", func(t *testing.T) {
		if os.Getenv("E2E_RESTART_DOCKER") == "" {
			t.Skip("set E2E_RESTART_DOCKER=1 to restart the Docker daemon (systemctl --user restart docker)")
		}
		if out, err := exec.Command("systemctl", "--user", "restart", "docker").CombinedOutput(); err != nil {
			t.Fatalf("systemctl --user restart docker: %v: %s", err, out)
		}
		eventually(t, "the Docker daemon", 2*time.Minute, func() string {
			if out, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
				return strings.TrimSpace(string(out))
			}
			return ""
		})
		// Docker restarts the container (unless-stopped) on a new port; the
		// route follows once piway's event stream or its 30 s sweep sees it.
		eventually(t, "loadlab after Docker restarted", 3*time.Minute, direct)
		eventually(t, "loadlab through Caddy after Docker restarted", time.Minute, proxied)
		if code, _, err := tryGet(p.url("/healthz")); err != nil || code != 200 {
			t.Errorf("piway after Docker restarted: %d %v", code, err)
		}
		redeploy(t, p, "loadlab")
		eventually(t, "loadlab after a redeploy by the same piway", time.Minute, direct)
	})
}

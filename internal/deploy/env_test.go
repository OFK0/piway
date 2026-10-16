package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ofk0/piway/internal/databases"
	"github.com/ofk0/piway/internal/manifest"
	"github.com/ofk0/piway/internal/secrets"
)

const envManifest = `piway: 1
name: guestbook
build:
  dockerfile: Dockerfile
run:
  port: 3000
  env:
    LOG_LEVEL: info
  secrets: [API_KEY, SESSION_SECRET]
`

func TestEnv(t *testing.T) {
	ctx := context.Background()
	store := &secrets.Store{Dir: filepath.Join(t.TempDir(), "secrets")}
	rt := fakeRuntime{}
	h := start(t, t.TempDir(), Options{Runtime: rt, Secrets: store})
	h.steps.mu.Lock()
	h.steps.manifest = envManifest
	h.steps.mu.Unlock()

	if _, err := h.p.Env(ctx, "guestbook", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("before any build: %v", err)
	}
	if err := h.p.SetSecret("guestbook", "API_KEY", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("set before any build: %v", err)
	}

	id := push(t, h.p, "aaaaaaaaaaaa1")
	started(t, h.steps)
	h.steps.release <- nil
	waitState(t, h.p, id, Succeeded)

	env, err := h.p.Env(ctx, "guestbook", false)
	if err != nil {
		t.Fatal(err)
	}
	if env.Build != id || len(env.Env) != 1 || env.Env[0] != (EnvVar{"LOG_LEVEL", "info"}) || env.File != store.Path("guestbook") {
		t.Errorf("env = %+v", env)
	}
	if len(env.Secrets) != 2 || env.Secrets[0].Set || !env.Secrets[0].Declared || env.RestartNeeded {
		t.Errorf("secrets before any value: %+v", env.Secrets)
	}

	if err := h.p.SetSecret("guestbook", "API_KEY", "k1"); err != nil {
		t.Fatal(err)
	}
	if err := h.p.SetSecret("guestbook", "API_KEY", "two\nlines"); !errors.Is(err, secrets.ErrInvalid) {
		t.Errorf("multi-line value: %v", err)
	}
	os.WriteFile(store.Path("guestbook"), append(mustRead(t, store.Path("guestbook")), "OLD_TOKEN=gone\n"...), 0o600)

	env, _ = h.p.Env(ctx, "guestbook", false)
	if !env.Secrets[0].Set || env.Secrets[0].Value != nil || env.Secrets[1].Set || env.ValuesShown {
		t.Errorf("without values: %+v", env.Secrets)
	}
	if len(env.Secrets) != 3 || env.Secrets[2].Name != "OLD_TOKEN" || env.Secrets[2].Declared {
		t.Errorf("an undeclared secret in the file: %+v", env.Secrets)
	}
	env, _ = h.p.Env(ctx, "guestbook", true)
	if v := env.Secrets[0].Value; v == nil || *v != "k1" || env.Secrets[1].Value != nil || !env.ValuesShown {
		t.Errorf("with values: %+v", env.Secrets)
	}

	// The container was started with API_KEY=k1 and no SESSION_SECRET.
	started := map[string]string{"API_KEY": "k1"}
	rt["guestbook"] = Container{State: "running", SecretsDigest: secrets.Digest([]string{"API_KEY", "SESSION_SECRET"}, started)}
	if env, _ := h.p.Env(ctx, "guestbook", false); env.RestartNeeded {
		t.Error("restart needed with the same values")
	}
	h.p.SetSecret("guestbook", "SESSION_SECRET", "s")
	if env, _ := h.p.Env(ctx, "guestbook", false); !env.RestartNeeded {
		t.Error("no restart needed after a change")
	}
	h.p.RemoveSecret("guestbook", "SESSION_SECRET")
	if env, _ := h.p.Env(ctx, "guestbook", false); env.RestartNeeded {
		t.Error("restart needed after going back to the started values")
	}
	rt["guestbook"] = Container{State: "running"} // started before the label existed
	h.p.SetSecret("guestbook", "SESSION_SECRET", "s")
	if env, _ := h.p.Env(ctx, "guestbook", false); env.RestartNeeded {
		t.Error("restart needed for a container without a digest")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestExecStepsEnv(t *testing.T) {
	store := &secrets.Store{Dir: t.TempDir()}
	s := ExecSteps{Secrets: store}
	m, err := manifest.Parse("piway.yaml", []byte(envManifest))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.env(m); err == nil || !strings.Contains(err.Error(), "API_KEY, SESSION_SECRET") {
		t.Errorf("missing secrets: %v", err)
	}
	store.Set("guestbook", "API_KEY", "k")
	store.Set("guestbook", "SESSION_SECRET", "s")
	env, digest, err := s.env(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(env, " ") != "LOG_LEVEL=info API_KEY=k SESSION_SECRET=s" {
		t.Errorf("env %q", env)
	}
	if digest == "" || digest != secrets.Digest(m.Run.Secrets, map[string]string{"API_KEY": "k", "SESSION_SECRET": "s"}) {
		t.Errorf("digest %q", digest)
	}
}

type fakeDatabases struct{ ready bool }

func (f fakeDatabases) Env(app string, engines []string) ([]databases.Var, bool, error) {
	if !f.ready {
		return nil, false, nil
	}
	return []databases.Var{{Name: "DATABASE_URL", Value: "postgres://x"}, {Name: "POSTGRES_URL", Value: "postgres://x"}}, true, nil
}

func TestEnvInjected(t *testing.T) {
	ctx := context.Background()
	db := &fakeDatabases{}
	h := start(t, t.TempDir(), Options{Databases: db})
	h.steps.mu.Lock()
	h.steps.manifest = "piway: 1\nname: guestbook\nbuild:\nrun:\n  port: 1\n  databases: [postgres]\n"
	h.steps.mu.Unlock()
	id := push(t, h.p, "aaaaaaaaaaaa1")
	started(t, h.steps)
	h.steps.release <- nil
	waitState(t, h.p, id, Succeeded)

	env, err := h.p.Env(ctx, "guestbook", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Injected) != 2 || env.Injected[0].Set || env.Injected[0].Value != nil || env.Injected[0].From != "postgres" {
		t.Errorf("before set up: %+v", env.Injected)
	}
	db.ready = true
	env, _ = h.p.Env(ctx, "guestbook", true)
	if v := env.Injected[0].Value; !env.Injected[0].Set || v == nil || *v != "postgres://x" {
		t.Errorf("with values: %+v", env.Injected)
	}
	env, _ = h.p.Env(ctx, "guestbook", false)
	if env.Injected[0].Value != nil || !env.Injected[0].Set {
		t.Errorf("without values: %+v", env.Injected)
	}
}

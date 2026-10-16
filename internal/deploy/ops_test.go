package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ofk0/piway/internal/gitauth"
	"github.com/ofk0/piway/internal/webhook"
)

func deployed(t *testing.T, h *harness, commit string) Build {
	t.Helper()
	id := push(t, h.p, commit)
	started(t, h.steps)
	h.steps.release <- nil
	return waitState(t, h.p, id, Succeeded)
}

func TestRestartReleasesTheLiveVersion(t *testing.T) {
	h := start(t, t.TempDir(), Options{})
	if _, err := h.p.Restart("guestbook"); !errors.Is(err, ErrNotFound) {
		t.Errorf("restart before any deploy: %v", err)
	}
	first := deployed(t, h, "aaaaaaaaaaaa1")
	deployed(t, h, "bbbbbbbbbbbb2")
	rb, err := h.p.Rollback("guestbook")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, h.p, rb.ID, Succeeded)

	// Live is a rollback to the first build: a restart releases that image
	// with that build's piway.yaml, without building.
	r, err := h.p.Restart("guestbook")
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != KindRestart || r.RollbackOf != first.ID || r.Image != first.Image || r.Commit != first.Commit {
		t.Errorf("restart = %+v, want the first build's image", r)
	}
	b := waitState(t, h.p, r.ID, Succeeded)
	select {
	case img := <-h.steps.started:
		t.Errorf("a restart built %s", img)
	default:
	}
	log, _, _, _ := h.p.Follow(b.ID)
	if !strings.Contains(string(log), "==> Restarting guestbook with piway/guestbook:aaaaaaaaaaaa") || !strings.Contains(string(log), "==> Restarted guestbook") {
		t.Errorf("log:\n%s", log)
	}
	if rel := h.steps.releasedImages(); rel[len(rel)-1] != first.Image {
		t.Errorf("released %v", rel)
	}
	// A redeploy after a restart rebuilds the commit that runs.
	rd, err := h.p.Redeploy("guestbook")
	if err != nil || rd.Commit != first.Commit {
		t.Errorf("redeploy after restart: %+v, %v", rd, err)
	}
}

func TestDeployNow(t *testing.T) {
	var asked []string
	resolve := func(_ context.Context, src gitauth.Source, branch string) (string, error) {
		asked = append(asked, src.URL+" "+branch+" "+src.Auth)
		return strings.Repeat("c", 40), nil
	}
	targets := []webhook.Target{{Repo: "OFK0/guestbook", Branch: "main"}, {Repo: "OFK0/blog", Branch: "main", Auth: gitauth.DeployKey}}
	h := start(t, t.TempDir(), Options{Targets: webhook.StaticTargets(targets), ResolveHead: resolve})
	ctx := context.Background()

	// A configured branch that was never built: clone from GitHub.
	b, err := h.p.DeployNow(ctx, DeployRequest{Repo: "ofk0/BLOG", Branch: "main"}, "api")
	if err != nil {
		t.Fatal(err)
	}
	if b.Repo != "OFK0/blog" || b.Commit != strings.Repeat("c", 40) || b.Pusher != "api" || b.CloneURL != "https://github.com/OFK0/blog.git" {
		t.Errorf("build %+v", b)
	}
	if len(asked) != 1 || asked[0] != "https://github.com/OFK0/blog.git main deploy-key" {
		t.Errorf("resolved %v", asked)
	}
	started(t, h.steps)
	h.steps.release <- nil
	waitState(t, h.p, b.ID, Succeeded)
	h.steps.mu.Lock()
	if len(h.steps.auths) != 1 || h.steps.auths[0] != gitauth.DeployKey {
		t.Errorf("fetched with auth %q, want the repository's deploy key", h.steps.auths)
	}
	h.steps.mu.Unlock()

	// By app name, with an exact commit: no lookup, the app's clone URL.
	sha := strings.Repeat("d", 40)
	b, err = h.p.DeployNow(ctx, DeployRequest{App: "guestbook", Commit: sha}, "api")
	if err != nil {
		t.Fatal(err)
	}
	if b.Commit != sha || len(asked) != 1 || b.CloneURL != "https://github.com/OFK0/blog.git" {
		t.Errorf("build %+v, resolved %v", b, asked)
	}

	for name, r := range map[string]DeployRequest{
		"unknown app":         {App: "nope"},
		"unconfigured branch": {Repo: "OFK0/guestbook", Branch: "dev"},
		"unconfigured repo":   {Repo: "someone/else", Branch: "main"},
	} {
		if _, err := h.p.DeployNow(ctx, r, "api"); !errors.Is(err, ErrUnknownTarget) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := h.p.DeployNow(ctx, DeployRequest{App: "guestbook", Commit: "abc1234"}, "api"); err == nil || !strings.Contains(err.Error(), "40-character") {
		t.Errorf("short commit: %v", err)
	}

	tg := h.p.Targets()
	if len(tg) != 2 || tg[0].App != "" || tg[1].App != "guestbook" {
		t.Errorf("targets %+v", tg)
	}
}

type logRuntime struct{ fakeRuntime }

func (logRuntime) Logs(_ context.Context, app string, lines int) (string, error) {
	return app + " says hi", nil
}

func TestLogs(t *testing.T) {
	h := start(t, t.TempDir(), Options{Runtime: logRuntime{}})
	if _, err := h.p.Logs(context.Background(), "guestbook", 10); !errors.Is(err, ErrNotFound) {
		t.Errorf("before a deploy: %v", err)
	}
	deployed(t, h, "aaaaaaaaaaaa1")
	if out, err := h.p.Logs(context.Background(), "guestbook", 10); err != nil || out != "guestbook says hi" {
		t.Errorf("logs %q, %v", out, err)
	}

	site := startSite(t, siteManifest, map[string]string{"public/index.html": "x"})
	waitState(t, site.p, push(t, site.p, "aaaaaaaaaaaa1"), Succeeded)
	if _, err := site.p.Logs(context.Background(), "guestbook", 10); !errors.Is(err, ErrNotRunning) {
		t.Errorf("static site: %v", err)
	}
}

func TestDeployPath(t *testing.T) {
	targets := webhook.StaticTargets{
		{Repo: "OFK0/mono", Branch: "main", Path: "apps/web"},
		{Repo: "OFK0/mono", Branch: "main", Path: "apps/api"},
	}
	h := start(t, t.TempDir(), Options{Targets: targets})
	h.steps.mu.Lock()
	h.steps.subdir = "apps/web"
	h.steps.mu.Unlock()

	// Two apps of one branch queue side by side; neither replaces the other.
	for _, path := range []string{"apps/web", "apps/api"} {
		if err := h.p.Deploy(webhook.Request{Repo: "OFK0/mono", Branch: "main", Commit: strings.Repeat("a", 40), CloneURL: "x", Path: path}); err != nil {
			t.Fatal(err)
		}
	}
	builds := h.p.List()
	if len(builds) != 2 || builds[0].State == Cancelled || builds[1].State == Cancelled {
		t.Fatalf("builds %+v", builds)
	}
	web := builds[0]
	if web.Path != "apps/web" {
		web = builds[1]
	}
	started(t, h.steps)
	h.steps.release <- nil
	waitState(t, h.p, web.ID, Succeeded)

	// The api build finds no apps/api directory in the checkout.
	api := builds[0]
	if api.Path != "apps/api" {
		api = builds[1]
	}
	b := waitState(t, h.p, api.ID, Failed)
	if !strings.Contains(b.Error, "apps/api is not a directory") {
		t.Errorf("error %q", b.Error)
	}

	// DeployNow and Targets know the path.
	tt := h.p.Targets()
	if len(tt) != 2 || tt[0].Path != "apps/web" || tt[0].App != "guestbook" {
		t.Errorf("targets %+v", tt)
	}
	if _, err := h.p.DeployNow(context.Background(), DeployRequest{Repo: "OFK0/mono", Branch: "main", Path: "apps/nope", Commit: strings.Repeat("b", 40)}, "api"); !errors.Is(err, ErrUnknownTarget) {
		t.Errorf("unknown path: %v", err)
	}
	rd, err := h.p.DeployNow(context.Background(), DeployRequest{App: "guestbook", Commit: strings.Repeat("c", 40)}, "api")
	if err != nil || rd.Path != "apps/web" {
		t.Errorf("deploy by app: %+v %v", rd, err)
	}
}

func TestDeployGitLab(t *testing.T) {
	var asked []gitauth.Source
	resolve := func(_ context.Context, src gitauth.Source, branch string) (string, error) {
		asked = append(asked, src)
		return strings.Repeat("e", 40), nil
	}
	targets := []webhook.Target{
		{Repo: "grp/sub/app", Branch: "main", Provider: "gitlab"},
		{Repo: "grp/self", Branch: "main", Provider: "gitlab", Host: "git.example.com", Auth: gitauth.DeployKey},
	}
	h := start(t, t.TempDir(), Options{Targets: webhook.StaticTargets(targets), ResolveHead: resolve})
	ctx := context.Background()

	// gitlab.com, public: cloned from its HTTPS address.
	b, err := h.p.DeployNow(ctx, DeployRequest{Repo: "grp/sub/app", Branch: "main"}, "api")
	if err != nil || b.CloneURL != "https://gitlab.com/grp/sub/app.git" {
		t.Fatalf("build %+v, %v", b, err)
	}
	started(t, h.steps)
	h.steps.release <- nil
	waitState(t, h.p, b.ID, Succeeded)

	// A self-hosted server with a deploy key: asked, and fetched, as that.
	b, err = h.p.DeployNow(ctx, DeployRequest{Repo: "grp/self", Branch: "main"}, "api")
	if err != nil || b.CloneURL != "https://git.example.com/grp/self.git" {
		t.Fatalf("build %+v, %v", b, err)
	}
	started(t, h.steps)
	h.steps.release <- nil
	waitState(t, h.p, b.ID, Succeeded)

	if len(asked) != 2 || asked[0].Provider != "gitlab" || asked[1].Host != "git.example.com" || asked[1].Auth != gitauth.DeployKey {
		t.Errorf("head lookups %+v", asked)
	}
	h.steps.mu.Lock()
	defer h.steps.mu.Unlock()
	if len(h.steps.sources) != 2 || h.steps.sources[0] != "gitlab/" || h.steps.sources[1] != "gitlab/git.example.com" {
		t.Errorf("fetched as %q", h.steps.sources)
	}
}

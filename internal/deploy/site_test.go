package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const siteManifest = `piway: 1
name: guestbook
static:
  dir: public
  spa: true
`

const builtSiteManifest = `piway: 1
name: guestbook
static:
  dir: dist
  build:
    image: node:22-alpine
    command: npm ci && npm run build
`

func startSite(t *testing.T, manifest string, files map[string]string) *harness {
	t.Helper()
	steps := newFake()
	steps.manifest = manifest
	steps.siteFiles = files
	h := start(t, t.TempDir(), Options{Steps: steps})
	h.steps = steps
	return h
}

func readSite(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStaticSiteDeploys(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secret, []byte("do not serve"), 0o600)
	h := startSite(t, siteManifest, map[string]string{
		"public/index.html":  "home",
		"public/css/app.css": "css",
		"public/leak":        "->" + secret, // a symbolic link out of the checkout
		"public/.git/config": "git",
		"README.md":          "not in static.dir",
	})
	id := push(t, h.p, "aaaaaaaaaaaa1")
	b := waitState(t, h.p, id, Succeeded)
	if b.Site != id || b.Image != "" || b.App != "guestbook" {
		t.Errorf("build = %+v, want Site = its own id and no image", b)
	}

	dir := filepath.Join(h.dir, "sites", "guestbook", id)
	if got := h.steps.siteDirs; len(got) != 1 || got[0] != dir {
		t.Fatalf("served %v, want %s", got, dir)
	}
	if got := readSite(t, dir, "index.html"); got != "home aaaaaaaaaaaa1" {
		t.Errorf("index.html = %q", got)
	}
	readSite(t, dir, "css/app.css")
	for _, name := range []string{"leak", ".git", "README.md"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s was copied", name)
		}
	}
	log, _, _, _ := h.p.Follow(id)
	if !strings.Contains(string(log), "WARNING: skipped leak") {
		t.Errorf("no warning for the skipped link:\n%s", log)
	}

	sites := h.p.LiveSites()
	if len(sites) != 1 || sites[0] != (Site{App: "guestbook", Dir: dir, SPA: true}) {
		t.Errorf("LiveSites = %+v", sites)
	}
}

func TestStaticSiteBuildStep(t *testing.T) {
	h := startSite(t, builtSiteManifest, map[string]string{"package.json": "{}"})
	id := push(t, h.p, "aaaaaaaaaaaa1")
	waitState(t, h.p, id, Succeeded)
	if got := readSite(t, filepath.Join(h.dir, "sites", "guestbook", id), "built.txt"); got != "built" {
		t.Errorf("built.txt = %q", got)
	}
	log, _, _, _ := h.p.Follow(id)
	if !strings.Contains(string(log), "site build in node:22-alpine") || !strings.Contains(string(log), "has no index.html") {
		t.Errorf("log:\n%s", log)
	}
}

func TestStaticSiteFailures(t *testing.T) {
	t.Run("missing dir", func(t *testing.T) {
		h := startSite(t, siteManifest, map[string]string{"index.html": "at the root, not in public/"})
		b := waitState(t, h.p, push(t, h.p, "aaaaaaaaaaaa1"), Failed)
		if !strings.Contains(b.Error, `static.dir "public" does not exist`) {
			t.Errorf("error = %q", b.Error)
		}
	})
	t.Run("dir is a link out of the checkout", func(t *testing.T) {
		h := startSite(t, siteManifest, map[string]string{"public": "->" + t.TempDir()})
		b := waitState(t, h.p, push(t, h.p, "aaaaaaaaaaaa1"), Failed)
		if b.Error == "" || len(h.steps.siteDirs) != 0 {
			t.Errorf("served a directory outside the checkout: %+v", b)
		}
	})
	t.Run("build step fails", func(t *testing.T) {
		h := startSite(t, builtSiteManifest, nil)
		h.steps.siteFail = errors.New("npm ERR! missing script: build")
		b := waitState(t, h.p, push(t, h.p, "aaaaaaaaaaaa1"), Failed)
		if !strings.Contains(b.Error, "missing script") || len(h.steps.siteDirs) != 0 {
			t.Errorf("build = %+v, served %v", b, h.steps.siteDirs)
		}
	})
}

func TestStaticSiteRollbackAndPrune(t *testing.T) {
	h := startSite(t, siteManifest, map[string]string{"public/index.html": "v"})
	var ids []string
	for _, c := range []string{"aaaaaaaaaaa01", "aaaaaaaaaaa02", "aaaaaaaaaaa03", "aaaaaaaaaaa04", "aaaaaaaaaaa05"} {
		id := push(t, h.p, c)
		waitState(t, h.p, id, Succeeded)
		ids = append(ids, id)
	}
	entries, _ := os.ReadDir(filepath.Join(h.dir, "sites", "guestbook"))
	var kept []string
	for _, e := range entries {
		kept = append(kept, e.Name())
	}
	if want := ids[2:]; !slices.Equal(kept, want) {
		t.Errorf("kept sites %v, want the %d newest %v", kept, keepSites, want)
	}

	rb, err := h.p.Rollback("guestbook")
	if err != nil {
		t.Fatal(err)
	}
	if rb.Site != ids[3] || rb.RollbackOf != ids[3] {
		t.Errorf("rollback = %+v, want the site of %s", rb, ids[3])
	}
	waitState(t, h.p, rb.ID, Succeeded)
	dirs := h.steps.siteDirs
	if got := dirs[len(dirs)-1]; got != filepath.Join(h.dir, "sites", "guestbook", ids[3]) {
		t.Errorf("rollback served %s", got)
	}
	if got := readSite(t, h.p.LiveSites()[0].Dir, "index.html"); got != "v aaaaaaaaaaa04" {
		t.Errorf("live site after rollback serves %q", got)
	}

	// A rollback to a pruned site fails instead of serving nothing.
	os.RemoveAll(filepath.Join(h.dir, "sites", "guestbook", ids[4]))
	rb, err = h.p.Rollback("guestbook")
	if err != nil {
		t.Fatal(err)
	}
	b := waitState(t, h.p, rb.ID, Failed)
	if !strings.Contains(b.Error, "no longer exists") {
		t.Errorf("error = %q", b.Error)
	}
}

func TestAppSwitchesFromContainerToSite(t *testing.T) {
	h := start(t, t.TempDir(), Options{})
	id := push(t, h.p, "aaaaaaaaaaaa1")
	started(t, h.steps)
	h.steps.release <- nil
	waitState(t, h.p, id, Succeeded)

	h.steps.mu.Lock()
	h.steps.manifest = siteManifest
	h.steps.siteFiles = map[string]string{"public/index.html": "now static"}
	h.steps.mu.Unlock()
	site := push(t, h.p, "bbbbbbbbbbbb2")
	waitState(t, h.p, site, Succeeded)
	if s := h.p.LiveSites(); len(s) != 1 || s[0].App != "guestbook" {
		t.Errorf("LiveSites = %+v", s)
	}

	// Rolling back returns to the container's image.
	rb, err := h.p.Rollback("guestbook")
	if err != nil {
		t.Fatal(err)
	}
	if rb.Image != "piway/guestbook:aaaaaaaaaaaa" || rb.Site != "" {
		t.Errorf("rollback = %+v, want the image", rb)
	}
	waitState(t, h.p, rb.ID, Succeeded)
	if s := h.p.LiveSites(); len(s) != 0 {
		t.Errorf("LiveSites after rolling back to a container = %+v", s)
	}
	if apps, _ := h.p.Apps(context.Background()); apps[0].Live.Site != "" {
		t.Errorf("live = %+v", apps[0].Live)
	}
}

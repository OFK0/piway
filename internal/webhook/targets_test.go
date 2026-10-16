package webhook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeApps(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "apps.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadTargets(t *testing.T) {
	got, err := LoadTargets(writeApps(t, `
deploy:
  - repo: OFK0/guestbook
    branch: main
  - repo: OFK0/blog
    branch: release
    auth: deploy-key
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Target{{Repo: "OFK0/guestbook", Branch: "main"}, {Repo: "OFK0/blog", Branch: "release", Auth: "deploy-key"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestLoadTargetsMissingOrEmpty(t *testing.T) {
	if got, err := LoadTargets(filepath.Join(t.TempDir(), "none.yaml")); err != nil || got != nil {
		t.Errorf("missing file: %v, %v; want no targets and no error", got, err)
	}
	if got, err := LoadTargets(writeApps(t, "# nothing yet\n")); err != nil || len(got) != 0 {
		t.Errorf("empty file: %v, %v", got, err)
	}
}

func TestLoadTargetsRejects(t *testing.T) {
	tests := map[string]string{
		"typo":         "deploy:\n  - repo: a/b\n    brach: main\n",
		"no branch":    "deploy:\n  - repo: a/b\n",
		"bad repo":     "deploy:\n  - repo: https://github.com/a/b\n    branch: main\n",
		"duplicate":    "deploy:\n  - {repo: a/b, branch: main}\n  - {repo: A/B, branch: main}\n",
		"unknown key":  "apps:\n  - repo: a/b\n",
		"bad auth":     "deploy:\n  - {repo: a/b, branch: main, auth: password}\n",
		"mixed auth":   "deploy:\n  - {repo: a/b, branch: main, auth: token}\n  - {repo: A/b, branch: dev}\n",
		"path up":      "deploy:\n  - {repo: a/b, branch: main, path: ../x}\n",
		"path root":    "deploy:\n  - {repo: a/b, branch: main, path: /x}\n",
		"path unclean": "deploy:\n  - {repo: a/b, branch: main, path: a/../b}\n",
		"same path":    "deploy:\n  - {repo: a/b, branch: main, path: x}\n  - {repo: a/b, branch: main, path: x}\n",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := LoadTargets(writeApps(t, content))
			if err == nil || !strings.Contains(err.Error(), "apps.yaml") {
				t.Errorf("err = %v, want an error naming the file", err)
			}
		})
	}
}

func TestAuthOf(t *testing.T) {
	targets := []Target{{Repo: "o/pub", Branch: "main"}, {Repo: "O/Priv", Branch: "main", Auth: "token"}}
	if got := AuthOf(targets, "o/priv"); got != "token" {
		t.Errorf("AuthOf(o/priv) = %q", got)
	}
	if got := AuthOf(targets, "o/pub") + AuthOf(targets, "o/other"); got != "" {
		t.Errorf("public and unlisted: %q", got)
	}
}

func TestTargetPaths(t *testing.T) {
	got, err := LoadTargets(writeApps(t, "deploy:\n  - {repo: a/b, branch: main, path: apps/web}\n  - {repo: a/b, branch: main, path: apps/api}\n  - {repo: a/b, branch: main}\n"))
	if err != nil || len(got) != 3 || got[0].Path != "apps/web" {
		t.Fatalf("%+v %v", got, err)
	}
	if got[0].Name() != "a/b@main:apps/web" || got[2].Name() != "a/b@main" {
		t.Errorf("names %q %q", got[0].Name(), got[2].Name())
	}
	for p, ok := range map[string]bool{"": true, "apps/web": true, "a.b/c_d-e": true, ".": false, "..": false, "a//b": false, "a/": false, "a b": false} {
		if ValidPath(p) != ok {
			t.Errorf("ValidPath(%q) = %v", p, !ok)
		}
	}
}

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
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Target{{"OFK0/guestbook", "main"}, {"OFK0/blog", "release"}}
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
		"typo":        "deploy:\n  - repo: a/b\n    brach: main\n",
		"no branch":   "deploy:\n  - repo: a/b\n",
		"bad repo":    "deploy:\n  - repo: https://github.com/a/b\n    branch: main\n",
		"duplicate":   "deploy:\n  - {repo: a/b, branch: main}\n  - {repo: A/B, branch: main}\n",
		"unknown key": "apps:\n  - repo: a/b\n",
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

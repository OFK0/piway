package webhook

import (
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func openStore(t *testing.T, content string) (*Store, string) {
	t.Helper()
	p := writeApps(t, content)
	s, err := OpenStore(p, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}

func TestStoreAddKeepsCommentsAndOtherKeys(t *testing.T) {
	s, p := openStore(t, "# my apps\ndeploy:\n  # the blog\n  - repo: OFK0/blog\n    branch: main\n")
	if err := s.Add(Target{Repo: "OFK0/guestbook", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	for _, want := range []string{"# my apps", "# the blog", "repo: OFK0/guestbook"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("file lost %q:\n%s", want, b)
		}
	}
	if got := s.Targets(); len(got) != 2 {
		t.Errorf("targets = %+v", got)
	}
	// What was written must load with the strict loader.
	if _, err := LoadTargets(p); err != nil {
		t.Errorf("written file does not load: %v", err)
	}
}

func TestStoreAddToMissingFile(t *testing.T) {
	p := t.TempDir() + "/sub/apps.yaml"
	s, err := OpenStore(p, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Targets(); len(got) != 0 {
		t.Fatalf("targets = %+v", got)
	}
	if err := s.Add(Target{Repo: "OFK0/blog", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if got := s.Targets(); len(got) != 1 || got[0].Repo != "OFK0/blog" {
		t.Errorf("targets = %+v", got)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestStoreAddRejects(t *testing.T) {
	s, _ := openStore(t, "deploy:\n  - repo: OFK0/blog\n    branch: main\n")
	if err := s.Add(Target{Repo: "ofk0/BLOG", Branch: "main"}); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate: %v, want ErrExists", err)
	}
	if err := s.Add(Target{Repo: "OFK0/blog", Branch: "dev"}); err != nil {
		t.Errorf("another branch: %v", err)
	}
	if err := s.Add(Target{Repo: "nope", Branch: "main"}); err == nil {
		t.Error("bad repo accepted")
	}
	if err := s.Add(Target{Repo: "a/b"}); err == nil {
		t.Error("empty branch accepted")
	}
	if err := s.Add(Target{Repo: "OFK0/blog", Branch: "beta", Auth: "token"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a different auth for another branch: %v, want ErrInvalid", err)
	}
	if err := s.Add(Target{Repo: "a/b", Branch: "main", Auth: "password"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown auth: %v, want ErrInvalid", err)
	}
}

func TestStoreAddWritesAuth(t *testing.T) {
	s, p := openStore(t, "deploy:\n  - repo: OFK0/blog\n    branch: main\n")
	if err := s.Add(Target{Repo: "OFK0/app", Branch: "main", Auth: "deploy-key"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "auth: deploy-key") || strings.Count(string(b), "auth:") != 1 {
		t.Errorf("file:\n%s", b)
	}
	if got := s.Targets(); len(got) != 2 || got[1].Auth != "deploy-key" {
		t.Errorf("targets = %+v", got)
	}
}

func TestStoreRemove(t *testing.T) {
	s, _ := openStore(t, "deploy:\n  - repo: OFK0/blog\n    branch: main\n  - repo: OFK0/blog\n    branch: dev\n  - repo: OFK0/guestbook\n    branch: main\n")
	got, err := s.Remove("ofk0/blog", "dev")
	if err != nil || len(got) != 1 || got[0].Branch != "dev" {
		t.Fatalf("remove one branch: %+v, %v", got, err)
	}
	got, err = s.Remove("OFK0/blog", "")
	if err != nil || len(got) != 1 {
		t.Fatalf("remove the rest: %+v, %v", got, err)
	}
	if _, err := s.Remove("OFK0/blog", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("again: %v, want ErrNotFound", err)
	}
	if left := s.Targets(); len(left) != 1 || left[0].Repo != "OFK0/guestbook" {
		t.Errorf("left = %+v", left)
	}
}

func TestStorePicksUpHandEdits(t *testing.T) {
	s, p := openStore(t, "deploy:\n  - repo: OFK0/blog\n    branch: main\n")
	edited := "deploy:\n  - repo: OFK0/blog\n    branch: main\n  - repo: OFK0/new\n    branch: main\n"
	if err := os.WriteFile(p, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(p, future, future)
	if got := s.Targets(); len(got) != 2 {
		t.Errorf("after hand edit: %+v", got)
	}

	// A broken edit keeps the last good list.
	if err := os.WriteFile(p, []byte("deploy:\n  - repo: nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	later := future.Add(time.Hour)
	_ = os.Chtimes(p, later, later)
	if got := s.Targets(); len(got) != 2 {
		t.Errorf("after broken edit: %+v, want the previous two", got)
	}
}

func TestOpenStoreRejectsBrokenFile(t *testing.T) {
	if _, err := OpenStore(writeApps(t, "deploy:\n  - repo: nope\n    branch: main\n"), slog.New(slog.DiscardHandler)); err == nil {
		t.Error("broken file opened")
	}
}

func TestStorePaths(t *testing.T) {
	s, p := openStore(t, "deploy: []\n")
	for _, path := range []string{"apps/web", "apps/api"} {
		if err := s.Add(Target{Repo: "o/mono", Branch: "main", Path: path}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Add(Target{Repo: "o/mono", Branch: "main", Path: "apps/web"}); !errors.Is(err, ErrExists) {
		t.Errorf("same path twice: %v", err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "path: apps/web") {
		t.Errorf("file:\n%s", b)
	}
	removed, err := s.Remove("o/mono", "main", "apps/web")
	if err != nil || len(removed) != 1 || len(s.Targets()) != 1 || s.Targets()[0].Path != "apps/api" {
		t.Errorf("remove one path: %v %v %+v", removed, err, s.Targets())
	}
	if removed, _ := s.Remove("o/mono", "main"); len(removed) != 1 {
		t.Errorf("remove the rest: %v", removed)
	}
}

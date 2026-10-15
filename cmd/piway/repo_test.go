package main

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ofk0/piway/internal/config"
	"github.com/ofk0/piway/internal/webhook"
)

func TestRepoCommands(t *testing.T) {
	file := filepath.Join(t.TempDir(), "apps.yaml")
	store, err := webhook.OpenStore(file, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{AppsFile: file}
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := repoCommand(store, cfg, args[0], args[1:], &out, &errb)
		return code, out.String(), errb.String()
	}

	if code, out, _ := run("list"); code != 0 || !strings.Contains(out, "No repositories") {
		t.Errorf("empty list: %d %q", code, out)
	}
	if code, out, errs := run("add", "OFK0/guestbook", "-branch", "dev"); code != 0 {
		t.Fatalf("add: %d %q %q", code, out, errs)
	}
	if code, out, _ := run("add", "OFK0/guestbook", "-branch", "dev"); code != 0 || !strings.Contains(out, "already listed") {
		t.Errorf("add twice: %d %q", code, out)
	}
	if code, out, _ := run("list"); code != 0 || !strings.Contains(out, "OFK0/guestbook") || !strings.Contains(out, "dev") {
		t.Errorf("list: %d %q", code, out)
	}
	if code, _, errs := run("add", "https://github.com/OFK0/x"); code != 2 {
		t.Errorf("url: %d %q, want usage error", code, errs)
	}
	if code, _, errs := run("remove", "OFK0/nope"); code != 1 || !strings.Contains(errs, "not listed") {
		t.Errorf("remove unknown: %d %q", code, errs)
	}
	if code, out, _ := run("remove", "OFK0/guestbook"); code != 0 || !strings.Contains(out, "Removed") {
		t.Errorf("remove: %d %q", code, out)
	}
}

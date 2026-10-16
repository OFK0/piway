package main

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ofk0/piway/internal/config"
	"github.com/ofk0/piway/internal/gitauth"
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
		r := repoCLI{store: store, access: gitauth.New(keysDir(cfg)), cfg: cfg, stdin: strings.NewReader(""), stdout: &out, stderr: &errb}
		code := r.command(args[0], args[1:])
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

func TestRepoPrivate(t *testing.T) {
	file := filepath.Join(t.TempDir(), "apps.yaml")
	store, err := webhook.OpenStore(file, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{AppsFile: file}
	access := gitauth.New(keysDir(cfg))
	run := func(stdin string, args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		r := repoCLI{store: store, access: access, cfg: cfg, stdin: strings.NewReader(stdin), stdout: &out, stderr: &errb}
		code := r.command(args[0], args[1:])
		return code, out.String(), errb.String()
	}

	code, out, errs := run("", "add", "OFK0/secret", "-private")
	if code != 0 || !strings.Contains(out, "ssh-ed25519 ") || !strings.Contains(out, "https://github.com/OFK0/secret/settings/keys/new") {
		t.Fatalf("add -private: %d %q %q", code, out, errs)
	}
	pub, _ := access.PublicKey("OFK0/secret")
	if code, out, _ := run("", "key", "OFK0/secret"); code != 0 || !strings.Contains(out, pub) {
		t.Errorf("key: %d %q", code, out)
	}
	if code, _, _ := run("", "add", "OFK0/secret", "-private", "-token"); code != 2 {
		t.Errorf("-private and -token: %d, want 2", code)
	}
	if code, out, _ := run("", "list"); code != 0 || !strings.Contains(out, "deploy-key") {
		t.Errorf("list: %q", out)
	}
	if code, _, errs := run("", "test", "OFK0/nope"); code != 1 || !strings.Contains(errs, "not listed") {
		t.Errorf("test unlisted: %d %q", code, errs)
	}

	if code, out, _ := run("", "remove", "OFK0/secret"); code != 0 || !strings.Contains(out, "Deleted its deploy key") {
		t.Errorf("remove: %d %q", code, out)
	}
	if access.HasKey("OFK0/secret") {
		t.Error("the deploy key outlived its repository")
	}

	if code, out, _ := run("", "add", "OFK0/other", "-token"); code != 0 || !strings.Contains(out, "piway repo token set") {
		t.Errorf("add -token without a token: %d %q", code, out)
	}
	if code, _, errs := run("github_pat_example\n", "token", "set"); code != 0 || !access.HasToken() {
		t.Errorf("token set: %d %q", code, errs)
	}
	if code, _, _ := run("", "token", "remove"); code != 0 || access.HasToken() {
		t.Errorf("token remove: %d", code)
	}
	if code, _, _ := run("", "token", "set"); code != 1 {
		t.Errorf("empty token: %d, want 1", code)
	}
}

func TestRepoPath(t *testing.T) {
	file := filepath.Join(t.TempDir(), "apps.yaml")
	store, err := webhook.OpenStore(file, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{AppsFile: file}
	run := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		r := repoCLI{store: store, access: gitauth.New(keysDir(cfg)), cfg: cfg, stdin: strings.NewReader(""), stdout: &out, stderr: &errb}
		code := r.command(args[0], args[1:])
		return code, out.String() + errb.String()
	}
	if code, out := run("add", "OFK0/piway", "-path", "examples/loadlab/"); code != 0 || !strings.Contains(out, "OFK0/piway@main:examples/loadlab") {
		t.Fatalf("add: %d %q", code, out)
	}
	run("add", "OFK0/piway", "-path", "examples/chat")
	if code, out := run("list"); code != 0 || !strings.Contains(out, "examples/chat") || !strings.Contains(out, "PATH") {
		t.Errorf("list: %q", out)
	}
	if code, out := run("remove", "OFK0/piway", "-path", "examples/chat"); code != 0 || strings.Contains(out, "loadlab") {
		t.Errorf("remove one path: %d %q", code, out)
	}
	if len(store.Targets()) != 1 {
		t.Errorf("targets %+v", store.Targets())
	}
}

func TestRepoAddShowsPublicAddress(t *testing.T) {
	cfg := config.Config{AppsFile: filepath.Join(t.TempDir(), "apps.yaml"), WebhookSecret: strings.Repeat("s", 20)}
	for name, tc := range map[string]struct {
		url  string
		want string
		not  string
	}{
		"found":   {"https://pi.example.ts.net:10000/api/v1/webhooks/github", "  https://pi.example.ts.net:10000/api/v1/webhooks/github", "<your piway address>"},
		"unknown": {"", "knows no public", "<your piway address>"},
	} {
		store, err := webhook.OpenStore(cfg.AppsFile+name, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}
		var out, errb bytes.Buffer
		r := repoCLI{store: store, access: gitauth.New(keysDir(cfg)), cfg: cfg, stdin: strings.NewReader(""), stdout: &out, stderr: &errb,
			webhookURL: func() (string, string) { return tc.url, "tailscale funnel" }}
		if code := r.command("add", []string{"OFK0/app"}); code != 0 {
			t.Fatalf("%s: %d %s", name, code, errb.String())
		}
		if !strings.Contains(out.String(), tc.want) || strings.Contains(out.String(), tc.not) {
			t.Errorf("%s: output\n%s", name, out.String())
		}
	}
}

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ofk0/piway/internal/config"
	"github.com/ofk0/piway/internal/gitauth"
	"github.com/ofk0/piway/internal/publicurl"
	"github.com/ofk0/piway/internal/webhook"
	"golang.org/x/term"
)

const repoUsage = `usage: piway repo <command>

  add owner/name [-branch main] [-path dir] [-private | -token]
                                     deploy pushes to this repository; -path for
                                     an app in a directory of it
  list                               show the repositories that deploy
  remove owner/name [-branch main] [-path dir]
                                     stop deploying it (all branches and paths
                                     without -branch and -path)
  test owner/name                    check that piway can read the repository
  key owner/name                     show a private repository's deploy key again
  token set | remove                 store or delete the GitHub token for -token

A private repository needs -private (piway makes a deploy key for it, which
you add to the repository on GitHub) or -token (one fine-grained token with
Contents: read). Deploy keys are recommended; docs/DEPLOY.md explains both.

Edits apps_file; a running piway picks the change up within a moment, with
no restart. Apps that are already running, and their data, are not touched.`

// keysDir holds deploy keys and the token: keys/ next to apps.yaml.
func keysDir(cfg config.Config) string {
	return filepath.Join(filepath.Dir(cfg.AppsFile), "keys")
}

// runRepo implements `piway repo`: the repositories in apps.yaml, without
// editing it by hand.
func runRepo(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, repoUsage)
		return 2
	}
	if args[0] == "-h" || args[0] == "-help" || args[0] == "--help" {
		fmt.Fprintln(os.Stderr, repoUsage)
		return 0
	}
	loaded, err := config.Load(nil, os.LookupEnv, config.DefaultPath(), io.Discard)
	if err != nil {
		fmt.Fprintln(os.Stderr, "piway:", err)
		return 2
	}
	store, err := webhook.OpenStore(loaded.Config.AppsFile, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		fmt.Fprintln(os.Stderr, "piway:", err)
		return 1
	}
	r := repoCLI{store: store, access: gitauth.New(keysDir(loaded.Config)), cfg: loaded.Config,
		stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}
	return r.command(args[0], args[1:])
}

// publicURL is the address GitHub reaches the webhook at, and where it
// was found, or "" when piway knows none.
func (r repoCLI) publicURL() (string, string) {
	if r.webhookURL != nil {
		return r.webhookURL()
	}
	f := &publicurl.Finder{Configured: r.cfg.WebhookURL, Addr: r.cfg.Addr, Path: "/api/v1/webhooks/github"}
	return f.URL(context.Background())
}

type repoCLI struct {
	store          *webhook.Store
	access         *gitauth.Access
	cfg            config.Config
	stdin          io.Reader
	stdout, stderr io.Writer
	webhookURL     func() (string, string) // tests; nil asks publicurl
}

func (r repoCLI) command(cmd string, args []string) int {
	stdout, stderr := r.stdout, r.stderr
	fs := flag.NewFlagSet("piway repo "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	branch := fs.String("branch", "", "`branch` to deploy (add: default main; remove: default every branch)")
	dir := fs.String("path", "", "the app's `directory` in the repository (default its root)")
	pathSet := false
	private := fs.Bool("private", false, "add: a private repository, read with a deploy key piway makes")
	token := fs.Bool("token", false, "add: a private repository, read with the stored GitHub token")
	// Flags may follow the repository: piway repo add owner/name -branch dev.
	var rest []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		rest = append(rest, fs.Arg(0))
		args = fs.Args()[1:]
	}
	fs.Visit(func(f *flag.Flag) { pathSet = pathSet || f.Name == "path" })
	*dir = strings.Trim(*dir, "/")

	need := func() (string, bool) {
		if len(rest) != 1 || strings.Contains(rest[0], "://") {
			fmt.Fprintf(stderr, "usage: piway repo %s owner/name\n", cmd)
			return "", false
		}
		return rest[0], true
	}

	switch cmd {
	case "list":
		if len(rest) != 0 {
			fmt.Fprintln(stderr, "usage: piway repo list")
			return 2
		}
		targets := r.store.Targets()
		if len(targets) == 0 {
			fmt.Fprintln(stdout, "No repositories deploy yet. Add one: piway repo add owner/name")
			return 0
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "REPOSITORY\tBRANCH\tPATH\tAUTH")
		for _, t := range targets {
			auth := t.Auth
			if auth == "" {
				auth = "public"
			}
			path := t.Path
			if path == "" {
				path = "/"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.Repo, t.Branch, path, auth)
		}
		_ = tw.Flush()
		return 0

	case "add":
		repo, ok := need()
		if !ok {
			return 2
		}
		if *private && *token {
			fmt.Fprintln(stderr, "piway: choose -private (deploy key) or -token, not both")
			return 2
		}
		auth := gitauth.Public
		switch {
		case *private:
			auth = gitauth.DeployKey
		case *token:
			auth = gitauth.Token
		}
		b := *branch
		if b == "" {
			b = "main"
		}
		target := webhook.Target{Repo: repo, Branch: b, Path: *dir, Auth: auth}
		err := r.store.Add(target)
		exists := errors.Is(err, webhook.ErrExists)
		if err != nil && !exists {
			fmt.Fprintln(stderr, "piway:", err)
			return 1
		}
		if auth == gitauth.DeployKey {
			if _, err := r.access.GenerateKey(repo); err != nil {
				fmt.Fprintln(stderr, "piway: make the deploy key:", err)
				return 1
			}
		}
		if exists {
			fmt.Fprintf(stdout, "%s is already listed.\n", target.Name())
			if auth == gitauth.DeployKey {
				return r.showKey(repo)
			}
			return 0
		}
		fmt.Fprintf(stdout, "Added %s to %s.\n\n", target.Name(), r.cfg.AppsFile)
		switch auth {
		case gitauth.DeployKey:
			if code := r.showKey(repo); code != 0 {
				return code
			}
			fmt.Fprintln(stdout)
		case gitauth.Token:
			if !r.access.HasToken() {
				fmt.Fprintln(stdout, "No GitHub token is stored yet: piway repo token set")
			}
			fmt.Fprintf(stdout, "The token needs Contents: read on %s. Then check: piway repo test %s\n\n", repo, repo)
		}
		if url, _ := r.publicURL(); url != "" {
			fmt.Fprintln(stdout, "Push events deploy it once GitHub reaches piway. On the repository's")
			fmt.Fprintln(stdout, "Settings > Webhooks, point a webhook at")
			fmt.Fprintln(stdout, "  "+url)
			fmt.Fprintln(stdout, "with content type application/json and the same secret as webhook_secret.")
		} else {
			fmt.Fprintln(stdout, "Push events deploy it once GitHub can reach piway, and piway knows no public")
			fmt.Fprintln(stdout, "address: the one you open the dashboard at may be tailnet-only. Publish the")
			fmt.Fprintln(stdout, "webhook (Tailscale Funnel, docs/REMOTE.md) so piway finds it, or set")
			fmt.Fprintln(stdout, "webhook_url in the config. Then, on the repository's Settings > Webhooks, point")
			fmt.Fprintln(stdout, "a webhook at that address + /api/v1/webhooks/github, with content type")
			fmt.Fprintln(stdout, "application/json and the same secret as webhook_secret.")
		}
		if r.cfg.WebhookSecret == "" {
			fmt.Fprintln(stdout, "\nwebhook_secret is not set, so piway will refuse every delivery (docs/DEPLOY.md).")
		}
		return 0

	case "remove", "rm":
		repo, ok := need()
		if !ok {
			return 2
		}
		var removed []webhook.Target
		var err error
		if pathSet {
			removed, err = r.store.Remove(repo, *branch, *dir)
		} else {
			removed, err = r.store.Remove(repo, *branch)
		}
		if errors.Is(err, webhook.ErrNotFound) {
			fmt.Fprintf(stderr, "piway: %s is not listed (piway repo list)\n", repo)
			return 1
		}
		if err != nil {
			fmt.Fprintln(stderr, "piway:", err)
			return 1
		}
		for _, t := range removed {
			fmt.Fprintf(stdout, "Removed %s. Its running app keeps running.\n", t.Name())
		}
		if removed[0].Auth == gitauth.DeployKey && !webhook.Listed(r.store.Targets(), repo) && r.access.HasKey(repo) {
			if err := r.access.RemoveKey(repo); err != nil {
				fmt.Fprintln(stderr, "piway: delete the deploy key:", err)
				return 1
			}
			fmt.Fprintf(stdout, "Deleted its deploy key. Remove it on GitHub too: https://github.com/%s/settings/keys\n", removed[0].Repo)
		}
		return 0

	case "test":
		repo, ok := need()
		if !ok {
			return 2
		}
		targets := r.store.Targets()
		if !webhook.Listed(targets, repo) {
			fmt.Fprintf(stderr, "piway: %s is not listed (piway repo list)\n", repo)
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := r.access.Test(ctx, gitauth.Source{Repo: repo, Auth: webhook.AuthOf(targets, repo)}); err != nil {
			fmt.Fprintln(stderr, "piway:", err)
			return 1
		}
		fmt.Fprintf(stdout, "piway can read %s.\n", repo)
		return 0

	case "key":
		repo, ok := need()
		if !ok {
			return 2
		}
		return r.showKey(repo)

	case "token":
		if len(rest) != 1 {
			fmt.Fprintln(stderr, "usage: piway repo token set | remove")
			return 2
		}
		switch rest[0] {
		case "set":
			tok, err := r.readToken()
			if err == nil {
				err = r.access.SetToken(tok)
			}
			if err != nil {
				fmt.Fprintln(stderr, "piway:", err)
				return 1
			}
			fmt.Fprintln(stdout, "Stored the token. Check a repository with: piway repo test owner/name")
			return 0
		case "remove", "rm":
			if err := r.access.RemoveToken(); err != nil {
				fmt.Fprintln(stderr, "piway:", err)
				return 1
			}
			fmt.Fprintln(stdout, "Deleted the token. Revoke it on GitHub too: https://github.com/settings/personal-access-tokens")
			return 0
		}
		fmt.Fprintln(stderr, "usage: piway repo token set | remove")
		return 2
	}
	fmt.Fprintln(stderr, repoUsage)
	return 2
}

// showKey prints repo's public deploy key and where it goes.
func (r repoCLI) showKey(repo string) int {
	pub, err := r.access.PublicKey(repo)
	if err != nil {
		fmt.Fprintln(r.stderr, "piway:", err)
		return 1
	}
	fmt.Fprintf(r.stdout, "Add this deploy key to %s (read access only, leave \"Allow write access\" off):\n\n", repo)
	fmt.Fprintf(r.stdout, "  %s\n\n", pub)
	fmt.Fprintf(r.stdout, "  %s\n\n", gitauth.DeployKeyURL(repo))
	fmt.Fprintf(r.stdout, "Then check: piway repo test %s\n", repo)
	return 0
}

// readToken asks for the token on the terminal without echoing it, or
// reads one line from standard input when it is not a terminal.
func (r repoCLI) readToken() (string, error) {
	if f, ok := r.stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(r.stderr, "GitHub token (Contents: read): ")
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(r.stderr)
		return string(b), err
	}
	line, err := bufio.NewReader(r.stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return line, nil
}

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/ofk0/piway/internal/config"
	"github.com/ofk0/piway/internal/webhook"
)

const repoUsage = `usage: piway repo <command>

  add owner/name [-branch main]      deploy pushes to this repository
  list                               show the repositories that deploy
  remove owner/name [-branch main]   stop deploying it (all branches without -branch)

Edits apps_file; a running piway picks the change up within a moment, with
no restart. Apps that are already running, and their data, are not touched.`

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
	return repoCommand(store, loaded.Config, args[0], args[1:], os.Stdout, os.Stderr)
}

func repoCommand(store *webhook.Store, cfg config.Config, cmd string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("piway repo "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	branch := fs.String("branch", "", "`branch` to deploy (add: default main; remove: default every branch)")
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
		targets := store.Targets()
		if len(targets) == 0 {
			fmt.Fprintln(stdout, "No repositories deploy yet. Add one: piway repo add owner/name")
			return 0
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "REPOSITORY\tBRANCH")
		for _, t := range targets {
			fmt.Fprintf(tw, "%s\t%s\n", t.Repo, t.Branch)
		}
		_ = tw.Flush()
		return 0

	case "add":
		repo, ok := need()
		if !ok {
			return 2
		}
		b := *branch
		if b == "" {
			b = "main"
		}
		err := store.Add(webhook.Target{Repo: repo, Branch: b})
		if errors.Is(err, webhook.ErrExists) {
			fmt.Fprintf(stdout, "%s@%s is already listed.\n", repo, b)
			return 0
		}
		if err != nil {
			fmt.Fprintln(stderr, "piway:", err)
			return 1
		}
		fmt.Fprintf(stdout, "Added %s@%s to %s.\n\n", repo, b, cfg.AppsFile)
		fmt.Fprintln(stdout, "Push events deploy it once GitHub reaches piway. On the repository's")
		fmt.Fprintln(stdout, "Settings > Webhooks, point a webhook at")
		fmt.Fprintln(stdout, "  https://<your piway address>"+"/api/v1/webhooks/github")
		fmt.Fprintln(stdout, "with content type application/json and the same secret as webhook_secret.")
		if cfg.WebhookSecret == "" {
			fmt.Fprintln(stdout, "\nwebhook_secret is not set, so piway will refuse every delivery (docs/DEPLOY.md).")
		}
		return 0

	case "remove", "rm":
		repo, ok := need()
		if !ok {
			return 2
		}
		removed, err := store.Remove(repo, *branch)
		if errors.Is(err, webhook.ErrNotFound) {
			fmt.Fprintf(stderr, "piway: %s is not listed (piway repo list)\n", repo)
			return 1
		}
		if err != nil {
			fmt.Fprintln(stderr, "piway:", err)
			return 1
		}
		for _, t := range removed {
			fmt.Fprintf(stdout, "Removed %s@%s. Its running app keeps running.\n", t.Repo, t.Branch)
		}
		return 0
	}
	fmt.Fprintln(stderr, repoUsage)
	return 2
}

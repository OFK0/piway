package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"time"

	"github.com/ofk0/piway/internal/config"
	"github.com/ofk0/piway/internal/gitauth"
	"github.com/ofk0/piway/internal/publicurl"
	"github.com/ofk0/piway/internal/setup"
	"github.com/ofk0/piway/internal/webhook"
)

// runDoctor implements `piway doctor`: it checks the installation and says
// how to fix each problem. Exit status 1 means a check failed.
func runDoctor(args []string) int {
	fs := flag.NewFlagSet("piway doctor", flag.ContinueOnError)
	security := fs.Bool("security", false, "check the hardening steps in docs/HARDENING.md instead (run with sudo)")
	asJSON := fs.Bool("json", false, "print the results as JSON")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: piway doctor [-security] [-json]\n\nChecks this piway installation and prints how to fix each problem.\nExits 1 if a check fails.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var report *setup.Report
	footer := "piway setup fixes most of these; docs/ has the steps for each."
	if *security {
		report = setup.Security(ctx, setup.System{}, os.Getenv("SUDO_USER"))
		footer = "docs/HARDENING.md explains each step."
	} else {
		report = setup.Doctor(ctx, setup.System{}, doctorEnv())
	}

	var err error
	if *asJSON {
		err = report.WriteJSON(os.Stdout)
	} else {
		err = report.WriteText(os.Stdout, footer)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "piway:", err)
		return 1
	}
	if report.Failed() {
		return 1
	}
	return 0
}

// doctorEnv reads the settings the service would use. A broken config file
// is reported as a failed check, and the other checks use the defaults.
func doctorEnv() setup.Env {
	env := setup.Env{User: os.Getenv("USER")}
	if u, err := user.Current(); err == nil {
		env.User, env.Home = u.Username, u.HomeDir
	}
	if env.Home == "" {
		env.Home, _ = os.UserHomeDir()
	}
	loaded, err := config.Load(nil, os.LookupEnv, config.DefaultPath(), io.Discard)
	if err != nil {
		env.Config, env.ConfigErr = config.Default(), err
		return env
	}
	env.Config, env.ConfigFile = loaded.Config, loaded.File
	env.Repos, env.ReposErr = webhook.LoadTargets(loaded.Config.AppsFile)
	env.TestRepo = gitauth.New(keysDir(loaded.Config)).Test
	finder := &publicurl.Finder{Configured: loaded.Config.WebhookURL, Addr: loaded.Config.Addr, Path: "/api/v1/webhooks/github"}
	env.WebhookURL = func(ctx context.Context) string { u, _ := finder.URL(ctx); return u }
	if _, err := exec.LookPath("gh"); err == nil {
		env.Hooks = githubHooks
	}
	return env
}

// githubHooks lists the payload URLs of repo's webhooks with the gh CLI.
func githubHooks(ctx context.Context, repo string) ([]string, error) {
	out, err := exec.CommandContext(ctx, "gh", "api", "repos/"+repo+"/hooks", "--jq", ".[].config.url").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, errors.New(strings.TrimSpace(strings.SplitN(string(ee.Stderr), "\n", 2)[0]))
		}
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/ofk0/piway/internal/setup"
)

// runSetup implements `piway setup`: it finds what is missing with the same
// checks as `piway doctor`, shows the plan and installs it. Running it again
// only does what is still missing.
func runSetup(args []string) int {
	fs := flag.NewFlagSet("piway setup", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "print the plan and the commands, change nothing")
	yes := fs.Bool("yes", false, "answer every question with its default and run without asking")
	proxy := fs.String("proxy-domain", "local", "publish apps at <name>.`DOMAIN` through Caddy; \"\" leaves the proxy out")
	noDocker := fs.Bool("no-docker", false, "skip Docker (apps cannot be deployed without it)")
	tailscale := fs.Bool("tailscale", false, "publish the dashboard to your devices with Tailscale (docs/REMOTE.md)")
	passwordStdin := fs.Bool("password-stdin", false, "with -yes: read the dashboard password from stdin")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `usage: piway setup [flags]

Installs what piway needs on this Raspberry Pi: the piway service, rootless
Docker, Caddy and mDNS for apps, and optionally Tailscale. Every command that
runs with sudo is shown first. Run it again at any time: it only does what is
missing.`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	in := bufio.NewReader(os.Stdin)
	ask := func(q string, def bool) bool {
		if *yes || *dryRun {
			return def
		}
		hint := "[Y/n]"
		if !def {
			hint = "[y/N]"
		}
		fmt.Printf("%s %s ", q, hint)
		line, _ := in.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "":
			return def
		case "y", "yes":
			return true
		}
		return false
	}

	env := doctorEnv()
	opt := setup.Options{ProxyDomain: *proxy, Docker: !*noDocker, Tailscale: *tailscale}
	fmt.Println("piway setup: checking what is already in place…")
	if env.ConfigFile == "" && !set["proxy-domain"] && !ask("Publish apps at <name>.local on your network (Caddy and mDNS)?", true) {
		opt.ProxyDomain = ""
	}
	if env.ConfigFile != "" && !set["proxy-domain"] {
		opt.ProxyDomain = env.Config.ProxyDomain // keep what the file says
	}
	if !set["no-docker"] && !ask("Install Docker, to build and run apps?", true) {
		opt.Docker = false
	}
	if !set["tailscale"] && !*yes && !*dryRun && ask("Reach the dashboard from your other devices with Tailscale?", false) {
		opt.Tailscale = true
	}
	switch {
	case !*yes || *passwordStdin:
		opt.SetPassword = func(context.Context) error { return setPassword(env.Config.PasswordFile) }
	default:
		// -yes without a password: leave it for later rather than guess.
	}

	steps, err := setup.Plan(ctx, setup.System{}, env, opt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "piway setup:", err)
		return 1
	}
	r := &setup.Runner{Host: setup.System{}, Out: os.Stdout, In: in, Yes: *yes, DryRun: *dryRun}
	reboot, err := r.Run(ctx, steps)
	if err != nil {
		fmt.Fprintln(os.Stderr, "\npiway setup:", err)
		return 1
	}
	if *dryRun {
		return 0
	}

	fmt.Println("\nChecking the result:")
	report := setup.Doctor(ctx, setup.System{}, doctorEnv())
	_ = report.WriteText(os.Stdout, "Run `piway doctor` after fixing these; `piway setup` again does what is still missing.")
	if reboot {
		fmt.Println("\nReboot to finish: memory limits for containers take effect after `sudo reboot`.")
	}
	if url := dashboardURL(env); url != "" && !report.Failed() {
		fmt.Println("\nThe dashboard is at " + url)
	}
	if report.Failed() {
		return 1
	}
	return 0
}

// runUninstall implements `piway uninstall`.
func runUninstall(args []string) int {
	fs := flag.NewFlagSet("piway uninstall", flag.ContinueOnError)
	purge := fs.Bool("purge", false, "also delete the configuration, password, tokens and metrics history")
	dryRun := fs.Bool("dry-run", false, "print what would be removed, change nothing")
	yes := fs.Bool("yes", false, "remove without asking")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	steps := setup.UninstallPlan(ctx, setup.System{}, doctorEnv(), *purge)
	r := &setup.Runner{Host: setup.System{}, Out: os.Stdout, In: bufio.NewReader(os.Stdin), Yes: *yes, DryRun: *dryRun}
	if _, err := r.Run(ctx, steps); err != nil {
		fmt.Fprintln(os.Stderr, "\npiway uninstall:", err)
		return 1
	}
	return 0
}

// dashboardURL is where to open the dashboard from another device on the LAN.
func dashboardURL(env setup.Env) string {
	host, _ := os.Hostname()
	if host == "" {
		return ""
	}
	addr := env.Config.Addr
	if strings.HasPrefix(addr, "127.0.0.1:") || strings.HasPrefix(addr, "localhost:") {
		return "" // loopback only: reached through Tailscale, not the LAN
	}
	_, port, _ := strings.Cut(addr, ":")
	return fmt.Sprintf("http://%s.local:%s/", host, port)
}

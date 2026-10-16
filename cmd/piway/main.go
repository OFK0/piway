// Command piway is a self-hosted metrics agent and deploy platform for Raspberry Pi.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/ofk0/piway/internal/agent"
	"github.com/ofk0/piway/internal/alert"
	"github.com/ofk0/piway/internal/auth"
	"github.com/ofk0/piway/internal/caddy"
	"github.com/ofk0/piway/internal/config"
	"github.com/ofk0/piway/internal/databases"
	"github.com/ofk0/piway/internal/deploy"
	"github.com/ofk0/piway/internal/docker"
	"github.com/ofk0/piway/internal/gitauth"
	"github.com/ofk0/piway/internal/history"
	"github.com/ofk0/piway/internal/mcp"
	"github.com/ofk0/piway/internal/mdns"
	"github.com/ofk0/piway/internal/notify"
	"github.com/ofk0/piway/internal/push"
	"github.com/ofk0/piway/internal/recorder"
	"github.com/ofk0/piway/internal/router"
	"github.com/ofk0/piway/internal/secrets"
	"github.com/ofk0/piway/internal/server"
	"github.com/ofk0/piway/internal/stream"
	"github.com/ofk0/piway/internal/version"
	"github.com/ofk0/piway/internal/webhook"
	"github.com/ofk0/piway/internal/webui"
	"golang.org/x/term"
)

func main() {
	// git runs piway as GIT_ASKPASS to read a repository token.
	if file := os.Getenv(gitauth.AskpassEnv); file != "" && len(os.Args) == 2 {
		os.Exit(gitauth.Askpass(os.Args[1], file, os.Stdout))
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "mcp":
			os.Exit(runMCP(os.Args[2:]))
		case "backup":
			os.Exit(runBackup(os.Args[2:]))
		case "restore":
			os.Exit(runRestore(os.Args[2:]))
		case "doctor":
			os.Exit(runDoctor(os.Args[2:]))
		case "repo":
			os.Exit(runRepo(os.Args[2:]))
		case "setup":
			os.Exit(runSetup(os.Args[2:]))
		case "uninstall":
			os.Exit(runUninstall(os.Args[2:]))
		}
	}
	cfg, err := config.Load(os.Args[1:], os.LookupEnv, config.DefaultPath(), os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "piway:", err)
		os.Exit(2)
	}

	switch {
	case cfg.ShowVersion:
		fmt.Printf("piway %s (%s)\n", version.Version, version.Commit)
		return
	case cfg.PrintConfig:
		cfg.Print(os.Stdout)
		return
	case cfg.SetPassword:
		if err := setPassword(cfg.PasswordFile); err != nil {
			fmt.Fprintln(os.Stderr, "piway:", err)
			os.Exit(1)
		}
		return
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel})))
	if cfg.File != "" {
		slog.Info("loaded config", "file", cfg.File)
	}

	if err := run(cfg.Config); err != nil {
		slog.Error("piway exited", "err", err)
		os.Exit(1)
	}
}

func run(cfg config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log := slog.Default()
	ag, err := agent.New(cfg.Interval, log, agent.OpenDefault(log)...)
	if err != nil {
		return err
	}
	defer ag.Close()

	hub := stream.NewHub(cfg.StreamQueue)
	live := stream.NewServer(hub, log)
	ag.OnPublish(func(s *agent.Snapshot) {
		// Encode once per round; every client is sent the same bytes.
		b, err := json.Marshal(s)
		if err != nil {
			log.Error("encode snapshot", "err", err)
			return
		}
		hub.Broadcast(b)
	})

	alerts, err := openAlerts(cfg, log)
	if err != nil {
		return err
	}
	ag.OnPublish(func(s *agent.Snapshot) { alerts.Observe(recorder.Points(nil, s), s.Time) })
	pusher, err := openPush(cfg, log)
	if err != nil {
		return err
	}
	go pusher.Run(ctx)
	// Alerts and finished builds become notifications (docs/ALERTS.md).
	notifier := &notify.Notifier{Mode: notify.Mode(cfg.NotifyDeploys), Send: pusher}
	alerts.OnEvent(notifier.Alert)

	hist, err := openHistory(cfg, log)
	if err != nil {
		return err
	}
	compactDone := make(chan struct{})
	if hist != nil {
		ag.OnPublish(recorder.New(hist, log).Record)
		go func() {
			hist.Run(ctx, 10*time.Second)
			close(compactDone)
		}()
	} else {
		close(compactDone)
	}

	agentDone := make(chan struct{})
	go func() {
		ag.Run(ctx)
		close(agentDone)
	}()
	go hub.Run(ctx)
	// Runs after the HTTP server has shut down: cancelling ctx stops the
	// agent (before the deferred Close releases the collectors' files) and
	// the Hub, which tells every WebSocket to close. Shutdown does not track
	// hijacked connections, so wait for their close handshakes here.
	// The history closes last: Close writes pending samples, and neither
	// the agent nor compaction may use a closed store.
	defer func() {
		stop()
		<-agentDone
		<-compactDone
		live.Wait()
		if hist != nil {
			if err := hist.Close(); err != nil {
				log.Error("close metrics history", "err", err)
			}
		}
	}()

	ui := webui.FS()
	// A nil *history.History inside the interface would not compare equal
	// to nil, so pass a true nil when recording is off.
	var queries server.SeriesQuerier
	if hist != nil {
		queries = hist
	}
	routes := router.New(log)
	defer routes.Close() // last: apps stay reachable until the end
	proxy, err := openProxy(ctx, cfg, routes, log)
	if err != nil {
		return err
	}
	repos, err := webhook.OpenStore(cfg.AppsFile, log)
	if err != nil {
		return err
	}
	access := gitauth.New(keysDir(cfg))
	pipeline, err := openPipeline(ctx, cfg, repos, access, routes, proxy, alerts, notifier, log)
	if err != nil {
		return err
	}
	var builds server.Builds // a true nil when deploys are off (see queries)
	var deployer webhook.Deployer = logDeployer{log}
	pipelineDone := make(chan struct{})
	if pipeline != nil {
		builds, deployer = pipeline, pipeline
		go func() {
			pipeline.Run(ctx)
			close(pipelineDone)
		}()
	} else {
		close(pipelineDone)
	}
	// Registered after the shutdown defer above, so it runs before it:
	// cancel ctx here too, or Run would never return. A running build is
	// cancelled and recorded.
	defer func() {
		stop()
		<-pipelineDone
	}()

	hooks := openWebhooks(cfg, repos, deployer, log)
	guard, err := openAuth(cfg, log)
	if err != nil {
		return err
	}
	defer func() {
		if err := guard.Close(); err != nil {
			log.Error("save API tokens", "err", err)
		}
	}()
	handler, err := server.New(server.Deps{
		Snapshots: ag, Live: live, History: queries, Webhooks: hooks, Builds: builds, Repos: repos, RepoAccess: access, WebhookSecretSet: cfg.WebhookSecret != "", UI: ui, Auth: guard, Alerts: alerts, Push: pusher,
	})
	if err != nil {
		return fmt.Errorf("dashboard: %w", err)
	}
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("piway listening", "addr", cfg.Addr, "version", version.Version, "dashboard", ui != nil)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// runMCP is "piway mcp": an MCP server on stdin and stdout for a client
// that starts it as a subprocess, such as Claude Code. It reaches a
// running piway over its HTTP API (docs/MCP.md).
func runMCP(args []string) int {
	fs := flag.NewFlagSet("piway mcp", flag.ContinueOnError)
	def := os.Getenv("PIWAY_URL")
	if def == "" {
		def = "http://127.0.0.1:8080"
	}
	url := fs.String("url", def, "piway's `URL` (env PIWAY_URL)")
	tokenFile := fs.String("token-file", "", "read the API token from `file` instead of $PIWAY_TOKEN")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: piway mcp [-url URL] [-token-file FILE]\n\nServes MCP on stdin/stdout. The API token comes from $PIWAY_TOKEN or -token-file.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	// stdout carries the protocol; everything else goes to stderr.
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	token := os.Getenv("PIWAY_TOKEN")
	if *tokenFile != "" {
		b, err := os.ReadFile(*tokenFile)
		if err != nil {
			log.Error("read token", "err", err)
			return 1
		}
		token = strings.TrimSpace(string(b))
	}
	c := &mcp.Client{Base: *url}
	if token != "" {
		c.Authorization = "Bearer " + token
	} else {
		log.Warn("no API token ($PIWAY_TOKEN or -token-file): only works while piway has no password and runs on this machine")
	}
	s := &mcp.Server{Name: "piway", Version: version.Version, Tools: mcp.Tools(), Log: log}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := s.ServeStdio(ctx, c, os.Stdin, os.Stdout); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("mcp", "err", err)
		return 1
	}
	return 0
}

// openAlerts loads the alert rules and logs every alert that fires or
// resolves.
func openAlerts(cfg config.Config, log *slog.Logger) (*alert.Engine, error) {
	rules, err := alert.Load(cfg.AlertsFile)
	if err != nil {
		return nil, fmt.Errorf("alerts: %w", err)
	}
	e := alert.New(rules)
	e.OnEvent(func(ev alert.Event) {
		if ev.State == alert.Resolved {
			log.Info("alert resolved", "rule", ev.Rule, "series", ev.Series, "summary", ev.Summary)
		} else {
			log.Warn("alert firing", "rule", ev.Rule, "series", ev.Series, "summary", ev.Summary)
		}
	})
	log.Info("alerts on", "rules", len(rules.Rules), "deploy_failures", rules.DeployFailures, "file", cfg.AlertsFile)
	return e, nil
}

// openPush sets up Web Push to the subscribed devices.
func openPush(cfg config.Config, log *slog.Logger) (*push.Service, error) {
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = config.DefaultDataDir()
	}
	p, err := push.New(push.Options{Dir: filepath.Join(dataDir, "push"), Subject: cfg.PushContact, Logger: log})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// openAuth sets up sign-in. Sessions are kept next to the metrics history.
func openAuth(cfg config.Config, log *slog.Logger) (*auth.Auth, error) {
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = config.DefaultDataDir()
	}
	a, err := auth.New(auth.Options{
		PasswordFile: cfg.PasswordFile,
		SessionsFile: filepath.Join(dataDir, "sessions.json"),
		TokensFile:   filepath.Join(dataDir, "tokens.json"),
		Logger:       log,
	})
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	if a.Mode() == "local" {
		log.Warn("no dashboard password: the API answers only this machine (set one with piway -set-password)", "password_file", cfg.PasswordFile)
	}
	return a, nil
}

// setPassword asks for a new password twice on the terminal, or reads one
// line from standard input when it is not a terminal (for scripts).
func setPassword(file string) error {
	var pw string
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		read := func(prompt string) (string, error) {
			fmt.Fprint(os.Stderr, prompt)
			b, err := term.ReadPassword(fd)
			fmt.Fprintln(os.Stderr)
			return string(b), err
		}
		first, err := read("New dashboard password: ")
		if err != nil {
			return err
		}
		second, err := read("Again: ")
		if err != nil {
			return err
		}
		if first != second {
			return errors.New("the passwords differ; nothing changed")
		}
		pw = first
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		pw = strings.TrimRight(line, "\r\n")
	}
	if err := auth.SetPassword(file, pw); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Password saved to %s. Signed-in browsers must sign in again.\n", file)
	return nil
}

// openHistory opens the metrics history in cfg.DataDir, or returns nil when
// recording is disabled (data_dir set to "").
func openHistory(cfg config.Config, log *slog.Logger) (*history.History, error) {
	if cfg.DataDir == "" {
		log.Info("metrics history disabled (data_dir is empty)")
		return nil, nil
	}
	h, err := history.Open(history.Options{
		Dir:           cfg.DataDir,
		FlushInterval: cfg.FlushInterval,
		Retention:     history.Retention{Raw: cfg.RetentionRaw, Minute: cfg.Retention1m, Hour: cfg.Retention1h},
		Logger:        log,
	})
	if err != nil {
		return nil, fmt.Errorf("metrics history: %w", err)
	}
	log.Info("recording metrics", "dir", cfg.DataDir, "flush_interval", cfg.FlushInterval)
	return h, nil
}

// openWebhooks builds the GitHub webhook handler from webhook_secret and
// the deploy targets in repos.
func openWebhooks(cfg config.Config, repos *webhook.Store, d webhook.Deployer, log *slog.Logger) http.Handler {
	switch n := len(repos.Targets()); {
	case cfg.WebhookSecret == "":
		log.Info("GitHub webhooks disabled (webhook_secret is empty)")
	case n == 0:
		log.Warn("GitHub webhooks enabled, but no deploy targets", "apps_file", cfg.AppsFile)
	default:
		log.Info("GitHub webhooks enabled", "targets", n)
	}
	return webhook.New(cfg.WebhookSecret, repos, d, log)
}

// logDeployer records deploy requests when deploys are off (no Docker).
type logDeployer struct{ log *slog.Logger }

func (d logDeployer) Deploy(r webhook.Request) error {
	d.log.Warn("deploy requested, but deploys are off (no Docker daemon)", "repo", r.Repo, "branch", r.Branch, "commit", r.Commit)
	return nil
}

// openProxy publishes the apps in routes at <name>.<proxy_domain> through
// Caddy, and over mDNS for a .local domain. It returns nil when
// proxy_domain is empty.
func openProxy(ctx context.Context, cfg config.Config, routes *router.Router, log *slog.Logger) (*caddy.Proxy, error) {
	if cfg.ProxyDomain == "" {
		log.Info("app domains disabled (proxy_domain is empty)")
		return nil, nil
	}
	p, err := caddy.New(caddy.Options{
		Admin:  cfg.CaddyAdmin,
		Domain: cfg.ProxyDomain,
		Listen: cfg.ProxyListen,
		TLS:    cfg.ProxyTLS,
		Logger: log,
	})
	if err != nil {
		return nil, err
	}
	// Each watcher gets its own channel; a pending signal is enough, since
	// a sync reads the latest routes.
	var watchers []chan struct{}
	watch := func() <-chan struct{} {
		c := make(chan struct{}, 1)
		watchers = append(watchers, c)
		return c
	}
	go p.Run(ctx, watch(), routes.Ports, 30*time.Second)
	log.Info("publishing apps through caddy", "domain", cfg.ProxyDomain, "listen", cfg.ProxyListen, "tls", cfg.ProxyTLS, "admin", cfg.CaddyAdmin)

	if mdns.Publishes(cfg.ProxyDomain) {
		if avahi, err := mdns.Connect(); err != nil {
			log.Warn("app names are not published on the LAN: no avahi daemon", "err", err)
		} else {
			names := func() []string { return slices.Collect(maps.Keys(routes.Ports())) }
			pub := mdns.New(avahi, cfg.ProxyDomain, log)
			go func() {
				pub.Run(ctx, watch(), names, 30*time.Second)
				avahi.Close()
			}()
		}
	}
	routes.OnChange(func() {
		for _, c := range watchers {
			select {
			case c <- struct{}{}:
			default:
			}
		}
	})
	return p, nil
}

// openPipeline starts the build pipeline if a Docker daemon is reachable,
// and returns nil (deploys off) otherwise.
func openPipeline(ctx context.Context, cfg config.Config, repos webhook.TargetSource, access *gitauth.Access, routes *router.Router, proxy *caddy.Proxy, alerts *alert.Engine, notifier *notify.Notifier, log *slog.Logger) (*deploy.Pipeline, error) {
	sock, err := docker.SocketPath(os.Getenv)
	if err != nil {
		return nil, err
	}
	if !docker.Available(sock) {
		log.Info("deploys disabled: no Docker daemon", "socket", sock, "setup", "docs/DOCKER.md")
		return nil, nil
	}
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = config.DefaultDataDir()
	}
	secretStore := &secrets.Store{Dir: filepath.Join(filepath.Dir(cfg.AppsFile), "secrets")}
	dbs := &databases.Manager{Docker: docker.New(sock), DockerHost: "unix://" + sock, Dir: filepath.Join(filepath.Dir(cfg.AppsFile), "databases")}
	steps := deploy.ExecSteps{
		Docker:     docker.New(sock),
		Router:     routes,
		DockerHost: "unix://" + sock,
		Secrets:    secretStore,
		Git:        access,
		Databases:  dbs,
	}
	opts := deploy.Options{
		Targets:   repos,
		Git:       access,
		Secrets:   secretStore,
		Databases: dbs,
		Dir:       filepath.Join(dataDir, "deploys"),
		Steps:     steps,
		Runtime:   steps,
		Logger:    log,
		OnFinish: func(b deploy.Build) {
			if b.State == deploy.Succeeded || b.State == deploy.Failed {
				alerts.DeployFinished(notify.Target(b.Repo, b.Branch), b.App, b.State == deploy.Failed, b.Error, b.Finished)
				notifier.Build(b)
			}
		},
	}
	if proxy != nil {
		opts.AppURL = proxy.URL
		notifier.AppURL = proxy.URL
	}
	p, err := deploy.New(opts)
	if err != nil {
		return nil, fmt.Errorf("deploys: %w", err)
	}
	// Apps kept running while piway was down; serve them again.
	if err := steps.Restore(ctx, p.Live); err != nil {
		log.Warn("restore app routes", "err", err)
	}
	for _, site := range p.LiveSites() {
		if err := steps.ServeSite(site); err != nil {
			log.Warn("restore site", "app", site.App, "err", err)
		}
	}
	log.Info("deploys enabled", "docker", sock)
	return p, nil
}

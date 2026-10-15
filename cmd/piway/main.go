// Command piway is a self-hosted metrics agent and deploy platform for Raspberry Pi.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/ofk0/piway/internal/agent"
	"github.com/ofk0/piway/internal/caddy"
	"github.com/ofk0/piway/internal/config"
	"github.com/ofk0/piway/internal/deploy"
	"github.com/ofk0/piway/internal/docker"
	"github.com/ofk0/piway/internal/history"
	"github.com/ofk0/piway/internal/mdns"
	"github.com/ofk0/piway/internal/recorder"
	"github.com/ofk0/piway/internal/router"
	"github.com/ofk0/piway/internal/server"
	"github.com/ofk0/piway/internal/stream"
	"github.com/ofk0/piway/internal/version"
	"github.com/ofk0/piway/internal/webhook"
	"github.com/ofk0/piway/internal/webui"
)

func main() {
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
	pipeline, err := openPipeline(ctx, cfg, routes, proxy, log)
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

	hooks, err := openWebhooks(cfg, deployer, log)
	if err != nil {
		return err
	}
	handler, err := server.New(server.Deps{
		Snapshots: ag, Live: live, History: queries, Webhooks: hooks, Builds: builds, UI: ui,
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
// the deploy targets in apps_file.
func openWebhooks(cfg config.Config, d webhook.Deployer, log *slog.Logger) (http.Handler, error) {
	targets, err := webhook.LoadTargets(cfg.AppsFile)
	if err != nil {
		return nil, err
	}
	switch {
	case cfg.WebhookSecret == "":
		log.Info("GitHub webhooks disabled (webhook_secret is empty)")
	case len(targets) == 0:
		log.Warn("GitHub webhooks enabled, but no deploy targets", "apps_file", cfg.AppsFile)
	default:
		log.Info("GitHub webhooks enabled", "targets", len(targets))
	}
	return webhook.New(cfg.WebhookSecret, targets, d, log), nil
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
func openPipeline(ctx context.Context, cfg config.Config, routes *router.Router, proxy *caddy.Proxy, log *slog.Logger) (*deploy.Pipeline, error) {
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
	steps := deploy.ExecSteps{
		Docker:     docker.New(sock),
		Router:     routes,
		DockerHost: "unix://" + sock,
		SecretsDir: filepath.Join(filepath.Dir(cfg.AppsFile), "secrets"),
	}
	opts := deploy.Options{
		Dir:     filepath.Join(dataDir, "deploys"),
		Steps:   steps,
		Runtime: steps,
		Logger:  log,
	}
	if proxy != nil {
		opts.AppURL = proxy.URL
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

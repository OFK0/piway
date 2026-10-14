// Command piway is a self-hosted metrics agent and deploy platform for Raspberry Pi.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ofk0/piway/internal/agent"
	"github.com/ofk0/piway/internal/config"
	"github.com/ofk0/piway/internal/recorder"
	"github.com/ofk0/piway/internal/server"
	"github.com/ofk0/piway/internal/stream"
	"github.com/ofk0/piway/internal/tsdb"
	"github.com/ofk0/piway/internal/version"
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

	store, err := openHistory(cfg, log)
	if err != nil {
		return err
	}
	if store != nil {
		ag.OnPublish(recorder.New(store, log).Record)
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
	// The store closes last: Close writes pending samples, and the agent
	// must not record into a closed store.
	defer func() {
		stop()
		<-agentDone
		live.Wait()
		if store != nil {
			if err := store.Close(); err != nil {
				log.Error("close metrics history", "err", err)
			}
		}
	}()

	ui := webui.FS()
	handler, err := server.New(ag, live, ui)
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
func openHistory(cfg config.Config, log *slog.Logger) (*tsdb.Store, error) {
	if cfg.DataDir == "" {
		log.Info("metrics history disabled (data_dir is empty)")
		return nil, nil
	}
	store, err := tsdb.Open(cfg.DataDir, tsdb.Options{FlushInterval: cfg.FlushInterval, Logger: log})
	if err != nil {
		return nil, fmt.Errorf("metrics history: %w", err)
	}
	log.Info("recording metrics", "dir", cfg.DataDir, "flush_interval", cfg.FlushInterval)
	return store, nil
}

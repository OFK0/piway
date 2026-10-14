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
	"github.com/ofk0/piway/internal/server"
	"github.com/ofk0/piway/internal/stream"
	"github.com/ofk0/piway/internal/version"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	interval := flag.Duration("interval", time.Second, "metric collection interval")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("piway %s (%s)\n", version.Version, version.Commit)
		return
	}

	if err := run(*addr, *interval); err != nil {
		slog.Error("piway exited", "err", err)
		os.Exit(1)
	}
}

// streamQueueLen is how many snapshots a WebSocket client may fall behind
// before it is disconnected: 8 rounds is 8 seconds at the default interval.
const streamQueueLen = 8

func run(addr string, interval time.Duration) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log := slog.Default()
	ag, err := agent.New(interval, log, agent.OpenDefault(log)...)
	if err != nil {
		return err
	}
	defer ag.Close()

	hub := stream.NewHub(streamQueueLen)
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
	defer func() {
		stop()
		<-agentDone
		live.Wait()
	}()

	srv := &http.Server{
		Addr:              addr,
		Handler:           server.New(ag, live),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("piway listening", "addr", addr, "version", version.Version)
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

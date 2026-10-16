package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/ofk0/piway/internal/docker"
)

// Crash is a live app container that exited without piway stopping it.
// Docker restarts it (unless-stopped); Watch then routes to it again.
type Crash struct {
	App         string
	OOM         bool  // killed for using more memory than run.memory
	ExitCode    int   // as the die event reports it
	MemoryLimit int64 // run.memory as the container has it; 0 if none
	At          time.Time
}

// Summary says what happened, for an alert.
func (c Crash) Summary() string {
	if c.OOM {
		s := fmt.Sprintf("%s was killed for running out of memory", c.App)
		if c.MemoryLimit > 0 {
			s += fmt.Sprintf(" (run.memory %d MiB)", c.MemoryLimit>>20)
		}
		return s + "; Docker restarted it"
	}
	return fmt.Sprintf("%s exited with code %d; Docker restarted it", c.App, c.ExitCode)
}

// reconcileEvery is how often Watch checks every route, in case it missed
// an event (the stream broke, or piway started meanwhile).
const reconcileEvery = 30 * time.Second

// Watch keeps each app's route pointed at its live container and reports
// crashes, until ctx ends. A container gets a new host port whenever it
// starts, so when Docker restarts a crashed one, the route must follow
// (#132). Only the live build's container is followed: during a deploy the
// new container is not routed to before its health check passes.
func (s ExecSteps) Watch(ctx context.Context, live func(app string) string, onCrash func(Crash), log *slog.Logger) {
	go func() {
		t := time.NewTicker(reconcileEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.Reconcile(ctx, live, "", log)
			}
		}
	}()

	var mu sync.Mutex
	stopped := map[string]bool{} // containers piway (or someone) stopped on purpose
	oom := map[string]bool{}
	backoff := time.Second
	for ctx.Err() == nil {
		err := s.Docker.Events(ctx, docker.LabelApp, []string{"start", "die", "oom", "kill", "stop"}, func(ev docker.Event) {
			app := ev.Attributes[docker.LabelApp]
			mu.Lock()
			defer mu.Unlock()
			switch ev.Action {
			case "kill", "stop":
				stopped[ev.ID] = true
			case "oom":
				oom[ev.ID] = true
			case "die":
				intentional, killed := stopped[ev.ID], oom[ev.ID]
				delete(stopped, ev.ID)
				delete(oom, ev.ID)
				if intentional || ev.Attributes["piway.build"] != live(app) || onCrash == nil {
					return
				}
				code, _ := strconv.Atoi(ev.Attributes["exitCode"])
				c := Crash{App: app, OOM: killed, ExitCode: code, At: ev.Time}
				if d, err := s.Docker.Details(ctx, ev.ID); err == nil {
					c.MemoryLimit = d.MemoryLimit
				}
				onCrash(c)
			case "start":
				delete(stopped, ev.ID)
				if ev.Attributes["piway.build"] == live(app) {
					s.Reconcile(ctx, live, app, log)
				}
			}
		})
		if ctx.Err() != nil {
			return
		}
		log.Warn("docker events stopped; reconnecting", "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
		s.Reconcile(ctx, live, "", log) // catch up on what was missed
	}
}

// Reconcile points the route of app (every app when "") at the port its
// live container is published on now, if that changed. Apps whose live
// container does not run are left alone: Docker is restarting it, or a
// deploy is replacing it.
func (s ExecSteps) Reconcile(ctx context.Context, live func(app string) string, only string, log *slog.Logger) {
	all, err := s.Docker.List(ctx)
	if err != nil {
		log.Warn("reconcile routes", "err", err)
		return
	}
	for _, c := range all {
		app := c.App()
		if (only != "" && app != only) || c.State != "running" || c.Labels["piway.build"] != live(app) {
			continue
		}
		d, err := s.Docker.Details(ctx, c.ID)
		if err != nil {
			continue
		}
		for _, port := range d.Ports {
			target := fmt.Sprintf("http://127.0.0.1:%d", port)
			if s.Router.Target(app) == target {
				break
			}
			if err := s.Router.Set(app, HostPort(app), target); err != nil {
				log.Warn("reroute", "app", app, "err", err)
				break
			}
			log.Info("rerouted to the restarted container", "app", app, "target", target, "restarts", d.RestartCount)
			break
		}
	}
}

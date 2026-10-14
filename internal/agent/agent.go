// Package agent runs the metric collectors on a fixed interval and publishes
// each round as an immutable Snapshot.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ofk0/piway/internal/collector/cpu"
	"github.com/ofk0/piway/internal/collector/disk"
	"github.com/ofk0/piway/internal/collector/mem"
	"github.com/ofk0/piway/internal/collector/netdev"
	"github.com/ofk0/piway/internal/collector/thermal"
)

// Snapshot is every metric gathered in one collection round.
//
// A published Snapshot is never modified again, so any number of goroutines
// may read it without locking. A nil field means the collector is unavailable
// on this machine, failed this round (see Errors), or has no baseline yet.
type Snapshot struct {
	Seq     uint64             `json:"seq"`
	Time    time.Time          `json:"time"`
	CPU     *cpu.Usage         `json:"cpu"`
	Memory  *mem.Info          `json:"memory"`
	Thermal *thermal.Reading   `json:"thermal"`
	Disk    *disk.Stats        `json:"disk"`
	Network []netdev.Interface `json:"network"`
	// Errors maps collector name to the error it returned this round.
	Errors map[string]string `json:"errors,omitempty"`
}

// Collector fills its own fields of a Snapshot.
//
// Collect runs concurrently with the other collectors of the same round, so it
// must only write the Snapshot fields it owns. It is never called concurrently
// with itself. It must not keep references to s after returning.
type Collector interface {
	Name() string
	Collect(ctx context.Context, now time.Time, s *Snapshot) error
	Close() error
}

// Agent periodically runs its collectors and publishes the result.
type Agent struct {
	interval   time.Duration
	collectors []Collector
	log        *slog.Logger

	latest atomic.Pointer[Snapshot]

	// Owned by the Run goroutine.
	seq     uint64
	lastErr []string
}

// New returns an Agent that collects every interval. It takes ownership of
// the collectors and closes them in Close.
func New(interval time.Duration, log *slog.Logger, collectors ...Collector) (*Agent, error) {
	if interval <= 0 {
		return nil, fmt.Errorf("agent: interval must be positive, got %v", interval)
	}
	return &Agent{
		interval:   interval,
		collectors: collectors,
		log:        log,
		lastErr:    make([]string, len(collectors)),
	}, nil
}

// Latest returns the most recent Snapshot, or nil before the first round.
// It is safe to call from any goroutine.
func (a *Agent) Latest() *Snapshot {
	return a.latest.Load()
}

// Run collects immediately and then on every tick until ctx is cancelled.
//
// If a round takes longer than the interval, time.Ticker drops the missed
// ticks instead of queueing them, so a slow machine degrades to a lower
// sample rate rather than falling ever further behind.
func (a *Agent) Run(ctx context.Context) {
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	a.publish(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.publish(ctx)
		}
	}
}

func (a *Agent) publish(ctx context.Context) {
	s := a.collect(ctx, time.Now())
	a.seq++
	s.Seq = a.seq
	a.latest.Store(s)
}

// collect runs one round. Each collector gets its own goroutine so a slow
// source (vcgencmd spawns a process) does not delay the others.
func (a *Agent) collect(ctx context.Context, now time.Time) *Snapshot {
	// A round must not outlive its interval. Collectors that honour ctx stop
	// here; the rest are expected to be fast file reads.
	ctx, cancel := context.WithTimeout(ctx, a.interval)
	defer cancel()

	s := &Snapshot{Time: now}
	errs := make([]error, len(a.collectors))

	var wg sync.WaitGroup
	for i, c := range a.collectors {
		wg.Go(func() {
			errs[i] = safeCollect(ctx, c, now, s)
		})
	}
	wg.Wait()

	for i, err := range errs {
		name := a.collectors[i].Name()
		msg := ""
		if err != nil {
			msg = err.Error()
			if s.Errors == nil {
				s.Errors = make(map[string]string)
			}
			s.Errors[name] = msg
		}
		a.logTransition(i, name, msg)
	}
	return s
}

// logTransition logs when a collector starts failing, changes its error, or
// recovers, instead of repeating the same error every second.
func (a *Agent) logTransition(i int, name, msg string) {
	if msg == a.lastErr[i] {
		return
	}
	if msg == "" {
		a.log.Info("collector recovered", "collector", name)
	} else {
		a.log.Warn("collector failed", "collector", name, "err", msg)
	}
	a.lastErr[i] = msg
}

// safeCollect turns a panic into an error so one broken collector cannot
// take down the agent or the other collectors.
func safeCollect(ctx context.Context, c Collector, now time.Time, s *Snapshot) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	return c.Collect(ctx, now, s)
}

// Close closes all collectors. Call it after Run has returned.
func (a *Agent) Close() error {
	var errs []error
	for _, c := range a.collectors {
		if err := c.Close(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.Name(), err))
		}
	}
	return errors.Join(errs...)
}

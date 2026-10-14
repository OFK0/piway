package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/ofk0/piway/internal/collector/cpu"
	"github.com/ofk0/piway/internal/collector/disk"
	"github.com/ofk0/piway/internal/collector/mem"
	"github.com/ofk0/piway/internal/collector/netdev"
	"github.com/ofk0/piway/internal/collector/thermal"
)

// The adapters below hand every collector fresh destination values instead of
// reusing buffers between rounds: published snapshots are shared with readers,
// so their slices must never be written again.

type cpuCollector struct{ c *cpu.Collector }

func (cpuCollector) Name() string   { return "cpu" }
func (x cpuCollector) Close() error { return x.c.Close() }

func (x cpuCollector) Collect(_ context.Context, _ time.Time, s *Snapshot) error {
	var u cpu.Usage
	ok, err := x.c.Sample(&u)
	if err != nil {
		return err
	}
	if ok {
		s.CPU = &u
	}
	return nil
}

type memCollector struct{ c *mem.Collector }

func (memCollector) Name() string   { return "memory" }
func (x memCollector) Close() error { return x.c.Close() }

func (x memCollector) Collect(_ context.Context, _ time.Time, s *Snapshot) error {
	info, err := x.c.Sample()
	if err != nil {
		return err
	}
	s.Memory = &info
	return nil
}

type thermalCollector struct{ c *thermal.Collector }

func (thermalCollector) Name() string   { return "thermal" }
func (x thermalCollector) Close() error { return x.c.Close() }

func (x thermalCollector) Collect(ctx context.Context, _ time.Time, s *Snapshot) error {
	r, err := x.c.Sample(ctx)
	if err != nil {
		return err
	}
	s.Thermal = &r
	return nil
}

type diskCollector struct{ c *disk.Collector }

func (diskCollector) Name() string   { return "disk" }
func (x diskCollector) Close() error { return x.c.Close() }

func (x diskCollector) Collect(_ context.Context, now time.Time, s *Snapshot) error {
	var st disk.Stats
	if err := x.c.Sample(now, &st); err != nil {
		return err
	}
	s.Disk = &st
	return nil
}

type netCollector struct{ c *netdev.Collector }

func (netCollector) Name() string   { return "network" }
func (x netCollector) Close() error { return x.c.Close() }

func (x netCollector) Collect(_ context.Context, now time.Time, s *Snapshot) error {
	ifs, err := x.c.Sample(now, nil)
	if err != nil {
		return err
	}
	s.Network = ifs
	return nil
}

// OpenDefault opens every built-in collector with its default configuration.
// A collector that cannot be opened (for example on a non-Linux machine) is
// logged and skipped rather than failing the whole agent.
func OpenDefault(log *slog.Logger) []Collector {
	var cs []Collector
	add := func(name string, c Collector, err error) {
		if err != nil {
			log.Warn("collector unavailable", "collector", name, "err", err)
			return
		}
		cs = append(cs, c)
	}

	c, err := cpu.New(cpu.DefaultPath)
	add("cpu", cpuCollector{c}, err)

	m, err := mem.New(mem.DefaultPath)
	add("memory", memCollector{m}, err)

	t, err := thermal.New(thermal.DefaultConfig())
	add("thermal", thermalCollector{t}, err)

	d, err := disk.New(disk.DefaultConfig())
	add("disk", diskCollector{d}, err)

	n, err := netdev.New(netdev.DefaultPath, netdev.DefaultSkip)
	add("network", netCollector{n}, err)

	return cs
}

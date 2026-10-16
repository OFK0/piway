package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/ofk0/piway/internal/backup"
	"github.com/ofk0/piway/internal/config"
	"github.com/ofk0/piway/internal/deploy"
	"github.com/ofk0/piway/internal/docker"
	"github.com/ofk0/piway/internal/version"
)

// runBackup implements `piway backup`: one archive with the configuration,
// the state worth keeping and every app volume (docs/BACKUP.md).
func runBackup(args []string) int {
	fs := flag.NewFlagSet("piway backup", flag.ContinueOnError)
	out := fs.String("o", "", "`file` to write; - for standard output (default piway-backup-<time>.tar.gz here)")
	metrics := fs.Bool("metrics", false, "include the metrics history")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: piway backup [-o file] [-metrics]\n\nWrites a .tar.gz with piway's configuration (including secrets and deploy\nkeys, so keep it private), its deploy records and tokens, and every app\nvolume. SQLite databases in volumes are copied with SQLite's online backup.\nSee docs/BACKUP.md for restoring.")
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
	loaded, err := config.Load(nil, os.LookupEnv, config.DefaultPath(), io.Discard)
	if err != nil {
		fmt.Fprintln(os.Stderr, "piway:", err)
		return 2
	}
	cfg := loaded.Config
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = config.DefaultDataDir()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	opts := backup.Options{
		ConfigDir: filepath.Dir(cfg.AppsFile),
		DataDir:   dataDir,
		Metrics:   *metrics,
		Version:   version.Version,
		Log:       os.Stderr,
	}
	if sock, err := docker.SocketPath(os.Getenv); err == nil && docker.Available(sock) {
		vs, err := docker.New(sock).Volumes(ctx, deploy.LabelVolume)
		if err != nil {
			fmt.Fprintln(os.Stderr, "piway: list volumes:", err)
			return 1
		}
		for _, v := range vs {
			opts.Volumes = append(opts.Volumes, v.Name)
		}
		opts.Reader = &backup.Docker{Host: "unix://" + sock, Log: os.Stderr}
	} else {
		fmt.Fprintln(os.Stderr, "piway: no Docker daemon, so no volumes are backed up")
	}

	if *out == "-" {
		if _, err := backup.Write(ctx, os.Stdout, opts); err != nil {
			fmt.Fprintln(os.Stderr, "piway:", err)
			return 1
		}
		return 0
	}
	name := *out
	if name == "" {
		name = "piway-backup-" + time.Now().Format("20060102-150405") + ".tar.gz"
	}
	// Written next to its final name, readable only by this user (it holds
	// secrets), and renamed into place only when complete.
	f, err := os.CreateTemp(filepath.Dir(name), ".piway-backup-*.tmp")
	if err != nil {
		fmt.Fprintln(os.Stderr, "piway:", err)
		return 1
	}
	defer os.Remove(f.Name())
	m, err := backup.Write(ctx, f, opts)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), name)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "piway:", err)
		return 1
	}
	fi, _ := os.Stat(name)
	fmt.Fprintf(os.Stderr, "Wrote %s (%d bytes, %d volumes). It holds secrets: keep it private.\n", name, fi.Size(), len(m.Volumes))
	return 0
}

// runRestore implements `piway restore`: it puts a backup back.
func runRestore(args []string) int {
	fs := flag.NewFlagSet("piway restore", flag.ContinueOnError)
	force := fs.Bool("force", false, "replace files and volumes that exist (volumes are emptied first)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: piway restore [-force] backup.tar.gz\n\nPuts a backup from piway backup back: the configuration, the state and the\nvolumes. Stop piway first (systemctl --user stop piway). Without -force,\nnothing is changed if anything would be overwritten. See docs/BACKUP.md.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	loaded, err := config.Load(nil, os.LookupEnv, config.DefaultPath(), io.Discard)
	if err != nil {
		fmt.Fprintln(os.Stderr, "piway:", err)
		return 2
	}
	cfg := loaded.Config
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = config.DefaultDataDir()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	opts := backup.RestoreOptions{ConfigDir: filepath.Dir(cfg.AppsFile), DataDir: dataDir, Force: *force, Log: os.Stderr}
	if sock, err := docker.SocketPath(os.Getenv); err == nil && docker.Available(sock) {
		opts.Writer = &backup.Docker{Host: "unix://" + sock, Log: os.Stderr}
	}
	if err := backup.Restore(ctx, fs.Arg(0), opts); err != nil {
		fmt.Fprintln(os.Stderr, "piway:", err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "Restored. Start piway, then deploy each app once: images are not in backups, so piway builds them again.")
	return 0
}

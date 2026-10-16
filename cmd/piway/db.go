package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/ofk0/piway/internal/config"
	"github.com/ofk0/piway/internal/databases"
	"github.com/ofk0/piway/internal/docker"
	"golang.org/x/term"
)

const dbUsage = `usage: piway db <command>

  backup [app]                       back up every app's databases now, or one app's
  list [app]                         show the backups
  restore [-engine e] [-from file] [-yes] app
                                     replace an app's database with a backup
                                     (the newest unless -from)

Backups are logical dumps (pg_dump, mariadb-dump, Redis DUMP), one file per
app and engine, kept 7 deep. piway makes them every night at 03:30. Copy
them off the Pi: see docs/DATABASES.md.`

// newDatabases is the database manager for cfg on the Docker at sock.
func newDatabases(cfg config.Config, sock, dataDir string) *databases.Manager {
	return &databases.Manager{
		Docker:     docker.New(sock),
		DockerHost: "unix://" + sock,
		Dir:        filepath.Join(filepath.Dir(cfg.AppsFile), "databases"),
		BackupDir:  filepath.Join(dataDir, "db-backups"),
	}
}

// runDB implements `piway db`.
func runDB(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "-help" || args[0] == "--help" {
		fmt.Fprintln(os.Stderr, dbUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	loaded, err := config.Load(nil, os.LookupEnv, config.DefaultPath(), io.Discard)
	if err != nil {
		fmt.Fprintln(os.Stderr, "piway:", err)
		return 2
	}
	dataDir := loaded.Config.DataDir
	if dataDir == "" {
		dataDir = config.DefaultDataDir()
	}
	sock, err := docker.SocketPath(os.Getenv)
	if err != nil || !docker.Available(sock) {
		fmt.Fprintln(os.Stderr, "piway: no Docker daemon, so no databases (docs/DOCKER.md)")
		return 1
	}
	m := newDatabases(loaded.Config, sock, dataDir)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return dbCommand(ctx, m, args[0], args[1:], os.Stdin, os.Stdout, os.Stderr)
}

func dbCommand(ctx context.Context, m *databases.Manager, cmd string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("piway db "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	engine := fs.String("engine", "", "restore: the `engine` (postgres, mariadb or redis) when the app has several")
	from := fs.String("from", "", "restore: the backup `file` (default the newest)")
	yes := fs.Bool("yes", false, "restore: do not ask for confirmation")
	var rest []string
	for len(args) > 0 { // flags may follow the app
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		rest = append(rest, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(rest) > 1 {
		fmt.Fprintln(stderr, dbUsage)
		return 2
	}
	app := ""
	if len(rest) == 1 {
		app = rest[0]
	}

	// enginesOf lists the engines app has a database on.
	enginesOf := func(app string) []string {
		var out []string
		for _, e := range databases.Names() {
			apps, _ := m.Apps(e)
			if slices.Contains(apps, app) {
				out = append(out, e)
			}
		}
		return out
	}

	switch cmd {
	case "backup":
		if app == "" {
			if err := m.BackupAll(ctx, databases.Keep, stdout); err != nil {
				fmt.Fprintln(stderr, "piway:", err)
				return 1
			}
			return 0
		}
		list := enginesOf(app)
		if len(list) == 0 {
			fmt.Fprintf(stderr, "piway: %s has no databases\n", app)
			return 1
		}
		code := 0
		for _, e := range list {
			b, err := m.BackupApp(ctx, e, app, databases.Keep)
			if err != nil {
				fmt.Fprintln(stderr, "piway:", err)
				code = 1
				continue
			}
			fmt.Fprintf(stdout, "%s/%s: %s (%d bytes)\n", e, app, b.Path, b.Size)
		}
		return code

	case "list", "ls":
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ENGINE\tAPP\tTIME\tSIZE\tFILE")
		n := 0
		for _, e := range databases.Names() {
			apps, err := m.Apps(e)
			if err != nil {
				fmt.Fprintln(stderr, "piway:", err)
				return 1
			}
			for _, a := range apps {
				if app != "" && a != app {
					continue
				}
				bs, _ := m.Backups(e, a)
				for _, b := range bs {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", e, a, b.Time.Format("2006-01-02 15:04:05"), b.Size, b.Path)
					n++
				}
			}
		}
		if n == 0 {
			fmt.Fprintln(stdout, "No database backups yet. Make one: piway db backup")
			return 0
		}
		tw.Flush()
		return 0

	case "restore":
		if app == "" {
			fmt.Fprintln(stderr, "usage: piway db restore [-engine e] [-from file] [-yes] app")
			return 2
		}
		e := *engine
		if e == "" {
			list := enginesOf(app)
			switch len(list) {
			case 0:
				fmt.Fprintf(stderr, "piway: %s has no databases\n", app)
				return 1
			case 1:
				e = list[0]
			default:
				fmt.Fprintf(stderr, "piway: %s has databases on %s; choose one with -engine\n", app, strings.Join(list, " and "))
				return 2
			}
		}
		path := *from
		if path == "" {
			bs, err := m.Backups(e, app)
			if err != nil || len(bs) == 0 {
				fmt.Fprintf(stderr, "piway: no backups of %s on %s (piway db backup %s)\n", app, e, app)
				return 1
			}
			path = bs[0].Path
		}
		if !*yes && !confirm(stdin, stderr, fmt.Sprintf("This replaces %s's %s database with %s.\nThe app sees errors meanwhile; restart it afterwards. Type the app's name to go on: ", app, e, path), app) {
			fmt.Fprintln(stderr, "Nothing changed.")
			return 1
		}
		if err := m.Restore(ctx, e, app, path); err != nil {
			fmt.Fprintln(stderr, "piway:", err)
			return 1
		}
		fmt.Fprintf(stdout, "Restored %s's %s database from %s. Restart the app: its page, or POST /api/v1/apps/%s/restart.\n", app, e, path, app)
		return 0
	}
	fmt.Fprintln(stderr, dbUsage)
	return 2
}

// confirm asks on a terminal and reads one line; without a terminal it
// says no, so scripts must pass -yes.
func confirm(stdin io.Reader, stderr io.Writer, prompt, want string) bool {
	f, ok := stdin.(*os.File)
	if ok && !term.IsTerminal(int(f.Fd())) {
		fmt.Fprintln(stderr, "piway: not a terminal; pass -yes to restore without asking")
		return false
	}
	fmt.Fprint(stderr, prompt)
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	return strings.TrimSpace(line) == want
}

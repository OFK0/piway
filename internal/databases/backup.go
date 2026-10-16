package databases

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ofk0/piway/internal/docker"
)

// Keep is how many backups per app and engine are kept by default.
const Keep = 7

// Backup is one logical backup of an app's database.
type Backup struct {
	Engine string    `json:"engine"`
	App    string    `json:"app"`
	Time   time.Time `json:"time"`
	Path   string    `json:"path"`
	Size   int64     `json:"size_bytes"`
}

// ErrNotRunning means an engine has no running server.
var ErrNotRunning = errors.New("the engine is not running")

// stamp names backup files; it sorts by time.
const stamp = "20060102-150405"

// dumpScript is run in the Redis container with EVAL: every key under
// ARGV[1], one per line as hex(key) pttl hex(DUMP). Hex keeps any byte safe
// through redis-cli.
const dumpScript = `local function hex(s) return (s:gsub('.', function(c) return string.format('%02x', string.byte(c)) end)) end
local out = {}
local cursor = '0'
repeat
  local r = redis.call('SCAN', cursor, 'MATCH', ARGV[1], 'COUNT', 1000)
  cursor = r[1]
  for _, k in ipairs(r[2]) do
    local d = redis.call('DUMP', k)
    if d then
      local ttl = redis.call('PTTL', k)
      if ttl < 0 then ttl = 0 end
      table.insert(out, hex(k) .. ' ' .. ttl .. ' ' .. hex(d))
    end
  end
until cursor == '0'
return table.concat(out, '\n')`

// restoreScript puts one dumped key back: ARGV = hex(key) pttl hex(dump).
// It is on one line, as redis-cli reads one command per line.
const restoreScript = `local function unhex(h) return (h:gsub('..', function(x) return string.char(tonumber(x, 16)) end)) end redis.call('RESTORE', unhex(ARGV[1]), ARGV[2], unhex(ARGV[3]), 'REPLACE') return 1`

// clearScript deletes every key under ARGV[1].
const clearScript = `local n = 0 local cursor = '0' repeat local r = redis.call('SCAN', cursor, 'MATCH', ARGV[1], 'COUNT', 1000) cursor = r[1] for _, k in ipairs(r[2]) do redis.call('UNLINK', k) n = n + 1 end until cursor == '0' return n`

// dumpCmd writes app's data on e to standard output.
func dumpCmd(e *engine, app string) []string {
	switch e.name {
	case "postgres":
		// Plain SQL without owners or grants: restored as the app's role,
		// it owns everything again.
		return []string{"pg_dump", "-U", "postgres", "--no-owner", "--no-privileges", "-d", app}
	case "mariadb":
		return []string{"mariadb-dump", "-h", "127.0.0.1", "-u", "root", "--single-transaction",
			"--routines", "--triggers", "--events", "--skip-lock-tables", app}
	case "redis":
		return []string{"redis-cli", "--raw", "EVAL", dumpScript, "0", app + ":*"}
	}
	return nil
}

func ext(e string) string {
	if e == "redis" {
		return ".redis.gz"
	}
	return ".sql.gz"
}

// Apps lists the apps with a database on engine, from the stored
// passwords: an app keeps its database (and is listed) after it is gone,
// until its database is deleted.
func (m *Manager) Apps(engine string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(m.Dir, "apps"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var apps []string
	for _, ent := range entries {
		app, ok := strings.CutSuffix(ent.Name(), ".env")
		if !ok {
			continue
		}
		vals, err := m.apps().Values(app)
		if err != nil {
			return nil, err
		}
		if _, ok := vals[passwordKey(engine)]; ok {
			apps = append(apps, app)
		}
	}
	return apps, nil
}

// running reports whether engine's server runs.
func (m *Manager) running(ctx context.Context, engine string) (bool, error) {
	st, err := m.Docker.Inspect(ctx, containerName(engine))
	if errors.Is(err, docker.ErrNotFound) {
		return false, nil
	}
	return err == nil && st.Running, err
}

// BackupApp dumps app's database on engine to a new file under
// BackupDir/<engine>/<app>/ and prunes older ones beyond keep.
func (m *Manager) BackupApp(ctx context.Context, engine, app string, keep int) (Backup, error) {
	e, ok := engines[engine]
	if !ok {
		return Backup{}, fmt.Errorf("unknown database %q", engine)
	}
	if ok, err := m.running(ctx, engine); err != nil {
		return Backup{}, err
	} else if !ok {
		return Backup{}, fmt.Errorf("%s: %w", engine, ErrNotRunning)
	}
	dir := filepath.Join(m.BackupDir, engine, app)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Backup{}, err
	}
	now := m.now()
	path := filepath.Join(dir, now.Format(stamp)+ext(engine))
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return Backup{}, err
	}
	defer os.Remove(f.Name())
	gz := gzip.NewWriter(f)
	err = m.execStream(ctx, e, dumpCmd(e, app), nil, gz)
	if err == nil {
		err = gz.Close()
	}
	if err == nil {
		err = f.Chmod(0o600)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return Backup{}, fmt.Errorf("back up %s on %s: %w", app, engine, err)
	}
	m.prune(engine, app, keep)
	fi, _ := os.Stat(path)
	return Backup{Engine: engine, App: app, Time: now, Path: path, Size: fi.Size()}, nil
}

// BackupAll backs up every app on every running engine. Engines that do
// not run are skipped; one failure does not stop the others.
func (m *Manager) BackupAll(ctx context.Context, keep int, log io.Writer) error {
	var errs []error
	for _, engine := range Names() {
		ok, err := m.running(ctx, engine)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		apps, err := m.Apps(engine)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ok {
			if len(apps) > 0 {
				fmt.Fprintf(log, "%s is not running; its %d databases were not backed up\n", engine, len(apps))
			}
			continue
		}
		for _, app := range apps {
			b, err := m.BackupApp(ctx, engine, app, keep)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			fmt.Fprintf(log, "%s/%s: %s (%d bytes)\n", engine, app, b.Path, b.Size)
		}
	}
	return errors.Join(errs...)
}

// prune deletes app's backups on engine beyond the newest keep.
func (m *Manager) prune(engine, app string, keep int) {
	if keep <= 0 {
		return
	}
	list, _ := m.Backups(engine, app)
	for _, old := range list[min(keep, len(list)):] {
		os.Remove(old.Path)
	}
}

// Backups lists app's backups on engine, newest first.
func (m *Manager) Backups(engine, app string) ([]Backup, error) {
	dir := filepath.Join(m.BackupDir, engine, app)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Backup
	for _, ent := range entries {
		name, ok := strings.CutSuffix(ent.Name(), ext(engine))
		if !ok {
			continue
		}
		t, err := time.ParseInLocation(stamp, name, time.Local)
		if err != nil {
			continue
		}
		fi, err := ent.Info()
		if err != nil {
			continue
		}
		out = append(out, Backup{Engine: engine, App: app, Time: t, Path: filepath.Join(dir, ent.Name()), Size: fi.Size()})
	}
	slices.SortFunc(out, func(a, b Backup) int { return b.Time.Compare(a.Time) })
	return out, nil
}

// Restore replaces app's database on engine with the backup in path. The
// database is dropped and made again, so the app sees errors while this
// runs; restart it afterwards.
func (m *Manager) Restore(ctx context.Context, engine, app, path string) error {
	e, ok := engines[engine]
	if !ok {
		return fmt.Errorf("unknown database %q", engine)
	}
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return m.restoreLocked(ctx, e, app, path)
}

// restoreLocked is Restore; callers hold the lock.
func (m *Manager) restoreLocked(ctx context.Context, e *engine, app, path string) error {
	engine := e.name
	if ok, err := m.running(ctx, engine); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%s: %w", engine, ErrNotRunning)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	pw, err := m.appPassword(app, engine)
	if err != nil {
		return err
	}

	switch engine {
	case "postgres":
		drop := fmt.Sprintf(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%[1]s' AND pid <> pg_backend_pid();
DROP DATABASE IF EXISTS "%[1]s";
`, app)
		if err := m.exec(ctx, e, []string{"psql", "-v", "ON_ERROR_STOP=1", "-q", "-U", "postgres", "-d", "postgres"}, drop); err != nil {
			return err
		}
		if err := m.provisionLocked(ctx, e, app, pw); err != nil {
			return err
		}
		// As the app's role, so what the dump creates belongs to it.
		in := io.MultiReader(strings.NewReader(fmt.Sprintf("SET ROLE \"%s\";\n", app)), gz)
		return m.execStream(ctx, e, []string{"psql", "-v", "ON_ERROR_STOP=1", "-q", "-U", "postgres", "-d", app}, in, io.Discard)
	case "mariadb":
		if err := m.exec(ctx, e, []string{"mariadb", "-h", "127.0.0.1", "-u", "root"}, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`;\n", app)); err != nil {
			return err
		}
		if err := m.provisionLocked(ctx, e, app, pw); err != nil {
			return err
		}
		return m.execStream(ctx, e, []string{"mariadb", "-h", "127.0.0.1", "-u", "root", app}, gz, io.Discard)
	case "redis":
		if err := m.exec(ctx, e, []string{"redis-cli", "EVAL", clearScript, "0", app + ":*"}, ""); err != nil {
			return err
		}
		if err := m.provisionLocked(ctx, e, app, pw); err != nil {
			return err
		}
		return m.redisLoad(ctx, e, gz)
	}
	return nil
}

// redisLoad feeds dumped keys to redis-cli as EVALSHA commands, after
// loading restoreScript once.
func (m *Manager) redisLoad(ctx context.Context, e *engine, r io.Reader) error {
	if err := m.exec(ctx, e, []string{"redis-cli", "SCRIPT", "LOAD", restoreScript}, ""); err != nil {
		return err
	}
	sum := sha1.Sum([]byte(restoreScript))
	sha := hex.EncodeToString(sum[:])
	pr, pw := io.Pipe()
	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(nil, 512<<20)
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) != 3 {
				continue
			}
			fmt.Fprintf(pw, "EVALSHA %s 0 %s %s %s\n", sha, f[0], f[1], f[2])
		}
		pw.CloseWithError(sc.Err())
	}()
	var out bytes.Buffer
	if err := m.execStream(ctx, e, []string{"redis-cli"}, pr, &out); err != nil {
		return err
	}
	if strings.Contains(out.String(), "ERR") {
		return fmt.Errorf("redis refused a key: %s", lastLine(out.String()))
	}
	return nil
}

// execStream runs cmd in e's container with stdin and stdout streamed,
// authenticated as admin through the environment.
func (m *Manager) execStream(ctx context.Context, e *engine, cmd []string, stdin io.Reader, stdout io.Writer) error {
	admin, err := m.adminPassword(e.name)
	if err != nil {
		return err
	}
	args := append([]string{"exec", "-i", "-e", e.adminEnv, containerName(e.name)}, cmd...)
	c := exec.CommandContext(ctx, "docker", args...)
	c.Env = append(os.Environ(), "DOCKER_HOST="+m.DockerHost, e.adminEnv+"="+admin)
	c.Stdin, c.Stdout = stdin, stdout
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return fmt.Errorf("%w: %s", err, lastLine(s))
		}
		return err
	}
	return nil
}

// provisionLocked creates or updates app's database and user on e.
// Callers hold m.mu.
func (m *Manager) provisionLocked(ctx context.Context, e *engine, app, pw string) error {
	cmd, stdin := e.provision(app, pw)
	if err := m.exec(ctx, e, cmd, stdin); err != nil {
		return fmt.Errorf("%s: set up %s's database: %w", e.name, app, err)
	}
	return nil
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Nightly backs every database up each day at the given time of day
// (local time) until ctx ends.
func (m *Manager) Nightly(ctx context.Context, at time.Duration, keep int, log func(msg string, err error)) {
	for {
		now := m.now()
		next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Add(at)
		if !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(next.Sub(now)):
		}
		var out bytes.Buffer
		err := m.BackupAll(ctx, keep, &out)
		log(strings.TrimSpace(out.String()), err)
	}
}

package databases

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestEnv(t *testing.T) {
	pw := func(e string) string { return e + "pw" }
	got := env("blog", []string{"redis", "mariadb", "postgres"}, pw)
	want := []Var{
		{"DATABASE_URL", "postgres://blog:postgrespw@piway-postgres:5432/blog?sslmode=disable"},
		{"POSTGRES_URL", "postgres://blog:postgrespw@piway-postgres:5432/blog?sslmode=disable"},
		{"REDIS_URL", "redis://blog:redispw@piway-redis:6379/0"},
		{"REDIS_PREFIX", "blog:"},
		{"MYSQL_URL", "mysql://blog:mariadbpw@piway-mariadb:3306/blog"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("env =\n%v\nwant\n%v", got, want)
	}
	// MariaDB alone has DATABASE_URL.
	if got := env("blog", []string{"mariadb"}, pw); got[0].Name != "DATABASE_URL" || !strings.HasPrefix(got[0].Value, "mysql://") {
		t.Errorf("mariadb alone: %v", got)
	}
}

func TestTuning(t *testing.T) {
	pg := strings.Join(engines["postgres"].cmd(true), " ")
	if !strings.Contains(pg, "synchronous_commit=off") || strings.Contains(strings.Join(engines["postgres"].cmd(false), " "), "synchronous_commit") {
		t.Errorf("postgres: %s", pg)
	}
	if !slices.Contains(engines["mariadb"].cmd(true), "--innodb-flush-log-at-trx-commit=2") ||
		!slices.Contains(engines["mariadb"].cmd(false), "--innodb-flush-log-at-trx-commit=1") {
		t.Error("mariadb flush setting")
	}
	sd, ssd := strings.Join(engines["redis"].cmd(true), " "), strings.Join(engines["redis"].cmd(false), " ")
	if !strings.Contains(sd, "--appendonly no") || !strings.Contains(ssd, "--appendonly yes") {
		t.Errorf("redis: %q / %q", sd, ssd)
	}
}

func TestProvision(t *testing.T) {
	_, sql := engines["postgres"].provision("my-app", "abc123")
	for _, want := range []string{`CREATE ROLE "my-app" LOGIN PASSWORD 'abc123'`, `CREATE DATABASE "my-app" OWNER "my-app"`, `REVOKE ALL ON DATABASE "my-app" FROM PUBLIC`} {
		if !strings.Contains(sql, want) {
			t.Errorf("postgres SQL lacks %q", want)
		}
	}
	_, sql = engines["mariadb"].provision("my-app", "abc123")
	if !strings.Contains(sql, "GRANT ALL PRIVILEGES ON `my-app`.* TO 'my-app'@'%'") {
		t.Errorf("mariadb SQL:\n%s", sql)
	}
	_, cmds := engines["redis"].provision("my-app", "abc123")
	if !strings.Contains(cmds, "ACL SETUSER my-app reset on >abc123 ~my-app:* &my-app:* +@all -@dangerous") || !strings.Contains(cmds, "ACL SAVE") {
		t.Errorf("redis: %s", cmds)
	}
	for name, e := range engines {
		if cmd, _ := e.provision("a", "SECRETPW"); strings.Contains(strings.Join(cmd, " "), "SECRETPW") {
			t.Errorf("%s: the password is on the command line", name)
		}
	}
}

func TestPasswords(t *testing.T) {
	m := &Manager{Dir: t.TempDir()}
	a1, err := m.adminPassword("postgres")
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := m.adminPassword("postgres")
	p1, _ := m.appPassword("blog", "postgres")
	p2, _ := m.appPassword("blog", "postgres")
	q, _ := m.appPassword("shop", "postgres")
	if a1 != a2 || p1 != p2 || len(p1) != 32 || p1 == q || p1 == a1 {
		t.Errorf("admin %s %s, blog %s %s, shop %s", a1, a2, p1, p2, q)
	}
	if fi, _ := os.Stat(filepath.Join(m.Dir, "apps", "blog.env")); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}

	vars, ok, err := m.Env("blog", []string{"postgres", "redis"})
	if err != nil || ok {
		t.Errorf("redis never prepared, so not ok: %v %v", ok, err)
	}
	m.appPassword("blog", "redis")
	vars, ok, _ = m.Env("blog", []string{"postgres", "redis"})
	if !ok || len(vars) != 4 || !strings.Contains(vars[0].Value, ":"+p1+"@") {
		t.Errorf("env %v %v", vars, ok)
	}
}

func TestDiskOf(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	os.WriteFile(file, nil, 0o600)
	var st syscall.Stat_t
	if err := syscall.Stat(file, &st); err != nil {
		t.Fatal(err)
	}
	dev := uint64(st.Dev)
	for disk, sd := range map[string]bool{"mmcblk0p2": true, "sda1": false, "nvme0n1p1": false} {
		sys := t.TempDir()
		os.MkdirAll(filepath.Join(sys, "dev", "block"), 0o755)
		link := filepath.Join(sys, "dev", "block", fmt.Sprintf("%d:%d", unix.Major(dev), unix.Minor(dev)))
		os.Symlink("../../devices/platform/x/block/"+disk, link)
		got, err := diskOf(file, sys)
		if err != nil || got != disk {
			t.Errorf("diskOf = %q, %v; want %s", got, err, disk)
		}
		if strings.HasPrefix(got, "mmcblk") != sd {
			t.Errorf("%s: sd = %v", disk, !sd)
		}
	}
	if !OnSDCard(filepath.Join(t.TempDir(), "missing")) {
		t.Error("an unknown disk should be treated as an SD card")
	}
}

func TestMajorAndVolume(t *testing.T) {
	for image, want := range map[string]string{
		"postgres:17-alpine": "piway-db-postgres-17",
		"mariadb:11.4":       "piway-db-mariadb-11.4",
		"redis:7.4-alpine":   "piway-db-redis-7.4",
		"redis":              "piway-db-redis-latest",
	} {
		name, _, _ := strings.Cut(image, ":")
		if got := volumeName(name, image); got != want {
			t.Errorf("volumeName(%s) = %s, want %s", image, got, want)
		}
	}
}

func TestBackupsListAndApps(t *testing.T) {
	m := &Manager{Dir: t.TempDir(), BackupDir: t.TempDir()}
	m.appPassword("blog", "postgres")
	m.appPassword("blog", "redis")
	m.appPassword("shop", "postgres")
	if apps, _ := m.Apps("postgres"); !slices.Equal(apps, []string{"blog", "shop"}) {
		t.Errorf("postgres apps %v", apps)
	}
	if apps, _ := m.Apps("mariadb"); len(apps) != 0 {
		t.Errorf("mariadb apps %v", apps)
	}

	dir := filepath.Join(m.BackupDir, "postgres", "blog")
	os.MkdirAll(dir, 0o700)
	for _, name := range []string{"20261014-033000.sql.gz", "20261016-033000.sql.gz", "20261015-033000.sql.gz", "notes.txt", ".tmp-1"} {
		os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600)
	}
	bs, err := m.Backups("postgres", "blog")
	if err != nil || len(bs) != 3 {
		t.Fatalf("backups %v %v", bs, err)
	}
	if bs[0].Time.Day() != 16 || bs[2].Time.Day() != 14 {
		t.Errorf("not newest first: %v", bs)
	}
	m.prune("postgres", "blog", 2)
	if bs, _ := m.Backups("postgres", "blog"); len(bs) != 2 || bs[1].Time.Day() != 15 {
		t.Errorf("after prune: %v", bs)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Error("prune deleted a file that is not a backup")
	}
}

func TestDumpCommands(t *testing.T) {
	for _, name := range Names() {
		cmd := strings.Join(dumpCmd(engines[name], "blog"), " ")
		if cmd == "" || !strings.Contains(cmd, "blog") {
			t.Errorf("%s: %q", name, cmd)
		}
	}
	if !strings.Contains(strings.Join(dumpCmd(engines["postgres"], "blog"), " "), "--no-owner") {
		t.Error("postgres dumps keep owners, which a restore as the app's role cannot set")
	}
	// redis-cli reads one command per line.
	if strings.Contains(restoreScript, "\n") || strings.Contains(clearScript, "\n") {
		t.Error("a Redis script that is fed through stdin spans lines")
	}
}

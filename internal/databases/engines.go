package databases

import (
	"fmt"
	"strings"
)

// engine is how piway runs one database server and gives apps a database
// on it.
type engine struct {
	name  string
	image string // major version pinned; upgrading is an explicit command
	port  int
	data  string // the data directory in the container
	// memory caps the container. It is enforced only where the memory
	// cgroup is on (docs/DOCKER.md); the engine settings below keep the
	// engine well under it anyway.
	memory int64
	// adminEnv is the variable the docker CLI passes into exec with the
	// admin password, so it is never on a command line.
	adminEnv string
	// env and cmd start the server; sd is true when its data is on an SD
	// card, where card life and speed come before the last second of
	// writes.
	env   func(admin string) []string
	cmd   func(sd bool) []string
	ready []string // exits 0 once the server takes connections
	// provision returns the client command and its standard input that
	// create (or update) app's database and user with password.
	provision func(app, password string) (cmd []string, stdin string)
	// urls are the variables an app gets.
	urls func(app, password string) []Var
}

// Var is an environment variable piway sets for an app.
type Var struct {
	Name  string
	Value string
}

const mib = 1 << 20

var engines = map[string]*engine{
	"postgres": {
		name:     "postgres",
		image:    "postgres:17-alpine",
		port:     5432,
		data:     "/var/lib/postgresql/data",
		memory:   256 * mib,
		adminEnv: "PGPASSWORD",
		env:      func(admin string) []string { return []string{"POSTGRES_PASSWORD=" + admin} },
		cmd: func(sd bool) []string {
			c := []string{"postgres", "-c", "shared_buffers=32MB", "-c", "max_connections=40", "-c", "work_mem=2MB"}
			if sd {
				// A crash can lose the last few hundred milliseconds of
				// commits, never corrupt the database; far fewer flushes.
				c = append(c, "-c", "synchronous_commit=off", "-c", "checkpoint_timeout=15min")
			}
			return c
		},
		ready: []string{"pg_isready", "-q", "-h", "127.0.0.1", "-U", "postgres"},
		provision: func(app, pw string) ([]string, string) {
			// App names are [a-z0-9-] and passwords hex, so quoting them is enough.
			sql := fmt.Sprintf(`DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%[1]s') THEN
    CREATE ROLE "%[1]s" LOGIN PASSWORD '%[2]s';
  ELSE
    ALTER ROLE "%[1]s" LOGIN PASSWORD '%[2]s';
  END IF;
END $$;
SELECT 'CREATE DATABASE "%[1]s" OWNER "%[1]s"' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '%[1]s')\gexec
REVOKE ALL ON DATABASE "%[1]s" FROM PUBLIC;
REVOKE CONNECT ON DATABASE postgres FROM PUBLIC;
`, app, pw)
			return []string{"psql", "-v", "ON_ERROR_STOP=1", "-q", "-U", "postgres", "-d", "postgres"}, sql
		},
		urls: func(app, pw string) []Var {
			u := fmt.Sprintf("postgres://%s:%s@piway-postgres:5432/%s?sslmode=disable", app, pw, app)
			return []Var{{"DATABASE_URL", u}, {"POSTGRES_URL", u}}
		},
	},
	"mariadb": {
		name:     "mariadb",
		image:    "mariadb:11.4",
		port:     3306,
		data:     "/var/lib/mysql",
		memory:   256 * mib,
		adminEnv: "MYSQL_PWD",
		env:      func(admin string) []string { return []string{"MARIADB_ROOT_PASSWORD=" + admin} },
		cmd: func(sd bool) []string {
			flush := "1"
			if sd {
				flush = "2" // written every commit, flushed once a second
			}
			return []string{"mariadbd", "--innodb-buffer-pool-size=64M", "--innodb-log-file-size=16M",
				"--performance-schema=OFF", "--skip-name-resolve", "--max-connections=40",
				"--innodb-flush-log-at-trx-commit=" + flush}
		},
		ready: []string{"mariadb-admin", "ping", "--silent", "-h", "127.0.0.1", "-u", "root"},
		provision: func(app, pw string) ([]string, string) {
			sql := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%[1]s`;\n"+
				"CREATE USER IF NOT EXISTS '%[1]s'@'%%' IDENTIFIED BY '%[2]s';\n"+
				"ALTER USER '%[1]s'@'%%' IDENTIFIED BY '%[2]s';\n"+
				"GRANT ALL PRIVILEGES ON `%[1]s`.* TO '%[1]s'@'%%';\n", app, pw)
			return []string{"mariadb", "-h", "127.0.0.1", "-u", "root"}, sql
		},
		urls: func(app, pw string) []Var {
			u := fmt.Sprintf("mysql://%s:%s@piway-mariadb:3306/%s", app, pw, app)
			return []Var{{"DATABASE_URL", u}, {"MYSQL_URL", u}}
		},
	},
	"redis": {
		name:     "redis",
		image:    "redis:7.4-alpine",
		port:     6379,
		data:     "/data",
		memory:   64 * mib,
		adminEnv: "REDISCLI_AUTH",
		env: func(admin string) []string {
			return []string{"PIWAY_ADMIN_HASH=" + sha256Hex(admin)}
		},
		cmd: func(sd bool) []string {
			persist := "--appendonly yes --appendfsync everysec"
			if sd {
				persist = `--appendonly no --save "900 1 300 100 60 10000"` // snapshots, not a log
			}
			// Users live in an ACL file in the volume, so they survive a
			// restart. The first start writes the admin user; the image's
			// entrypoint then drops to the redis user.
			script := `set -e
[ -s /data/users.acl ] || echo "user default on #$PIWAY_ADMIN_HASH ~* &* +@all" > /data/users.acl
chown redis:redis /data /data/users.acl
exec docker-entrypoint.sh redis-server --aclfile /data/users.acl --maxmemory 48mb --maxmemory-policy noeviction ` + persist
			return []string{"sh", "-c", script}
		},
		ready: []string{"redis-cli", "ping"},
		provision: func(app, pw string) ([]string, string) {
			// Keys and channels under "<app>:" only, and nothing that
			// reaches other apps' data or the server (-@dangerous covers
			// FLUSHALL, KEYS, CONFIG, ...).
			cmds := fmt.Sprintf("ACL SETUSER %[1]s reset on >%[2]s ~%[1]s:* &%[1]s:* +@all -@dangerous\nACL SAVE\n", app, pw)
			return []string{"redis-cli"}, cmds
		},
		urls: func(app, pw string) []Var {
			return []Var{{"REDIS_URL", fmt.Sprintf("redis://%s:%s@piway-redis:6379/0", app, pw)}, {"REDIS_PREFIX", app + ":"}}
		},
	},
}

// containerName is the engine's container, also its host name on Network.
func containerName(e string) string { return "piway-" + e }

// volumeName is where an engine running image keeps its data. The major
// version is part of it: data directories are not compatible across
// majors, so an upgrade fills a new volume and leaves the old one.
func volumeName(e, image string) string { return "piway-db-" + e + "-" + major(image) }

// major is the version in an image's tag: postgres:17-alpine is 17,
// mariadb:11.4 is 11.4.
func major(image string) string {
	_, tag, ok := strings.Cut(image, ":")
	if !ok {
		return "latest"
	}
	tag, _, _ = strings.Cut(tag, "-")
	return tag
}

// Env returns the variables app gets for engines, from stored passwords.
// With both postgres and mariadb, DATABASE_URL is PostgreSQL's.
func env(app string, list []string, password func(engine string) string) []Var {
	var out []Var
	seen := map[string]bool{}
	for _, name := range orderPostgresFirst(list) {
		e := engines[name]
		for _, v := range e.urls(app, password(name)) {
			if !seen[v.Name] {
				seen[v.Name] = true
				out = append(out, v)
			}
		}
	}
	return out
}

func orderPostgresFirst(list []string) []string {
	out := make([]string, 0, len(list))
	for _, e := range list {
		if e == "postgres" {
			out = append(out, e)
		}
	}
	for _, e := range list {
		if e != "postgres" {
			out = append(out, e)
		}
	}
	return out
}

// passwordKey is where an app's password for engine is stored.
func passwordKey(engine string) string { return strings.ToUpper(engine) + "_PASSWORD" }

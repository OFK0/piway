package manifest

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, src string) *Manifest {
	t.Helper()
	m, err := Parse(FileName, []byte(src))
	if err != nil {
		t.Fatalf("Parse:\n%v", err)
	}
	return m
}

func problems(t *testing.T, src string) string {
	t.Helper()
	_, err := Parse(FileName, []byte(src))
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("Parse(%q) error = %v, want *Error", src, err)
	}
	return e.Error()
}

var codeBlock = regexp.MustCompile("(?s)```(yaml|text)\n(.*?)```")

// TestSpecExamples keeps docs/MANIFEST.md honest: every YAML example in it
// must be valid, and its error example must be exactly what Parse reports.
func TestSpecExamples(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "MANIFEST.md"))
	if err != nil {
		t.Fatal(err)
	}
	var yamls int
	for _, b := range codeBlock.FindAllStringSubmatch(string(doc), -1) {
		switch b[1] {
		case "yaml":
			yamls++
			mustParse(t, b[2])
		case "text":
			if got := problems(t, specErrorExample); got+"\n" != b[2] {
				t.Errorf("the error example in MANIFEST.md is out of date; Parse now reports:\n%s", got)
			}
		}
	}
	if yamls != 3 {
		t.Errorf("found %d YAML examples in MANIFEST.md, want 3", yamls)
	}
}

// specErrorExample produces the error listing shown in MANIFEST.md.
const specErrorExample = `piway: 1
name: Guest_Book
build:
run:
  env: {}
  port: 70000
  memory: 256MiB
  cpus: 1
  memmory: 512MiB
`

func TestContainerApp(t *testing.T) {
	m := mustParse(t, `
piway: 1
name: guestbook
build:
  context: app
  dockerfile: app/Dockerfile.prod
  args:
    NODE_VERSION: 22
run:
  port: 3000
  command: ["node", "server.js"]
  env:
    NODE_ENV: production
    LOG_LEVEL: info
  secrets: [DATABASE_URL, SESSION_KEY]
  memory: 1GiB
  cpus: 0.5
  health:
    path: /healthz
    interval: 5s
    timeout: 1s
    start_period: 1m
  volumes:
    - data:/data
    - uploads:/app/public/uploads
  databases: [redis]
`)
	want := &Manifest{
		Version: 1,
		Name:    "guestbook",
		Build:   &Build{Context: "app", Dockerfile: "app/Dockerfile.prod", Args: []Var{{"NODE_VERSION", "22"}}},
		Run: &Run{
			Port:        3000,
			Command:     []string{"node", "server.js"},
			Env:         []Var{{"NODE_ENV", "production"}, {"LOG_LEVEL", "info"}}, // file order
			Secrets:     []string{"DATABASE_URL", "SESSION_KEY"},
			MemoryBytes: 1 << 30,
			CPUs:        0.5,
			Health:      Health{Path: "/healthz", Interval: 5 * time.Second, Timeout: time.Second, StartPeriod: time.Minute},
			Volumes:     []Volume{{"data", "/data"}, {"uploads", "/app/public/uploads"}},
			Databases:   []string{"redis"},
		},
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("got  %+v\nwant %+v", m, want)
	}
}

func TestDefaults(t *testing.T) {
	m := mustParse(t, "piway: 1\nname: hello\nbuild:\nrun:\n  port: 8080\n")
	if !reflect.DeepEqual(m.Build, &Build{Context: ".", Dockerfile: "Dockerfile"}) {
		t.Errorf("Build = %+v", m.Build)
	}
	r := m.Run
	if r.MemoryBytes != 256<<20 || r.CPUs != 1 || r.Health != defaultHealth || r.Command != nil {
		t.Errorf("Run = %+v, want the documented defaults", r)
	}

	s := mustParse(t, "piway: 1\nname: blog\nstatic:\n  dir: ./public/\n").Static
	if s.Dir != "public" || s.SPA || s.Build != nil {
		t.Errorf("Static = %+v", s)
	}
}

func TestInvalid(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		want      []string // every line Parse must report, in order
	}{
		{"empty", "", []string{"piway.yaml: the manifest is empty"}},
		{"comments only", "# nothing\n", []string{"piway.yaml: the manifest is empty"}},
		{"not a mapping", "- a\n- b\n", []string{"piway.yaml:1: must be a mapping of fields"}},
		{"bad yaml", "piway: 1\nname: a\n  port: 80\n", []string{"piway.yaml:3: not valid YAML: mapping values are not allowed in this context"}},
		{"two documents", "piway: 1\n---\nname: x\n", []string{"piway.yaml:2: only one YAML document is allowed; remove the extra ---"}},
		{"nothing set", "{}\n", []string{
			"piway.yaml:1: piway: is required",
			"piway.yaml:1: name: is required",
			"piway.yaml:1: set either build and run (a container app) or static (a static site)",
		}},
		{"version", "piway: 2\nname: a\nstatic: {dir: d}\n", []string{
			"piway.yaml:1: piway: must be 1, got 2 (this piway understands version 1)",
		}},
		{"version as string", "piway: \"1\"\nname: a\nstatic: {dir: d}\n", []string{
			`piway.yaml:1: piway: must be a whole number, got "1"`,
		}},
		{"names", "piway: 1\nname: -bad\nstatic: {dir: d}\n", []string{
			`piway.yaml:2: name: must be 1-40 lowercase letters, digits and "-", starting with a letter and not ending with "-", got "-bad"`,
		}},
		{"reserved name", "piway: 1\nname: www\nstatic: {dir: d}\n", []string{`piway.yaml:2: name: "www" is reserved`}},
		{"both kinds", "piway: 1\nname: a\nbuild:\nrun: {port: 1}\nstatic: {dir: d}\n", []string{
			"piway.yaml:5: static: cannot be used with build: an app is either a container (build and run) or a static site",
		}},
		{"volumes", "piway: 1\nname: a\nbuild:\nrun:\n  port: 1\n  volumes:\n    - data\n    - Data:/x\n    - db:data\n    - db:/data/../x\n    - etc:/etc/app\n    - root:/\n    - one:/srv\n    - one:/opt\n    - two:/srv/sub\n", []string{
			`piway.yaml:7: run.volumes[0]: must be name:/path, such as "data:/data", got "data"`,
			`piway.yaml:8: run.volumes[1]: volume name "Data" must be 1-32 lowercase letters, digits and "-", not starting or ending with "-"`,
			`piway.yaml:9: run.volumes[2]: "data" must be an absolute, clean path such as /data`,
			`piway.yaml:10: run.volumes[3]: "/data/../x" must be an absolute, clean path such as /data`,
			`piway.yaml:11: run.volumes[4]: cannot mount a volume at /etc/app`,
			`piway.yaml:12: run.volumes[5]: cannot mount a volume at /`,
			`piway.yaml:14: run.volumes[7]: volume "one" is listed twice`,
			`piway.yaml:15: run.volumes[8]: /srv/sub overlaps /srv, where volume "one" is mounted`,
		}},
		{"databases", "piway: 1\nname: a\nbuild:\nrun:\n  port: 1\n  env: {DATABASE_URL: x}\n  secrets: [REDIS_URL]\n  databases: [postgres, mysql, postgres, redis]\n", []string{
			"piway.yaml:6: run.env.DATABASE_URL: is set by piway for the postgres database; remove it",
			"piway.yaml:7: run.secrets: REDIS_URL is set by piway for the redis database; remove it",
			`piway.yaml:8: run.databases[1]: "mysql" is not a database piway runs; use postgres, mariadb, redis`,
			`piway.yaml:8: run.databases[2]: "postgres" is listed twice`,
		}},
		{"build without run", "piway: 1\nname: a\nbuild:\n", []string{
			"piway.yaml:3: run: is required with build: piway needs at least run.port",
		}},
		{"run without build", "piway: 1\nname: a\nrun: {port: 80}\n", []string{
			`piway.yaml:3: build: is required with run; write "build:" to use the defaults`,
		}},
		{"run with static", "piway: 1\nname: a\nstatic: {dir: d}\nrun: {port: 80}\n", []string{
			"piway.yaml:4: run: only applies to container apps; a static site has no run section",
		}},
		{"typo and duplicate", "piway: 1\nname: a\nname: b\nstatik: {dir: d}\n", []string{
			"piway.yaml:1: set either build and run (a container app) or static (a static site)",
			"piway.yaml:3: name: is set twice",
			`piway.yaml:4: statik: unknown field (did you mean "static"?)`,
		}},
		{"unknown, nothing close", "piway: 1\nname: a\nstatic: {dir: d, colour: red}\n", []string{
			"piway.yaml:3: static.colour: unknown field",
		}},
		{"paths", "piway: 1\nname: a\nbuild: {context: /src, dockerfile: ../Dockerfile}\nrun: {port: 1}\n", []string{
			`piway.yaml:3: build.context: must be relative to the repository root, got "/src"`,
			`piway.yaml:3: build.dockerfile: must stay inside the repository, got "../Dockerfile"`,
		}},
		{"run values", `piway: 1
name: a
build:
run:
  port: 0
  command: node server.js
  memory: 16MiB
  cpus: 100
`, []string{
			"piway.yaml:5: run.port: must be between 1 and 65535, got 0",
			`piway.yaml:6: run.command: must be a list, such as ["a", "b"]`,
			"piway.yaml:7: run.memory: must be at least 32MiB, got 16MiB",
			"piway.yaml:8: run.cpus: must be between 0.1 and 64, got 100",
		}},
		{"memory unit", "piway: 1\nname: a\nbuild:\nrun: {port: 1, memory: 512M}\n", []string{
			`piway.yaml:4: run.memory: must be a whole number with MiB or GiB, such as 256MiB or 1GiB, got "512M"`,
		}},
		{"missing port", "piway: 1\nname: a\nbuild:\nrun: {cpus: 1}\n", []string{"piway.yaml:4: run.port: is required"}},
		{"env and secrets", `piway: 1
name: a
build:
run:
  port: 1
  env:
    1BAD: x
    EMPTY:
    TOKEN: in-the-repo
  secrets: [TOKEN, TOKEN, not-valid]
`, []string{
			"piway.yaml:7: run.env.1BAD: is not a valid variable name (letters, digits and _, not starting with a digit)",
			`piway.yaml:8: run.env.EMPTY: must be a single value; write "" for an empty one`,
			"piway.yaml:9: run.env.TOKEN: is also listed in secrets; secret values belong on the Pi, not in the repository",
			`piway.yaml:10: run.secrets[1]: "TOKEN" is listed twice`,
			`piway.yaml:10: run.secrets[2]: "not-valid" is not a valid variable name`,
		}},
		{"health", "piway: 1\nname: a\nbuild:\nrun:\n  port: 1\n  health: {path: healthz, interval: 1s, timeout: soon}\n", []string{
			`piway.yaml:6: run.health.path: must start with "/", got "healthz"`,
			`piway.yaml:6: run.health.timeout: must be a positive duration such as 10s or 1m30s, got "soon"`,
			"piway.yaml:6: run.health.timeout: must be shorter than interval (1s), got 2s",
		}},
		{"static", "piway: 1\nname: a\nstatic:\n  spa: yes\n  build: {image: node:22}\n", []string{
			"piway.yaml:3: static.dir: is required",
			`piway.yaml:4: static.spa: must be true or false, got "yes"`,
			"piway.yaml:5: static.build.command: is required",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := problems(t, tc.src)
			if want := strings.Join(tc.want, "\n"); got != want {
				t.Errorf("got\n%s\nwant\n%s", got, want)
			}
		})
	}
}

// YAML anchors and aliases resolve to the node they point at.
func TestAliases(t *testing.T) {
	m := mustParse(t, "piway: 1\nname: a\nbuild:\n  args: &shared {MODE: prod}\nrun:\n  port: 1\n  env: *shared\n")
	if want := []Var{{"MODE", "prod"}}; !reflect.DeepEqual(m.Run.Env, want) {
		t.Errorf("Env = %v, want %v", m.Run.Env, want)
	}
}

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("piway: 1\nname: a\nstatic: {dir: [x]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.HasPrefix(err.Error(), path+":3: static.dir: must be a string") {
		t.Errorf("Load error = %v, want one naming the file and line", err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load(missing) = %v, want ErrNotExist", err)
	}
}

// FuzzParse checks that no input makes Parse panic, and that it returns
// either a manifest or an *Error with at least one problem.
func FuzzParse(f *testing.F) {
	f.Add([]byte(specErrorExample))
	f.Add([]byte("piway: 1\nname: a\nbuild:\nrun: {port: 1, env: &e {A: b}, secrets: [A]}\n"))
	f.Add([]byte("piway: 1\nname: blog\nstatic: {dir: dist, spa: true, build: {image: x, command: y}}\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := Parse(FileName, data)
		var e *Error
		switch {
		case err == nil && m == nil:
			t.Fatal("no manifest and no error")
		case err != nil && (!errors.As(err, &e) || len(e.Problems) == 0):
			t.Fatalf("error %v is not an *Error with problems", err)
		}
	})
}

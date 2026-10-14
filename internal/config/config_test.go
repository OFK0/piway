package config

import (
	"errors"
	"flag"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type env map[string]string

func (e env) lookup(k string) (string, bool) { v, ok := e[k]; return v, ok }

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func load(t *testing.T, args []string, e env, defaultPath string) *Loaded {
	t.Helper()
	l, err := Load(args, e.lookup, defaultPath, io.Discard)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return l
}

func loadErr(t *testing.T, args []string, e env, defaultPath string) error {
	t.Helper()
	_, err := Load(args, e.lookup, defaultPath, io.Discard)
	if err == nil {
		t.Fatal("Load succeeded, want an error")
	}
	return err
}

func TestDefaults(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	l := load(t, nil, env{}, missing)

	if l.Config != Default() {
		t.Errorf("config = %+v, want defaults %+v", l.Config, Default())
	}
	if l.File != "" {
		t.Errorf("File = %q, want none for a missing default file", l.File)
	}
	for k, src := range l.Sources {
		if src != "default" {
			t.Errorf("source of %s = %q, want default", k, src)
		}
	}
}

func TestPrecedence(t *testing.T) {
	path := writeConfig(t, `
addr: ":7000"
interval: 5s
stream_queue: 4
log_level: warn
`)
	e := env{"PIWAY_INTERVAL": "3s", "PIWAY_STREAM_QUEUE": "16"}
	l := load(t, []string{"-config", path, "-stream-queue", "32"}, e, "")

	want := Config{
		Addr:        ":7000",         // file
		Interval:    3 * time.Second, // env beats file
		StreamQueue: 32,              // flag beats env and file
		LogLevel:    slog.LevelWarn,  // file
	}
	if l.Config != want {
		t.Errorf("config = %+v, want %+v", l.Config, want)
	}
	wantSrc := map[string]string{
		"addr":         "file",
		"interval":     "env PIWAY_INTERVAL",
		"stream_queue": "flag -stream-queue",
		"log_level":    "file",
	}
	for k, w := range wantSrc {
		if l.Sources[k] != w {
			t.Errorf("source of %s = %q, want %q", k, l.Sources[k], w)
		}
	}
}

func TestFlagEqualToDefaultStillOverrides(t *testing.T) {
	// Only flags that were set count, but a set flag wins even when its
	// value equals the default.
	path := writeConfig(t, "interval: 5s\n")
	l := load(t, []string{"-config", path, "-interval", "1s"}, env{}, "")
	if l.Interval != time.Second {
		t.Errorf("interval = %v, want 1s from the flag", l.Interval)
	}
}

func TestLastFlagWins(t *testing.T) {
	l := load(t, []string{"-interval", "1s", "-interval", "2s"}, env{}, "")
	if l.Interval != 2*time.Second {
		t.Errorf("interval = %v, want 2s", l.Interval)
	}
}

func TestEmptyEnvIsUnset(t *testing.T) {
	l := load(t, nil, env{"PIWAY_ADDR": ""}, "")
	if l.Addr != Default().Addr {
		t.Errorf("addr = %q, want default", l.Addr)
	}
}

func TestConfigPathResolution(t *testing.T) {
	fromFlag := writeConfig(t, "addr: \":1\"\n")
	fromEnv := writeConfig(t, "addr: \":2\"\n")
	fromDefault := writeConfig(t, "addr: \":3\"\n")

	tests := []struct {
		name string
		args []string
		env  env
		want string
	}{
		{"flag", []string{"-config", fromFlag}, env{"PIWAY_CONFIG": fromEnv}, ":1"},
		{"env", nil, env{"PIWAY_CONFIG": fromEnv}, ":2"},
		{"default", nil, env{}, ":3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := load(t, tt.args, tt.env, fromDefault)
			if l.Addr != tt.want {
				t.Errorf("addr = %q, want %q", l.Addr, tt.want)
			}
		})
	}
}

func TestExplicitMissingFileIsAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	for name, tc := range map[string]struct {
		args []string
		env  env
	}{
		"flag": {[]string{"-config", missing}, env{}},
		"env":  {nil, env{"PIWAY_CONFIG": missing}},
	} {
		t.Run(name, func(t *testing.T) {
			err := loadErr(t, tc.args, tc.env, "")
			if !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("err = %v, want not-exist", err)
			}
		})
	}
}

func TestEmptyFile(t *testing.T) {
	path := writeConfig(t, "# all defaults\n")
	l := load(t, []string{"-config", path}, env{}, "")
	if l.Config != Default() {
		t.Errorf("config = %+v, want defaults", l.Config)
	}
	if l.File != path {
		t.Errorf("File = %q, want %q", l.File, path)
	}
}

func TestInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		args    []string
		env     env
		wantErr string
	}{
		{"unknown key", "adress: \":80\"\n", nil, nil, `unknown setting "adress"`},
		{"duplicate key", "addr: \":1\"\naddr: \":2\"\n", nil, nil, "set twice"},
		{"nested value", "addr:\n  host: x\n", nil, nil, "must be a single value"},
		{"not a mapping", "- addr\n", nil, nil, "top level must be a mapping"},
		{"bad yaml", "addr: [\n", nil, nil, "yaml"},
		{"bad duration in file", "interval: soon\n", nil, nil, "interval (from file)"},
		{"interval too small", "", []string{"-interval", "1ms"}, nil, "below the minimum"},
		{"bad int in env", "", nil, env{"PIWAY_STREAM_QUEUE": "many"}, "stream_queue (from env PIWAY_STREAM_QUEUE)"},
		{"queue zero", "", []string{"-stream-queue", "0"}, nil, "at least 1"},
		{"bad log level", "log_level: loud\n", nil, nil, "log_level"},
		{"empty addr flag", "", []string{"-addr", ""}, nil, "must not be empty"},
		{"positional arg", "", []string{"extra"}, nil, `unexpected argument "extra"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := tt.args
			if tt.file != "" {
				args = append([]string{"-config", writeConfig(t, tt.file)}, args...)
			}
			err := loadErr(t, args, tt.env, "")
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestHelp(t *testing.T) {
	var out strings.Builder
	_, err := Load([]string{"-h"}, env{}.lookup, "", &out)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("err = %v, want flag.ErrHelp", err)
	}
	for _, want := range []string{"-interval", `default "1s"`, "PIWAY_INTERVAL", "-print-config"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestPrintRoundTrips(t *testing.T) {
	l := load(t, []string{"-addr", "[::1]:9090", "-log-level", "debug"}, env{"PIWAY_INTERVAL": "250ms"}, "")
	var out strings.Builder
	l.Print(&out)

	for _, want := range []string{`addr: "[::1]:9090"  # flag -addr`, "stream_queue: 8  # default", "interval: 250ms  # env PIWAY_INTERVAL", "log_level: debug", "# file: none"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	// The printed config is itself a valid config file with the same values.
	again := load(t, []string{"-config", writeConfig(t, out.String())}, env{}, "")
	if again.Config != l.Config {
		t.Errorf("round trip = %+v, want %+v", again.Config, l.Config)
	}
}

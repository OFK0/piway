// Package config builds piway's effective configuration from four layers,
// each overriding the one before:
//
//	defaults < config file < PIWAY_* environment variables < command-line flags
//
// Every layer goes through the same per-field parser, so "2s" means the same
// thing in YAML, in PIWAY_INTERVAL and in -interval.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Config is the effective configuration.
type Config struct {
	Addr        string
	Interval    time.Duration
	StreamQueue int
	LogLevel    slog.Level
	// DataDir holds the metrics history; empty disables recording.
	DataDir       string
	FlushInterval time.Duration
	// How long each history tier is kept (docs/STORAGE.md).
	RetentionRaw, Retention1m, Retention1h time.Duration
	// WebhookSecret verifies GitHub deliveries; empty disables webhooks.
	WebhookSecret string
	// AppsFile lists the repositories and branches that deploy (docs/DEPLOY.md).
	AppsFile string
	// ProxyDomain publishes each app at <name>.<ProxyDomain> through Caddy
	// (docs/PROXY.md); empty disables it.
	ProxyDomain string
	ProxyListen string // where Caddy serves apps
	ProxyTLS    bool
	CaddyAdmin  string // Caddy's admin API
	// PasswordFile holds the dashboard password's hash (internal/auth).
	PasswordFile string
}

// MinInterval keeps a typo like "1ms" from pinning a core: every round reads
// several files and may spawn vcgencmd.
const MinInterval = 100 * time.Millisecond

// Default returns the configuration used when nothing is set.
func Default() Config {
	return Config{
		Addr:          ":8080",
		Interval:      time.Second,
		StreamQueue:   8,
		LogLevel:      slog.LevelInfo,
		DataDir:       DefaultDataDir(),
		FlushInterval: time.Minute,
		RetentionRaw:  24 * time.Hour,
		Retention1m:   7 * day,
		Retention1h:   365 * day,
		AppsFile:      DefaultAppsFile(),
		ProxyListen:   ":8443",
		ProxyTLS:      true,
		CaddyAdmin:    "localhost:2019",
		PasswordFile:  DefaultPasswordFile(),
	}
}

// DefaultPasswordFile is password next to the default config file.
func DefaultPasswordFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "piway", "password")
}

// DefaultAppsFile is apps.yaml next to the default config file.
func DefaultAppsFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "piway", "apps.yaml")
}

// minWebhookSecret matches webhook.MinSecretLen.
const minWebhookSecret = 16

const day = 24 * time.Hour

// parseRetention is time.ParseDuration plus a "d" (day) unit, which
// retention periods need: "7d" rather than "168h".
func parseRetention(s string) (time.Duration, error) {
	var d time.Duration
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		d = time.Duration(n) * day
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, err
		}
	}
	if d < time.Hour {
		return 0, fmt.Errorf("must be at least 1h, got %v", d)
	}
	return d, nil
}

// formatRetention prints whole days as "7d" so -print-config reads back.
func formatRetention(d time.Duration) string {
	if d%day == 0 {
		return strconv.Itoa(int(d/day)) + "d"
	}
	return d.String()
}

// MaxFlushInterval bounds how much history a crash of the process can lose.
const MaxFlushInterval = 10 * time.Minute

// DefaultDataDir is $XDG_STATE_HOME/piway, or ~/.local/state/piway: the
// XDG location for data an application keeps between runs but that is not
// worth backing up like a document.
func DefaultDataDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(d) {
		return filepath.Join(d, "piway")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state", "piway")
}

// field is one setting. The first `word` in backquotes in usage names the
// flag's argument in -h output. Its YAML key is key, its environment variable is
// PIWAY_ + KEY, and its flag is key with '_' replaced by '-'.
type field struct {
	key   string
	usage string
	set   func(c *Config, s string) error
	get   func(c *Config) string
	// quote makes Print emit a double-quoted YAML string. Addresses need it:
	// "[::1]:8080" would otherwise parse as a YAML list.
	quote bool
	// secret keeps the value out of -print-config output.
	secret bool
}

var fields = []field{
	{
		key:   "addr",
		usage: "HTTP listen `address`",
		set: func(c *Config, s string) error {
			if s == "" {
				return errors.New("must not be empty")
			}
			c.Addr = s
			return nil
		},
		get:   func(c *Config) string { return c.Addr },
		quote: true,
	},
	{
		key:   "interval",
		usage: "metric collection interval as a `duration`, e.g. 500ms or 2s",
		set: func(c *Config, s string) error {
			d, err := time.ParseDuration(s)
			if err != nil {
				return err
			}
			if d < MinInterval {
				return fmt.Errorf("%v is below the minimum of %v", d, MinInterval)
			}
			c.Interval = d
			return nil
		},
		get: func(c *Config) string { return c.Interval.String() },
	},
	{
		key:   "stream_queue",
		usage: "`number` of snapshots a WebSocket client may fall behind before it is disconnected",
		set: func(c *Config, s string) error {
			n, err := strconv.Atoi(s)
			if err != nil {
				return err
			}
			if n < 1 {
				return fmt.Errorf("must be at least 1, got %d", n)
			}
			c.StreamQueue = n
			return nil
		},
		get: func(c *Config) string { return strconv.Itoa(c.StreamQueue) },
	},
	{
		key:   "log_level",
		usage: "log `level`: debug, info, warn or error",
		set: func(c *Config, s string) error {
			return c.LogLevel.UnmarshalText([]byte(s))
		},
		get: func(c *Config) string { return strings.ToLower(c.LogLevel.String()) },
	},
	{
		key:   "data_dir",
		usage: "`directory` for the metrics history; empty disables recording",
		set: func(c *Config, s string) error {
			// Expand a leading ~/ as a shell would, so the path in
			// config.example.yaml works when uncommented.
			if rest, ok := strings.CutPrefix(s, "~/"); ok {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				s = filepath.Join(home, rest)
			}
			c.DataDir = s
			return nil
		},
		get:   func(c *Config) string { return c.DataDir },
		quote: true,
	},
	{
		key:   "flush_interval",
		usage: "how long recorded samples may wait in memory before they are written, as a `duration`; 0 writes every sample",
		set: func(c *Config, s string) error {
			d, err := time.ParseDuration(s)
			if err != nil {
				return err
			}
			if d < 0 || d > MaxFlushInterval {
				return fmt.Errorf("must be between 0 and %v, got %v", MaxFlushInterval, d)
			}
			c.FlushInterval = d
			return nil
		},
		get: func(c *Config) string { return c.FlushInterval.String() },
	},
	retentionField("retention_raw", "every sample", func(c *Config) *time.Duration { return &c.RetentionRaw }),
	retentionField("retention_1m", "per-minute rollups", func(c *Config) *time.Duration { return &c.Retention1m }),
	retentionField("retention_1h", "per-hour rollups", func(c *Config) *time.Duration { return &c.Retention1h }),
	{
		key:   "webhook_secret",
		usage: "shared `secret` for GitHub webhook signatures; empty disables webhooks",
		set: func(c *Config, s string) error {
			if s != "" && len(s) < minWebhookSecret {
				return fmt.Errorf("must be at least %d characters (try: openssl rand -hex 32)", minWebhookSecret)
			}
			c.WebhookSecret = s
			return nil
		},
		get:    func(c *Config) string { return c.WebhookSecret },
		secret: true,
	},
	{
		key:   "apps_file",
		usage: "YAML `file` listing the repositories and branches that deploy",
		set: func(c *Config, s string) error {
			if rest, ok := strings.CutPrefix(s, "~/"); ok {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				s = filepath.Join(home, rest)
			}
			c.AppsFile = s
			return nil
		},
		get:   func(c *Config) string { return c.AppsFile },
		quote: true,
	},
	{
		key:   "proxy_domain",
		usage: "serve each app at <name>.`domain` through Caddy, such as local; empty disables it",
		set: func(c *Config, s string) error {
			s = strings.ToLower(strings.TrimSuffix(s, "."))
			if s != "" && !domainPattern.MatchString(s) {
				return fmt.Errorf("%q is not a domain name such as pi.local", s)
			}
			c.ProxyDomain = s
			return nil
		},
		get: func(c *Config) string { return c.ProxyDomain },
	},
	{
		key:   "proxy_listen",
		usage: "`address` Caddy serves apps on; ports below 1024 need privileges",
		set: func(c *Config, s string) error {
			if _, port, err := net.SplitHostPort(s); err != nil || port == "" {
				return fmt.Errorf("%q is not a host:port address such as :8443", s)
			}
			c.ProxyListen = s
			return nil
		},
		get:   func(c *Config) string { return c.ProxyListen },
		quote: true,
	},
	{
		key:   "proxy_tls",
		usage: "serve apps over HTTPS (`bool`); certificates come from Caddy",
		set: func(c *Config, s string) error {
			b, err := strconv.ParseBool(s)
			if err != nil {
				return fmt.Errorf("must be true or false, got %q", s)
			}
			c.ProxyTLS = b
			return nil
		},
		get: func(c *Config) string { return strconv.FormatBool(c.ProxyTLS) },
	},
	{
		key:   "caddy_admin",
		usage: "`address` of Caddy's admin API",
		set: func(c *Config, s string) error {
			if s == "" {
				return errors.New("must not be empty")
			}
			c.CaddyAdmin = s
			return nil
		},
		get:   func(c *Config) string { return c.CaddyAdmin },
		quote: true,
	},
	passwordFileField,
}

var passwordFileField = field{
	key:   "password_file",
	usage: "`file` with the dashboard password's hash, written by -set-password",
	set: func(c *Config, s string) error {
		if s == "" {
			return errors.New("must not be empty: the dashboard needs a password to be reachable from other machines")
		}
		if rest, ok := strings.CutPrefix(s, "~/"); ok {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			s = filepath.Join(home, rest)
		}
		c.PasswordFile = s
		return nil
	},
	get:   func(c *Config) string { return c.PasswordFile },
	quote: true,
}

// domainPattern is one or more DNS labels: "local" serves <name>.local.
var domainPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func retentionField(key, what string, ptr func(*Config) *time.Duration) field {
	return field{
		key:   key,
		usage: "how long to keep " + what + ", as a `duration` such as 24h or 7d",
		set: func(c *Config, s string) error {
			d, err := parseRetention(s)
			if err != nil {
				return err
			}
			*ptr(c) = d
			return nil
		},
		get: func(c *Config) string { return formatRetention(*ptr(c)) },
	}
}

func lookupField(key string) (field, bool) {
	for _, f := range fields {
		if f.key == key {
			return f, true
		}
	}
	return field{}, false
}

func envName(key string) string  { return "PIWAY_" + strings.ToUpper(key) }
func flagName(key string) string { return strings.ReplaceAll(key, "_", "-") }

// Loaded is the result of Load.
type Loaded struct {
	Config
	// File is the config file that was read, or "" if none was.
	File string
	// Sources maps each key to where its value came from: "default",
	// "file", "env PIWAY_X" or "flag -x".
	Sources map[string]string

	ShowVersion bool
	PrintConfig bool
	SetPassword bool
}

// Load parses args (without the program name) and builds the configuration.
// lookupEnv is os.LookupEnv outside tests. It returns flag.ErrHelp for -h.
//
// The config file is -config, else $PIWAY_CONFIG, else defaultPath. A file
// named by -config or $PIWAY_CONFIG must exist; defaultPath may be missing.
func Load(args []string, lookupEnv func(string) (string, bool), defaultPath string, usageOut io.Writer) (*Loaded, error) {
	l := &Loaded{Config: Default(), Sources: make(map[string]string)}
	for _, f := range fields {
		l.Sources[f.key] = "default"
	}

	// Flags are parsed first (to find -config) but applied last. Recording
	// them in order keeps "-interval 1s -interval 2s" meaning 2s.
	type flagValue struct{ key, value string }
	var flagValues []flagValue

	fset := flag.NewFlagSet("piway", flag.ContinueOnError)
	fset.SetOutput(usageOut)
	configPath := fset.String("config", "", "config `file` (default $PIWAY_CONFIG, else "+defaultPath+")")
	fset.BoolVar(&l.ShowVersion, "version", false, "print version and exit")
	fset.BoolVar(&l.PrintConfig, "print-config", false, "print the effective configuration and where each value came from, then exit")
	fset.BoolVar(&l.SetPassword, "set-password", false, "ask for a new dashboard password, store its hash in password_file, then exit")
	def := Default()
	for _, f := range fields {
		usage := fmt.Sprintf("%s (default %q, env %s)", f.usage, f.get(&def), envName(f.key))
		fset.Func(flagName(f.key), usage, func(s string) error {
			flagValues = append(flagValues, flagValue{f.key, s})
			return nil
		})
	}
	if err := fset.Parse(args); err != nil {
		return nil, err
	}
	if fset.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fset.Arg(0))
	}

	// Layer 2: file.
	path, explicit := *configPath, *configPath != ""
	if !explicit {
		path, explicit = lookupEnv("PIWAY_CONFIG")
	}
	if !explicit {
		path = defaultPath
	}
	if path != "" {
		values, err := readFile(path)
		switch {
		case err == nil:
			l.File = path
			for _, kv := range values {
				if err := l.apply(kv.key, kv.value, "file"); err != nil {
					return nil, fmt.Errorf("%s: %w", path, err)
				}
			}
		case errors.Is(err, fs.ErrNotExist) && !explicit:
			// The default location is optional.
		default:
			return nil, err
		}
	}

	// Layer 3: environment. An empty variable counts as unset, so a blank
	// Environment= line in a systemd unit does not break startup.
	for _, f := range fields {
		name := envName(f.key)
		if v, ok := lookupEnv(name); ok && v != "" {
			if err := l.apply(f.key, v, "env "+name); err != nil {
				return nil, err
			}
		}
	}

	// Layer 4: flags that were given explicitly.
	for _, fv := range flagValues {
		if err := l.apply(fv.key, fv.value, "flag -"+flagName(fv.key)); err != nil {
			return nil, err
		}
	}
	return l, nil
}

func (l *Loaded) apply(key, value, source string) error {
	f, ok := lookupField(key)
	if !ok {
		return fmt.Errorf("unknown setting %q", key)
	}
	if err := f.set(&l.Config, value); err != nil {
		return fmt.Errorf("%s (from %s): %w", key, source, err)
	}
	l.Sources[key] = source
	return nil
}

type keyValue struct{ key, value string }

// readFile returns the top-level scalar settings of a YAML file in order.
func readFile(path string) ([]keyValue, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(doc.Content) == 0 {
		return nil, nil // empty file or only comments
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: top level must be a mapping of settings", path)
	}
	var out []keyValue
	seen := make(map[string]bool)
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		if seen[k.Value] {
			return nil, fmt.Errorf("%s:%d: %s is set twice", path, k.Line, k.Value)
		}
		seen[k.Value] = true
		if v.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("%s:%d: %s must be a single value", path, v.Line, k.Value)
		}
		if _, ok := lookupField(k.Value); !ok {
			return nil, fmt.Errorf("%s:%d: unknown setting %q", path, k.Line, k.Value)
		}
		out = append(out, keyValue{k.Value, v.Value})
	}
	return out, nil
}

// DefaultPath is ~/.config/piway/config.yaml (or $XDG_CONFIG_HOME/...), or
// "" if the user config directory is unknown.
func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "piway", "config.yaml")
}

// Print writes the effective configuration as YAML that piway can read
// back, annotated with where each value came from.
func (l *Loaded) Print(w io.Writer) {
	if l.File != "" {
		fmt.Fprintf(w, "# file: %s\n", l.File)
	} else {
		fmt.Fprintln(w, "# file: none")
	}
	for _, f := range fields {
		v := f.get(&l.Config)
		if f.secret {
			// Never print a secret. A comment, so the output still reads
			// back as a config without inventing a value.
			state := "not set"
			if v != "" {
				state = "set, hidden"
			}
			fmt.Fprintf(w, "# %s: (%s)  # %s\n", f.key, state, l.Sources[f.key])
			continue
		}
		if f.quote {
			v = strconv.Quote(v) // Go and YAML double-quoted escapes agree for printable text
		}
		fmt.Fprintf(w, "%s: %s  # %s\n", f.key, v, l.Sources[f.key])
	}
}

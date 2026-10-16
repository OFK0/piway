// Package manifest reads and validates piway.yaml, the file in an app's
// repository that says how piway builds and runs it. docs/MANIFEST.md is the
// specification; this package implements it.
package manifest

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// FileName is where piway looks for the manifest in a repository.
const FileName = "piway.yaml"

// Version is the manifest version this package understands.
const Version = 1

// Manifest is a valid piway.yaml with defaults applied. Exactly one of Build
// and Static is set; Run is set exactly when Build is.
type Manifest struct {
	Version int
	Name    string
	Build   *Build
	Run     *Run
	Static  *Static
}

// Build describes how to build a container app's image.
type Build struct {
	Context    string // directory sent to the build, relative to the repository
	Dockerfile string // relative to the repository
	Args       []Var
}

// Run describes how to run a container app.
type Run struct {
	Port        int
	Command     []string // nil keeps the image's CMD
	Env         []Var
	Secrets     []string // names only; values live on the Pi
	MemoryBytes int64
	CPUs        float64
	Health      Health
	Volumes     []Volume // in file order
}

// Volume is a named, per-app volume mounted at Path. It survives deploys,
// restarts and rollbacks.
type Volume struct {
	Name string // a-z, 0-9 and -
	Path string // absolute path inside the container
}

// Health is the HTTP check that decides whether a new version may take
// traffic.
type Health struct {
	Path        string
	Interval    time.Duration
	Timeout     time.Duration
	StartPeriod time.Duration
}

// Static describes a static site, served by piway from a directory.
type Static struct {
	Dir   string
	SPA   bool
	Build *StaticBuild // nil if Dir is committed as is
}

// StaticBuild produces a static site's directory.
type StaticBuild struct {
	Image   string
	Command string // run with sh -c in the repository root
}

// Var is one environment variable or build argument, in file order.
type Var struct {
	Name, Value string
}

// Defaults (docs/MANIFEST.md › Fields).
const (
	defaultMemory = 256 << 20
	minMemory     = 32 << 20
)

var defaultHealth = Health{Path: "/", Interval: 10 * time.Second, Timeout: 2 * time.Second, StartPeriod: 30 * time.Second}

// Problem is one thing wrong with a manifest.
type Problem struct {
	Line  int    // 0 if the problem has no position, such as an empty file
	Field string // dotted path such as "run.port"; empty for the whole file
	Msg   string
}

// Error lists every problem found in one file, in line order.
type Error struct {
	File     string
	Problems []Problem
}

func (e *Error) Error() string {
	var b strings.Builder
	for i, p := range e.Problems {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(e.File)
		if p.Line > 0 {
			fmt.Fprintf(&b, ":%d", p.Line)
		}
		b.WriteString(": ")
		if p.Field != "" {
			b.WriteString(p.Field)
			b.WriteString(": ")
		}
		b.WriteString(p.Msg)
	}
	return b.String()
}

// Load reads and validates the manifest at path.
func Load(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(path, b)
}

// Parse validates data. file names it in error messages. The error, if any,
// is an *Error listing every problem.
func Parse(file string, data []byte) (*Manifest, error) {
	doc, problem := document(data)
	if problem != nil {
		return nil, &Error{File: file, Problems: []Problem{*problem}}
	}
	var d decoder
	m := d.manifest(doc)
	if len(d.problems) > 0 {
		slices.SortStableFunc(d.problems, func(a, b Problem) int { return a.Line - b.Line })
		return nil, &Error{File: file, Problems: d.problems}
	}
	return m, nil
}

var yamlLine = regexp.MustCompile(`^yaml: line (\d+): `)

// document parses data as exactly one YAML document.
func document(data []byte) (*yaml.Node, *Problem) {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, &Problem{Msg: "the manifest is empty"}
		}
		// "yaml: line 3: mapping values are not allowed in this context"
		msg := err.Error()
		line := 0
		if m := yamlLine.FindStringSubmatch(msg); m != nil {
			line, _ = strconv.Atoi(m[1])
			msg = msg[len(m[0]):]
		}
		return nil, &Problem{Line: line, Msg: "not valid YAML: " + strings.TrimPrefix(msg, "yaml: ")}
	}
	var next yaml.Node
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		return nil, &Problem{Line: next.Line, Msg: "only one YAML document is allowed; remove the extra ---"}
	}
	if len(doc.Content) == 0 {
		return nil, &Problem{Msg: "the manifest is empty"}
	}
	return doc.Content[0], nil
}

func (d *decoder) manifest(root *yaml.Node) *Manifest {
	m := &Manifest{}
	var buildKey, runKey, staticKey *yaml.Node
	set := d.mapping(root, "", fields{
		"piway": func(v *yaml.Node, p string) {
			if n, ok := d.integer(v, p); ok {
				if n != Version {
					d.add(v, p, "must be %d, got %d (this piway understands version %d)", Version, n, Version)
				}
				m.Version = n
			}
		},
		"name": func(v *yaml.Node, p string) {
			if s, ok := d.str(v, p); ok {
				d.checkName(v, p, s)
				m.Name = s
			}
		},
		"build":  func(v *yaml.Node, p string) { m.Build = d.build(v, p) },
		"run":    func(v *yaml.Node, p string) { m.Run = d.run(v, p) },
		"static": func(v *yaml.Node, p string) { m.Static = d.static(v, p) },
	})
	if root.Kind != yaml.MappingNode {
		return m // mapping() reported it
	}
	d.require(root, "", set, "piway", "name")
	buildKey, runKey, staticKey = set["build"], set["run"], set["static"]

	// An app is either a container (build + run) or a static site.
	switch {
	case staticKey != nil && buildKey != nil:
		d.add(staticKey, "static", "cannot be used with build: an app is either a container (build and run) or a static site")
	case staticKey != nil && runKey != nil:
		d.add(runKey, "run", "only applies to container apps; a static site has no run section")
	case buildKey != nil && runKey == nil:
		d.add(buildKey, "run", "is required with build: piway needs at least run.port")
	case runKey != nil && buildKey == nil:
		d.add(runKey, "build", `is required with run; write "build:" to use the defaults`)
	case buildKey == nil && staticKey == nil:
		d.add(root, "", "set either build and run (a container app) or static (a static site)")
	}
	return m
}

var namePattern = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)

var reservedNames = []string{"piway", "www"}

func (d *decoder) checkName(v *yaml.Node, p, s string) {
	switch {
	case !namePattern.MatchString(s):
		d.add(v, p, `must be 1-40 lowercase letters, digits and "-", starting with a letter and not ending with "-", got %q`, s)
	case slices.Contains(reservedNames, s):
		d.add(v, p, "%q is reserved", s)
	}
}

func (d *decoder) build(n *yaml.Node, p string) *Build {
	b := &Build{Context: ".", Dockerfile: "Dockerfile"}
	d.mapping(n, p, fields{
		"context":    func(v *yaml.Node, p string) { d.relPath(v, p, &b.Context) },
		"dockerfile": func(v *yaml.Node, p string) { d.relPath(v, p, &b.Dockerfile) },
		"args":       func(v *yaml.Node, p string) { b.Args = d.vars(v, p) },
	})
	return b
}

func (d *decoder) run(n *yaml.Node, p string) *Run {
	r := &Run{MemoryBytes: defaultMemory, CPUs: 1, Health: defaultHealth}
	var envNames map[string]*yaml.Node
	var secrets []*yaml.Node
	set := d.mapping(n, p, fields{
		"port": func(v *yaml.Node, p string) {
			if port, ok := d.integer(v, p); ok {
				if port < 1 || port > 65535 {
					d.add(v, p, "must be between 1 and 65535, got %d", port)
				}
				r.Port = port
			}
		},
		"command": func(v *yaml.Node, p string) {
			if cmd, ok := d.strings(v, p); ok {
				if len(cmd) == 0 {
					d.add(v, p, "must not be empty; leave it out to keep the image's CMD")
				}
				r.Command = cmd
			}
		},
		"env": func(v *yaml.Node, p string) {
			r.Env = d.vars(v, p)
			if v.Kind != yaml.MappingNode {
				return // vars() reported it
			}
			envNames = make(map[string]*yaml.Node)
			for i := 0; i+1 < len(v.Content); i += 2 {
				envNames[v.Content[i].Value] = v.Content[i]
			}
		},
		"secrets": func(v *yaml.Node, p string) {
			names, ok := d.strings(v, p)
			if !ok {
				return
			}
			for i, name := range names {
				item := v.Content[i]
				ip := fmt.Sprintf("%s[%d]", p, i)
				switch {
				case !validVarName(name):
					d.add(item, ip, "%q is not a valid variable name", name)
				case slices.Contains(names[:i], name):
					d.add(item, ip, "%q is listed twice", name)
				default:
					r.Secrets = append(r.Secrets, name)
					secrets = append(secrets, item)
				}
			}
		},
		"memory": func(v *yaml.Node, p string) {
			if s, ok := d.str(v, p); ok {
				if b, err := parseSize(s); err != nil {
					d.add(v, p, "%v", err)
				} else {
					r.MemoryBytes = b
				}
			}
		},
		"cpus": func(v *yaml.Node, p string) {
			if f, ok := d.number(v, p); ok {
				if f < 0.1 || f > 64 {
					d.add(v, p, "must be between 0.1 and 64, got %v", f)
				}
				r.CPUs = f
			}
		},
		"health":  func(v *yaml.Node, p string) { d.health(v, p, &r.Health) },
		"volumes": func(v *yaml.Node, p string) { r.Volumes = d.volumes(v, p) },
	})
	d.require(n, p, set, "port")
	// A secret must not also have a value in the repository.
	for _, s := range secrets {
		if k := envNames[s.Value]; k != nil {
			d.add(k, join(p, "env."+s.Value), "is also listed in secrets; secret values belong on the Pi, not in the repository")
		}
	}
	return r
}

var volumeName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// volumes decodes run.volumes: a list of "name:/path".
func (d *decoder) volumes(n *yaml.Node, p string) []Volume {
	items, ok := d.strings(n, p)
	if !ok {
		return nil
	}
	var out []Volume
	for i, item := range items {
		node, ip := n.Content[i], fmt.Sprintf("%s[%d]", p, i)
		name, mount, found := strings.Cut(item, ":")
		switch {
		case !found:
			d.add(node, ip, `must be name:/path, such as "data:/data", got %q`, item)
			continue
		case !volumeName.MatchString(name):
			d.add(node, ip, "volume name %q must be 1-32 lowercase letters, digits and \"-\", not starting or ending with \"-\"", name)
			continue
		case !strings.HasPrefix(mount, "/") || path.Clean(mount) != mount:
			d.add(node, ip, "%q must be an absolute, clean path such as /data", mount)
			continue
		case mount == "/" || slices.ContainsFunc(systemDirs, func(dir string) bool { return mount == dir || strings.HasPrefix(mount, dir+"/") }):
			d.add(node, ip, "cannot mount a volume at %s", mount)
			continue
		}
		bad := false
		for _, v := range out {
			switch {
			case v.Name == name:
				d.add(node, ip, "volume %q is listed twice", name)
				bad = true
			case v.Path == mount || strings.HasPrefix(mount, v.Path+"/") || strings.HasPrefix(v.Path, mount+"/"):
				d.add(node, ip, "%s overlaps %s, where volume %q is mounted", mount, v.Path, v.Name)
				bad = true
			}
		}
		if !bad {
			out = append(out, Volume{Name: name, Path: mount})
		}
	}
	return out
}

// systemDirs hold the container's own system; a volume there would hide it.
var systemDirs = []string{"/bin", "/dev", "/etc", "/lib", "/proc", "/sbin", "/sys", "/usr"}

func (d *decoder) health(n *yaml.Node, p string, h *Health) {
	set := d.mapping(n, p, fields{
		"path": func(v *yaml.Node, p string) {
			if s, ok := d.str(v, p); ok {
				if !strings.HasPrefix(s, "/") {
					d.add(v, p, `must start with "/", got %q`, s)
				}
				h.Path = s
			}
		},
		"interval":     func(v *yaml.Node, p string) { d.setDuration(v, p, &h.Interval) },
		"timeout":      func(v *yaml.Node, p string) { d.setDuration(v, p, &h.Timeout) },
		"start_period": func(v *yaml.Node, p string) { d.setDuration(v, p, &h.StartPeriod) },
	})
	if h.Timeout >= h.Interval {
		at := set["timeout"]
		if at == nil {
			at = set["interval"]
		}
		d.add(at, join(p, "timeout"), "must be shorter than interval (%v), got %v", h.Interval, h.Timeout)
	}
}

func (d *decoder) static(n *yaml.Node, p string) *Static {
	s := &Static{}
	set := d.mapping(n, p, fields{
		"dir": func(v *yaml.Node, p string) { d.relPath(v, p, &s.Dir) },
		"spa": func(v *yaml.Node, p string) {
			if b, ok := d.boolean(v, p); ok {
				s.SPA = b
			}
		},
		"build": func(v *yaml.Node, p string) {
			b := &StaticBuild{}
			set := d.mapping(v, p, fields{
				"image":   func(v *yaml.Node, p string) { b.Image, _ = d.str(v, p) },
				"command": func(v *yaml.Node, p string) { b.Command, _ = d.str(v, p) },
			})
			d.require(v, p, set, "image", "command")
			s.Build = b
		},
	})
	d.require(n, p, set, "dir")
	return s
}

func (d *decoder) setDuration(v *yaml.Node, p string, dst *time.Duration) {
	if t, ok := d.duration(v, p); ok {
		*dst = t
	}
}

// relPath accepts a path inside the repository.
func (d *decoder) relPath(v *yaml.Node, p string, dst *string) {
	s, ok := d.str(v, p)
	if !ok {
		return
	}
	clean := path.Clean(s)
	switch {
	case s == "":
		d.add(v, p, "must not be empty")
	case strings.HasPrefix(s, "/"):
		d.add(v, p, "must be relative to the repository root, got %q", s)
	case strings.Contains(s, `\`):
		d.add(v, p, `must use "/" between directories, got %q`, s)
	case clean == ".." || strings.HasPrefix(clean, "../"):
		d.add(v, p, "must stay inside the repository, got %q", s)
	default:
		*dst = clean
	}
}

var varNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validVarName(s string) bool { return varNamePattern.MatchString(s) }

// parseSize parses a memory size such as "256MiB" or "1GiB".
func parseSize(s string) (int64, error) {
	units := []struct {
		suffix string
		shift  uint
	}{{"GiB", 30}, {"MiB", 20}}
	for _, u := range units {
		if num, ok := strings.CutSuffix(s, u.suffix); ok {
			n, err := strconv.ParseInt(num, 10, 64)
			if err != nil || n <= 0 || n > 1<<(62-u.shift) {
				break
			}
			b := n << u.shift
			if b < minMemory {
				return 0, fmt.Errorf("must be at least 32MiB, got %s", s)
			}
			return b, nil
		}
	}
	return 0, fmt.Errorf("must be a whole number with MiB or GiB, such as 256MiB or 1GiB, got %q", s)
}

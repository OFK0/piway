package manifest

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// decoder walks a YAML node tree and collects every problem instead of
// stopping at the first, so one run shows the author everything to fix.
type decoder struct {
	problems []Problem
	// keys maps each decoded value node to its key, so a problem with a whole
	// section ("static.dir is required") points at the line naming the
	// section. A block mapping's own line is that of its first child.
	keys map[*yaml.Node]*yaml.Node
}

func (d *decoder) add(n *yaml.Node, field, format string, args ...any) {
	d.problems = append(d.problems, Problem{Line: n.Line, Field: field, Msg: fmt.Sprintf(format, args...)})
}

// fields maps each allowed key of a mapping to the function that decodes
// its value; path is the value's dotted field name.
type fields map[string]func(v *yaml.Node, path string)

// mapping decodes n with fs and returns the key node of every field that was
// set. A null value ("build:" with nothing after it) counts as an empty
// mapping, so a section can be enabled with all its defaults.
func (d *decoder) mapping(n *yaml.Node, path string, fs fields) map[string]*yaml.Node {
	n = resolve(n)
	set := make(map[string]*yaml.Node)
	if isNull(n) {
		return set
	}
	if n.Kind != yaml.MappingNode {
		d.add(n, path, "must be a mapping of fields")
		return set
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		p := join(path, k.Value)
		if _, dup := set[k.Value]; dup {
			d.add(k, p, "is set twice")
			continue
		}
		decode, ok := fs[k.Value]
		if !ok {
			d.add(k, p, "unknown field%s", suggestion(k.Value, fs))
			continue
		}
		set[k.Value] = k
		v = resolve(v)
		if d.keys == nil {
			d.keys = make(map[*yaml.Node]*yaml.Node)
		}
		d.keys[v] = k
		decode(v, p)
	}
	return set
}

// require reports each name missing from set, at the line of the mapping
// that should contain it.
func (d *decoder) require(at *yaml.Node, path string, set map[string]*yaml.Node, names ...string) {
	if k := d.keys[at]; k != nil {
		at = k
	}
	for _, name := range names {
		if set[name] == nil {
			d.add(at, join(path, name), "is required")
		}
	}
}

func (d *decoder) scalar(v *yaml.Node, path, what string) (string, bool) {
	if v.Kind != yaml.ScalarNode || isNull(v) {
		d.add(v, path, "must be %s", what)
		return "", false
	}
	return v.Value, true
}

func (d *decoder) str(v *yaml.Node, path string) (string, bool) {
	return d.scalar(v, path, "a string")
}

func (d *decoder) integer(v *yaml.Node, path string) (int, bool) {
	s, ok := d.scalar(v, path, "a whole number")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || v.Tag != "!!int" {
		d.add(v, path, "must be a whole number, got %q", s)
		return 0, false
	}
	return n, true
}

func (d *decoder) number(v *yaml.Node, path string) (float64, bool) {
	s, ok := d.scalar(v, path, "a number")
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || (v.Tag != "!!int" && v.Tag != "!!float") {
		d.add(v, path, "must be a number, got %q", s)
		return 0, false
	}
	return f, true
}

func (d *decoder) boolean(v *yaml.Node, path string) (bool, bool) {
	var b bool
	if v.Kind != yaml.ScalarNode || v.Tag != "!!bool" || v.Decode(&b) != nil {
		d.add(v, path, "must be true or false, got %q", v.Value)
		return false, false
	}
	return b, true
}

func (d *decoder) duration(v *yaml.Node, path string) (time.Duration, bool) {
	s, ok := d.str(v, path)
	if !ok {
		return 0, false
	}
	t, err := time.ParseDuration(s)
	if err != nil || t <= 0 {
		d.add(v, path, "must be a positive duration such as 10s or 1m30s, got %q", s)
		return 0, false
	}
	return t, true
}

// strings decodes a list of strings. A single string is an error rather than
// a one-element list, so a command is never split in an unexpected place.
func (d *decoder) strings(v *yaml.Node, path string) ([]string, bool) {
	if v.Kind != yaml.SequenceNode {
		d.add(v, path, "must be a list, such as [\"a\", \"b\"]")
		return nil, false
	}
	out := make([]string, 0, len(v.Content))
	ok := true
	for i, item := range v.Content {
		s, good := d.str(resolve(item), fmt.Sprintf("%s[%d]", path, i))
		ok = ok && good
		out = append(out, s)
	}
	return out, ok
}

// vars decodes a mapping of names to scalar values, in file order. Any
// scalar is accepted as a value, so PORT: 3000 means the string "3000".
func (d *decoder) vars(v *yaml.Node, path string) []Var {
	if v.Kind != yaml.MappingNode {
		d.add(v, path, "must be a mapping of NAME: value")
		return nil
	}
	var out []Var
	seen := make(map[string]bool)
	for i := 0; i+1 < len(v.Content); i += 2 {
		k, val := v.Content[i], resolve(v.Content[i+1])
		p := join(path, k.Value)
		switch {
		case seen[k.Value]:
			d.add(k, p, "is set twice")
		case !validVarName(k.Value):
			d.add(k, p, "is not a valid variable name (letters, digits and _, not starting with a digit)")
		case val.Kind != yaml.ScalarNode || isNull(val):
			d.add(val, p, `must be a single value; write "" for an empty one`)
		default:
			out = append(out, Var{Name: k.Value, Value: val.Value})
		}
		seen[k.Value] = true
	}
	return out
}

func resolve(n *yaml.Node) *yaml.Node {
	for n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// suggestion names the closest allowed key, if one is close enough to be a
// likely typo.
func suggestion(key string, fs fields) string {
	names := make([]string, 0, len(fs))
	for name := range fs {
		names = append(names, name)
	}
	slices.Sort(names) // stable choice between equally close names
	best, bestDist := "", 3
	for _, name := range names {
		if dist := levenshtein(strings.ToLower(key), name); dist < bestDist {
			best, bestDist = name, dist
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf(" (did you mean %q?)", best)
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

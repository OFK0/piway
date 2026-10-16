// Package secrets keeps the values of apps' run.secrets on the Pi, one
// file per app: <dir>/<app>.env, one NAME=value per line, # comments.
//
// The files can be edited by hand. Changes made through Set and Remove
// keep every other line, comments included, and are written atomically
// with mode 0600. Values never go to the log.
package secrets

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// ErrInvalid is returned for a bad name or value.
var ErrInvalid = errors.New("invalid secret")

// name is a portable environment variable name.
var name = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// appName matches manifest app names, so a name is a file name.
var appName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidName reports whether s can be a secret's name.
func ValidName(s string) bool { return name.MatchString(s) }

// Store is the secrets directory.
type Store struct {
	Dir string
	mu  sync.Mutex // serialises read-modify-write
}

// Path is the file that holds app's secrets.
func (s *Store) Path(app string) string { return filepath.Join(s.Dir, app+".env") }

// Values returns app's secrets. A missing file is no secrets.
func (s *Store) Values(app string) (map[string]string, error) {
	if !appName.MatchString(app) {
		return nil, fmt.Errorf("%w: app %q", ErrInvalid, app)
	}
	b, err := os.ReadFile(s.Path(app))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	return parse(s.Path(app), b)
}

// parse reads NAME=value lines; blank lines and # comments are skipped,
// and the value is everything after the first '='.
func parse(path string, b []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "" || strings.ContainsAny(k, " \t") {
			return nil, fmt.Errorf("%s:%d: want NAME=value", path, n)
		}
		out[k] = v
	}
	return out, sc.Err()
}

// Set gives app's secret key the value, replacing the line that set it
// before or adding one at the end.
func (s *Store) Set(app, key, value string) error {
	if !name.MatchString(key) {
		return fmt.Errorf("%w: %q is not a variable name (letters, digits and _, not starting with a digit)", ErrInvalid, key)
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%w: a value cannot span lines; the file holds one NAME=value per line", ErrInvalid)
	}
	if strings.TrimSpace(value) != value {
		// The parser trims each line, so the edges would be lost.
		return fmt.Errorf("%w: a value cannot start or end with spaces", ErrInvalid)
	}
	return s.edit(app, func(lines []string) []string {
		out, found := lines[:0], false
		for _, l := range lines {
			if k, ok := lineKey(l); ok && k == key {
				if found {
					continue // a later line for the name would win; drop it
				}
				l, found = key+"="+value, true
			}
			out = append(out, l)
		}
		if !found {
			out = append(out, key+"="+value)
		}
		return out
	})
}

// Remove deletes app's secret key. A missing one is not an error.
func (s *Store) Remove(app, key string) error {
	if !name.MatchString(key) {
		return fmt.Errorf("%w: %q is not a variable name", ErrInvalid, key)
	}
	return s.edit(app, func(lines []string) []string {
		return slices.DeleteFunc(lines, func(l string) bool { k, ok := lineKey(l); return ok && k == key })
	})
}

// lineKey is the name a line sets, if it sets one.
func lineKey(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return "", false
	}
	k, _, ok := strings.Cut(t, "=")
	return k, ok
}

func (s *Store) edit(app string, fn func(lines []string) []string) error {
	if !appName.MatchString(app) {
		return fmt.Errorf("%w: app %q", ErrInvalid, app)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.Path(app)
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := parse(path, b); err != nil {
		return err // do not rewrite a file piway cannot read
	}
	var lines []string
	if len(b) > 0 {
		lines = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	}
	lines = fn(lines)
	out := strings.Join(lines, "\n")
	if out != "" {
		out += "\n"
	}
	return writeFile(path, []byte(out))
}

// Digest summarises the values of names, so a container started with
// them can be told apart from one started with older values. It is
// empty when there are no names.
func Digest(names []string, values map[string]string) string {
	if len(names) == 0 {
		return ""
	}
	sorted := slices.Sorted(slices.Values(names))
	h := sha256.New()
	for _, n := range sorted {
		v, ok := values[n]
		fmt.Fprintf(h, "%s\x00%t\x00%s\x00", n, ok, v)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// writeFile replaces path with data, mode 0600, in a 0700 directory:
// write a sibling file, sync, rename.
func writeFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // a no-op after a successful rename
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

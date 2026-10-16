package webhook

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"
)

// TargetSource supplies the deploy targets. Callers ask on every use, so a
// source that changes is picked up without a restart.
type TargetSource interface {
	Targets() []Target
}

// StaticTargets is a fixed list, for tests and callers with nothing to edit.
type StaticTargets []Target

// Targets returns the list itself.
func (s StaticTargets) Targets() []Target { return s }

// ErrExists, ErrNotFound and ErrInvalid are returned by Store.Add and
// Store.Remove.
var (
	ErrInvalid  = errors.New("invalid target")
	ErrExists   = errors.New("already listed")
	ErrNotFound = errors.New("not listed")
)

// Store is the deploy targets of an apps file, kept in memory and written
// back on change. apps.yaml stays the source of truth: the file is
// re-read whenever its size or modification time changes, so editing it by
// hand keeps working, and a hand edit that does not parse is ignored (with
// a log line) until it is fixed.
type Store struct {
	path string
	log  *slog.Logger

	mu      sync.Mutex
	targets []Target
	size    int64
	mtime   time.Time
	loaded  bool
}

// OpenStore reads path. A missing file is an empty store; a file that does
// not parse is an error.
func OpenStore(path string, log *slog.Logger) (*Store, error) {
	s := &Store{path: path, log: log}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// Targets returns a copy of the current targets.
func (s *Store) Targets() []Target {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reload(); err != nil {
		s.log.Warn("apps file not reloaded, keeping the previous targets", "file", s.path, "err", err)
	}
	return append([]Target(nil), s.targets...)
}

// reload re-reads the file if it changed. Callers hold s.mu.
func (s *Store) reload() error {
	fi, err := os.Stat(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if s.loaded && s.size == 0 && s.mtime.IsZero() {
			return nil
		}
		s.targets, s.size, s.mtime, s.loaded = nil, 0, time.Time{}, true
		return nil
	case err != nil:
		return err
	}
	if s.loaded && fi.Size() == s.size && fi.ModTime().Equal(s.mtime) {
		return nil
	}
	targets, err := LoadTargets(s.path)
	if err != nil {
		return err
	}
	s.targets, s.size, s.mtime, s.loaded = targets, fi.Size(), fi.ModTime(), true
	return nil
}

// Add lists t. It reports ErrExists for a repository and branch that are
// already there (repository names are case-insensitive), and ErrInvalid
// for a bad target or one whose auth differs from the repository's other
// branches.
func (s *Store) Add(t Target) error {
	return s.edit(func(doc *yaml.Node, seq *yaml.Node, current []Target) (bool, error) {
		if err := checkTarget(t, current); err != nil {
			return false, err
		}
		seq.Content = append(seq.Content, targetNode(t))
		return true, nil
	})
}

// Remove unlists repo. With a branch only that branch goes; with an empty
// branch every branch of the repository does. Every path of a branch
// goes, unless path is set. It returns what it removed.
func (s *Store) Remove(repo, branch string, path ...string) ([]Target, error) {
	var removed []Target
	err := s.edit(func(doc *yaml.Node, seq *yaml.Node, current []Target) (bool, error) {
		kept := seq.Content[:0:0]
		for i, n := range seq.Content {
			t := current[i]
			if strings.EqualFold(t.Repo, repo) && (branch == "" || t.Branch == branch) && (len(path) == 0 || t.Path == path[0]) {
				removed = append(removed, t)
				continue
			}
			kept = append(kept, n)
		}
		if len(removed) == 0 {
			return false, fmt.Errorf("%s: %w", repo, ErrNotFound)
		}
		seq.Content = kept
		return true, nil
	})
	return removed, err
}

// edit parses the file as a yaml.Node tree, so comments and unrelated
// keys survive, lets fn change the deploy sequence, and writes the result
// atomically. current lines up with seq.Content.
func (s *Store) edit(fn func(doc, seq *yaml.Node, current []Target) (bool, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := os.ReadFile(s.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: %w", s.path, err)
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: top level is not a mapping", s.path)
	}
	var seq *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "deploy" {
			seq = root.Content[i+1]
		}
	}
	if seq == nil || seq.Kind == yaml.ScalarNode { // absent, or "deploy:" with nothing under it
		seq = &yaml.Node{Kind: yaml.SequenceNode}
		replaced := false
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value == "deploy" {
				root.Content[i+1], replaced = seq, true
			}
		}
		if !replaced {
			root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "deploy"}, seq)
		}
	}
	current := make([]Target, len(seq.Content))
	for i, n := range seq.Content {
		if err := n.Decode(&current[i]); err != nil {
			return fmt.Errorf("%s: deploy[%d]: %w", s.path, i, err)
		}
	}
	changed, err := fn(&doc, seq, current)
	if err != nil || !changed {
		return err
	}

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if err := writeAtomic(s.path, out.Bytes()); err != nil {
		return err
	}
	s.loaded = false // read it back, so size and mtime match what is on disk
	return s.reload()
}

func targetNode(t Target) *yaml.Node {
	str := func(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: v} }
	n := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		str("repo"), str(t.Repo), str("branch"), str(t.Branch),
	}}
	if t.Path != "" {
		n.Content = append(n.Content, str("path"), str(t.Path))
	}
	if t.Provider != "" {
		n.Content = append(n.Content, str("provider"), str(t.Provider))
	}
	if t.Host != "" {
		n.Content = append(n.Content, str("host"), str(t.Host))
	}
	if t.Auth != "" {
		n.Content = append(n.Content, str("auth"), str(t.Auth))
	}
	return n
}

// writeAtomic replaces path with data: write a sibling file, sync, rename.
// A crash leaves the old file or the new one, never half of either.
func writeAtomic(path string, data []byte) error {
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".apps-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // a no-op after a successful rename
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
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

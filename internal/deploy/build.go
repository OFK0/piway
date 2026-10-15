// Package deploy turns deploy requests into running apps: it fetches the
// pushed commit, builds its image and replaces the app's container, one
// build at a time, with every step's output logged and streamable
// (docs/DEPLOY.md).
package deploy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// State is where a build is in its life.
type State string

const (
	Queued    State = "queued"
	Running   State = "running"
	Succeeded State = "succeeded"
	Failed    State = "failed"
	Cancelled State = "cancelled"
)

// Done reports whether the build has finished, one way or another.
func (s State) Done() bool { return s == Succeeded || s == Failed || s == Cancelled }

// Build is one deploy attempt. It is persisted as <id>.json next to its
// log, <id>.log, so the history survives restarts.
type Build struct {
	ID       string `json:"id"`
	App      string `json:"app,omitempty"` // from piway.yaml, known once fetched
	Repo     string `json:"repo"`
	Branch   string `json:"branch"`
	Commit   string `json:"commit"`
	Pusher   string `json:"pusher,omitempty"`
	CloneURL string `json:"clone_url"`
	State    State  `json:"state"`
	// Kind is "rollback" for a rollback to RollbackOf's image; empty for a
	// build from a push.
	Kind       string `json:"kind,omitempty"`
	RollbackOf string `json:"rollback_of,omitempty"`
	Image      string `json:"image,omitempty"`
	// Site is set for a static site: the id of the build whose copy of the
	// site this build serves (its own, or the one a rollback returns to).
	Site     string    `json:"site,omitempty"`
	Error    string    `json:"error,omitempty"`
	Queued   time.Time `json:"queued_at"`
	Started  time.Time `json:"started_at,omitzero"`
	Finished time.Time `json:"finished_at,omitzero"`
}

// artifact identifies what a build serves, so a rollback can find an
// earlier version that differs: its image, or the commit of its site.
func (b *Build) artifact() string {
	if b.Site != "" {
		return "site@" + b.Commit
	}
	return b.Image
}

// key groups builds that replace each other: the same branch of the same
// repository (GitHub names are case-insensitive). Rollbacks of an app
// replace each other, not pushes.
func (b *Build) key() string {
	if b.Kind == KindRollback {
		return "rollback:" + b.App
	}
	return strings.ToLower(b.Repo) + "@" + b.Branch
}

// KindRollback marks a build that re-releases an earlier image.
const KindRollback = "rollback"

// ErrNotFound is returned for an unknown build id.
var ErrNotFound = errors.New("deploy: no such build")

// store keeps build records and logs in one directory.
type store struct{ dir string }

func (s store) metaPath(id string) string { return filepath.Join(s.dir, id+".json") }
func (s store) logPath(id string) string  { return filepath.Join(s.dir, id+".log") }

// manifestPath keeps the piway.yaml a build ran with, so a rollback to it
// needs neither the repository nor a rebuild.
func (s store) manifestPath(id string) string { return filepath.Join(s.dir, id+".piway.yaml") }

func (s store) save(b *Build) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	// Write then rename, so a crash never leaves a half-written record.
	tmp := s.metaPath(b.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.metaPath(b.ID))
}

// load reads every record, oldest first.
func (s store) load() ([]*Build, error) {
	paths, err := filepath.Glob(filepath.Join(s.dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []*Build
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var b Build
		if err := json.Unmarshal(data, &b); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, &b)
	}
	// Ids start with the queue time, so they sort chronologically.
	slices.SortFunc(out, func(a, b *Build) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func (s store) remove(id string) {
	os.Remove(s.metaPath(id))
	os.Remove(s.logPath(id))
	os.Remove(s.manifestPath(id))
}

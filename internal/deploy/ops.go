package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/ofk0/piway/internal/gitauth"
	"github.com/ofk0/piway/internal/webhook"
)

// releaseVerb starts a re-release's log line.
func releaseVerb(b Build, app string) string {
	if b.Kind == KindRestart {
		return "Restarting " + app + " with"
	}
	return "Rolling " + app + " back to"
}

// ErrNotRunning means an app has no version to restart or read logs from.
var ErrNotRunning = errors.New("deploy: the app has no running version")

// Restart queues a fresh release of the version that runs now: a new
// container from the same image, health-checked and switched to like a
// deploy, so the app does not go down. A static site is served again.
func (p *Pipeline) Restart(app string) (Build, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	live := p.liveLocked(app)
	if live == nil {
		return Build{}, ErrNotFound
	}
	// The build whose piway.yaml was saved: the live one, or what it
	// re-released.
	src := live.ID
	if live.released() {
		src = live.RollbackOf
	}
	if _, err := os.Stat(p.store.manifestPath(src)); err != nil {
		return Build{}, fmt.Errorf("deploy: the piway.yaml of build %s is gone", src)
	}
	b := &Build{
		ID: p.newID(live.Commit), App: app, Repo: live.Repo, Branch: live.Branch, Path: live.Path,
		Commit: live.Commit, CloneURL: live.CloneURL, Image: live.Image, Site: live.Site,
		Kind: KindRestart, RollbackOf: src, Pusher: "api", State: Queued, Queued: p.opts.now(),
	}
	if err := p.enqueueLocked(b); err != nil {
		return Build{}, err
	}
	return *b, nil
}

// Target is a repository branch that deploys, with the app it became once
// it has been built.
type Target struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Path   string `json:"path,omitempty"`
	App    string `json:"app,omitempty"`
}

// configured returns the current deploy targets; none without a source.
func (p *Pipeline) configured() []webhook.Target {
	if p.opts.Targets == nil {
		return nil
	}
	return p.opts.Targets.Targets()
}

// Targets lists the configured repository branches (apps.yaml).
func (p *Pipeline) Targets() []Target {
	p.mu.Lock()
	defer p.mu.Unlock()
	configured := p.configured()
	out := make([]Target, 0, len(configured))
	for _, t := range configured {
		tt := Target{Repo: t.Repo, Branch: t.Branch, Path: t.Path}
		for i := len(p.order) - 1; i >= 0; i-- {
			b := p.builds[p.order[i]]
			if b.App != "" && strings.EqualFold(b.Repo, t.Repo) && b.Branch == t.Branch && b.Path == t.Path {
				tt.App = b.App
				break
			}
		}
		out = append(out, tt)
	}
	return out
}

// DeployRequest names what to deploy: an app that has been built before,
// or a configured repository branch; and optionally the exact commit.
type DeployRequest struct {
	App    string `json:"app,omitempty"`
	Repo   string `json:"repo,omitempty"`
	Branch string `json:"branch,omitempty"`
	Path   string `json:"path,omitempty"`   // with repo and branch: which app of the branch
	Commit string `json:"commit,omitempty"` // full SHA; empty means the branch's newest
}

// ErrUnknownTarget means a deploy names neither a known app nor a
// configured repository branch.
var ErrUnknownTarget = errors.New("deploy: not a known app or a repository branch in apps.yaml")

// ErrBadCommit means a deploy names a commit that is not a full SHA.
var ErrBadCommit = errors.New("deploy: commit must be the full 40-character SHA")

// ErrNoHead means the branch's newest commit could not be found.
var ErrNoHead = errors.New("deploy: cannot find the newest commit")

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// DeployNow queues a deploy without a push: of r.Commit, or of the
// branch's newest commit, which it looks up first (git ls-remote). This
// is how to deploy when GitHub cannot reach the Pi.
func (p *Pipeline) DeployNow(ctx context.Context, r DeployRequest, pusher string) (Build, error) {
	if r.Commit != "" && !fullSHA.MatchString(r.Commit) {
		return Build{}, fmt.Errorf("%w, got %q", ErrBadCommit, r.Commit)
	}
	repo, branch, path, cloneURL, err := p.target(r)
	if err != nil {
		return Build{}, err
	}
	commit := r.Commit
	if commit == "" {
		resolve := p.opts.ResolveHead
		if resolve == nil {
			resolve = p.opts.Git.Head
		}
		src := gitauth.Source{Repo: repo, Auth: webhook.AuthOf(p.configured(), repo), URL: cloneURL}
		if commit, err = resolve(ctx, src, branch); err != nil {
			return Build{}, fmt.Errorf("%w of %s@%s: %w", ErrNoHead, repo, branch, err)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	b := &Build{
		ID: p.newID(commit), Repo: repo, Branch: branch, Commit: commit, Path: path,
		Pusher: pusher, CloneURL: cloneURL, State: Queued, Queued: p.opts.now(),
	}
	if err := p.enqueueLocked(b); err != nil {
		return Build{}, err
	}
	return *b, nil
}

// target finds the repository, branch, path and clone URL r means.
func (p *Pipeline) target(r DeployRequest) (repo, branch, path, cloneURL string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.App != "" {
		for i := len(p.order) - 1; i >= 0; i-- {
			b := p.builds[p.order[i]]
			if b.App == r.App && !b.released() && b.CloneURL != "" {
				return b.Repo, b.Branch, b.Path, b.CloneURL, nil
			}
		}
		return "", "", "", "", fmt.Errorf("%w: no build of app %q yet; deploy its repo and branch instead", ErrUnknownTarget, r.App)
	}
	for _, t := range p.configured() {
		if strings.EqualFold(t.Repo, r.Repo) && t.Branch == r.Branch && t.Path == r.Path {
			cloneURL := "https://github.com/" + t.Repo + ".git"
			for i := len(p.order) - 1; i >= 0; i-- { // a URL seen in a push wins
				if b := p.builds[p.order[i]]; strings.EqualFold(b.Repo, t.Repo) && b.CloneURL != "" {
					cloneURL = b.CloneURL
					break
				}
			}
			return t.Repo, t.Branch, t.Path, cloneURL, nil
		}
	}
	return "", "", "", "", fmt.Errorf("%w: %s", ErrUnknownTarget, webhook.Target{Repo: r.Repo, Branch: r.Branch, Path: r.Path}.Name())
}

// LogReader reads an app's output. ExecSteps implements it.
type LogReader interface {
	Logs(ctx context.Context, app string, lines int) (string, error)
}

// Logs returns the last lines of what the app's running container wrote.
func (p *Pipeline) Logs(ctx context.Context, app string, lines int) (string, error) {
	p.mu.Lock()
	live := p.liveLocked(app)
	p.mu.Unlock()
	switch {
	case live == nil:
		return "", ErrNotFound
	case live.Site != "":
		return "", fmt.Errorf("%w: %s is a static site, which has no process to log", ErrNotRunning, app)
	}
	lr, ok := p.opts.Runtime.(LogReader)
	if !ok {
		return "", fmt.Errorf("%w: no container runtime", ErrNotRunning)
	}
	return lr.Logs(ctx, app, lines)
}

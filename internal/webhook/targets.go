package webhook

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/ofk0/piway/internal/gitauth"
	"go.yaml.in/yaml/v3"
)

// repoName is GitHub's owner/name: letters, digits, '-', '_' and '.'.
var repoName = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

// LoadTargets reads the deploy targets from an apps file (docs/DEPLOY.md):
//
//	deploy:
//	  - repo: OFK0/guestbook
//	    branch: main
//	  - repo: OFK0/private-app
//	    branch: main
//	    auth: deploy-key
//
// All branches of a repository use the same auth.
// A missing file means no targets.
func LoadTargets(path string) ([]Target, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var file struct {
		Deploy []Target `yaml:"deploy"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true) // a typo such as "brach" is an error, not a silent no-op
	if err := dec.Decode(&file); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i := range file.Deploy {
		if err := checkTarget(file.Deploy[i], file.Deploy[:i]); err != nil {
			return nil, fmt.Errorf("%s: deploy[%d]: %w", path, i, err)
		}
	}
	return file.Deploy, nil
}

// checkTarget validates t against the targets listed before it.
func checkTarget(t Target, before []Target) error {
	switch {
	case !gitauth.ValidProvider(t.Provider):
		return fmt.Errorf("%w: provider %q is not github or gitlab", ErrInvalid, t.Provider)
	case t.Host != "" && !t.IsGitLab():
		return fmt.Errorf("%w: host is for GitLab; set provider: gitlab", ErrInvalid)
	case t.Host != "" && !gitauth.ValidHost(t.Host):
		return fmt.Errorf("%w: host %q must be a server name such as git.example.com, without https:// or a port", ErrInvalid, t.Host)
	case !t.IsGitLab() && !repoName.MatchString(t.Repo):
		return fmt.Errorf("%w: repo %q is not owner/name", ErrInvalid, t.Repo)
	case t.IsGitLab() && !gitauth.ValidRepo(gitauth.GitLab, t.Repo):
		return fmt.Errorf("%w: repo %q is not a GitLab project path such as group/name or group/subgroup/name", ErrInvalid, t.Repo)
	case t.IsGitLab() && t.Auth == gitauth.Token:
		return fmt.Errorf("%w: GitLab repositories are read with a deploy key, not a token", ErrInvalid)
	case t.Branch == "":
		return fmt.Errorf("%w: branch is required", ErrInvalid)
	case !gitauth.Valid(t.Auth):
		return fmt.Errorf("%w: auth %q is not %s or %s (leave it out for a public repository)", ErrInvalid, t.Auth, gitauth.DeployKey, gitauth.Token)
	case !ValidPath(t.Path):
		return fmt.Errorf("%w: path %q must be a directory in the repository, such as apps/web (no leading /, no ..)", ErrInvalid, t.Path)
	}
	for _, b := range before {
		if !strings.EqualFold(b.Repo, t.Repo) {
			continue
		}
		if b.IsGitLab() != t.IsGitLab() || (t.IsGitLab() && !strings.EqualFold(orDefault(b.Host, gitauth.DefaultGitLabHost), orDefault(t.Host, gitauth.DefaultGitLabHost))) {
			return fmt.Errorf("%w: %s is listed on %s, not on %s; a repository name belongs to one provider (remove it and add it again to change)",
				ErrInvalid, t.Repo, where(b), where(t))
		}
		if b.Auth != t.Auth {
			return fmt.Errorf("%w: %s is listed as %s, not %s; every branch of a repository uses the same auth (remove it and add it again to change)",
				ErrInvalid, t.Repo, authName(b.Auth), authName(t.Auth))
		}
		if b.Branch == t.Branch && b.Path == t.Path {
			return fmt.Errorf("%s: %w", t.Name(), ErrExists)
		}
	}
	return nil
}

var pathChars = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// ValidPath reports whether p can be a target's path: empty (the root),
// or a clean relative directory inside the repository.
func ValidPath(p string) bool {
	if p == "" {
		return true
	}
	return pathChars.MatchString(p) && path.Clean(p) == p && p != "." &&
		!strings.HasPrefix(p, "/") && p != ".." && !strings.HasPrefix(p, "../")
}

// where names the provider (and server) of a target in a message.
func where(t Target) string {
	if !t.IsGitLab() {
		return "GitHub"
	}
	return "GitLab (" + orDefault(t.Host, gitauth.DefaultGitLabHost) + ")"
}

// TargetOf returns the target that lists repo, for its provider, host and
// auth, which are the same for all its branches.
func TargetOf(targets []Target, repo string) (Target, bool) {
	for _, t := range targets {
		if strings.EqualFold(t.Repo, repo) {
			return t, true
		}
	}
	return Target{}, false
}

func authName(auth string) string {
	if auth == gitauth.Public {
		return "public"
	}
	return auth
}

// Listed reports whether any branch of repo is in targets.
func Listed(targets []Target, repo string) bool {
	for _, t := range targets {
		if strings.EqualFold(t.Repo, repo) {
			return true
		}
	}
	return false
}

// AuthOf returns the auth that targets give repo, which is the same for
// all its branches; Public if it is not listed.
func AuthOf(targets []Target, repo string) string {
	for _, t := range targets {
		if strings.EqualFold(t.Repo, repo) {
			return t.Auth
		}
	}
	return gitauth.Public
}

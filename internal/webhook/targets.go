package webhook

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
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
	case !repoName.MatchString(t.Repo):
		return fmt.Errorf("%w: repo %q is not owner/name", ErrInvalid, t.Repo)
	case t.Branch == "":
		return fmt.Errorf("%w: branch is required", ErrInvalid)
	case !gitauth.Valid(t.Auth):
		return fmt.Errorf("%w: auth %q is not %s or %s (leave it out for a public repository)", ErrInvalid, t.Auth, gitauth.DeployKey, gitauth.Token)
	}
	for _, b := range before {
		if !strings.EqualFold(b.Repo, t.Repo) {
			continue
		}
		if b.Auth != t.Auth {
			return fmt.Errorf("%w: %s is listed as %s, not %s; every branch of a repository uses the same auth (remove it and add it again to change)",
				ErrInvalid, t.Repo, authName(b.Auth), authName(t.Auth))
		}
		if b.Branch == t.Branch {
			return fmt.Errorf("%s@%s: %w", t.Repo, t.Branch, ErrExists)
		}
	}
	return nil
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

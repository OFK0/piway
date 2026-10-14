package webhook

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// repoName is GitHub's owner/name: letters, digits, '-', '_' and '.'.
var repoName = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

// LoadTargets reads the deploy targets from an apps file (docs/DEPLOY.md):
//
//	deploy:
//	  - repo: OFK0/guestbook
//	    branch: main
//
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
	seen := map[string]bool{}
	for i, t := range file.Deploy {
		switch {
		case !repoName.MatchString(t.Repo):
			return nil, fmt.Errorf("%s: deploy[%d]: repo %q is not owner/name", path, i, t.Repo)
		case t.Branch == "":
			return nil, fmt.Errorf("%s: deploy[%d]: branch is required", path, i)
		}
		key := strings.ToLower(t.Repo) + "@" + t.Branch
		if seen[key] {
			return nil, fmt.Errorf("%s: deploy[%d]: %s@%s is listed twice", path, i, t.Repo, t.Branch)
		}
		seen[key] = true
	}
	return file.Deploy, nil
}

package gitauth

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Kind is what went wrong reaching a repository.
type Kind string

// Kinds of Error.
const (
	NoKey         Kind = "no_key"         // no deploy key generated yet
	NoToken       Kind = "no_token"       // no token stored yet
	KeyRejected   Kind = "key_rejected"   // GitHub knows no such key
	NotFound      Kind = "not_found"      // the credential cannot see this repository
	TokenRejected Kind = "token_rejected" // the token is wrong, expired or revoked
	HostKey       Kind = "host_key"       // GitHub's host key is not the pinned one
	Network       Kind = "network"        // GitHub could not be reached
	NoRef         Kind = "no_ref"         // ls-remote --exit-code found no such branch
	Other         Kind = "other"
)

// Error explains why a repository could not be read, and what to do.
type Error struct {
	Kind   Kind
	Repo   string
	Auth   string
	Detail string // git's own last line, if any
}

func (e *Error) Error() string {
	msg := e.Message()
	if e.Detail != "" && e.Kind != NoKey && e.Kind != NoToken {
		msg += " (" + e.Detail + ")"
	}
	return msg
}

// Message says what is wrong and how to fix it, without git's words.
func (e *Error) Message() string {
	switch e.Kind {
	case NoKey:
		return fmt.Sprintf("%s has no deploy key yet: piway repo add %s -private", e.Repo, e.Repo)
	case NoToken:
		return "no GitHub token is stored: piway repo token set"
	case KeyRejected:
		return fmt.Sprintf("GitHub does not know this deploy key: add it to %s under Settings > Deploy keys (%s)", e.Repo, DeployKeyURL(e.Repo))
	case NotFound:
		if e.Auth == Token {
			return fmt.Sprintf("%s does not exist, or the token has no access to it: give the token Contents: read on this repository", e.Repo)
		}
		if e.Auth == DeployKey {
			return fmt.Sprintf("%s does not exist, or this deploy key was added to a different repository", e.Repo)
		}
		return fmt.Sprintf("%s does not exist, or it is private: set auth to deploy-key or token", e.Repo)
	case TokenRejected:
		return "GitHub rejected the token: it is wrong, expired or revoked (piway repo token set)"
	case HostKey:
		return "GitHub's SSH host key does not match the one piway trusts; nothing was sent. If GitHub announced a new key, update piway"
	case Network:
		return "cannot reach GitHub: check the Pi's network and DNS"
	case NoRef:
		return "no such branch"
	}
	return "git failed"
}

// classify turns a failed git command into an *Error from what git
// printed. GitHub's messages are stable enough to match on.
func classify(repo, auth string, err error, stderr string) error {
	e := &Error{Kind: Other, Repo: repo, Auth: auth, Detail: lastLine(stderr)}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 2 && strings.TrimSpace(stderr) == "" {
		e.Kind = NoRef // ls-remote --exit-code: the remote answered, without the ref
		return e
	}
	s := strings.ToLower(stderr)
	has := func(subs ...string) bool {
		for _, sub := range subs {
			if strings.Contains(s, sub) {
				return true
			}
		}
		return false
	}
	switch {
	case has("host key verification failed", "remote host identification has changed"):
		e.Kind = HostKey
	case has("permission denied (publickey"):
		e.Kind = KeyRejected
	case has("could not resolve", "connection timed out", "operation timed out", "connection refused",
		"network is unreachable", "no route to host", "failed to connect", "connection reset",
		"kex_exchange_identification", "connection closed by", "temporary failure in name resolution"):
		e.Kind = Network
	case has("authentication failed", "invalid username or token", "password authentication is not supported",
		"could not read username", "could not read password"):
		e.Kind = TokenRejected
		if auth == Public {
			e.Kind = NotFound // GitHub asks for credentials for a private repository
		}
	case has("repository not found", "could not read from remote repository"):
		e.Kind = NotFound
	}
	if e.Detail == "" {
		e.Detail = err.Error()
	}
	return e
}

// lastLine is the last non-empty line of git's output, which is usually
// the reason (earlier lines are progress and hints).
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		l = strings.TrimPrefix(l, "fatal: ")
		switch l {
		case "", "Could not read from remote repository.", "and the repository exists.",
			"Please make sure you have the correct access rights":
			continue
		}
		return l
	}
	return ""
}

// DeployKeyURL is the repository's page for adding a deploy key.
func DeployKeyURL(repo string) string {
	return "https://github.com/" + repo + "/settings/keys/new"
}

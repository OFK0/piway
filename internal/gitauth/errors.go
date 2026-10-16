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
	HostKey       Kind = "host_key"       // the server's host key is not the pinned one
	UnknownHost   Kind = "unknown_host"   // no host key is known for a self-hosted GitLab
	Network       Kind = "network"        // GitHub could not be reached
	NoRef         Kind = "no_ref"         // ls-remote --exit-code found no such branch
	Other         Kind = "other"
)

// Error explains why a repository could not be read, and what to do.
type Error struct {
	Kind     Kind
	Repo     string
	Auth     string
	Provider string // GitHub when empty
	Host     string // the GitLab server
	Detail   string // git's own last line, if any; for UnknownHost, the file to add the key to
}

// providerName is what the provider is called in a message.
func providerName(provider string) string {
	if IsGitLab(provider) {
		return "GitLab"
	}
	return "GitHub"
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
	prov := providerName(e.Provider)
	switch e.Kind {
	case NoKey:
		return fmt.Sprintf("%s has no deploy key yet: piway repo add %s -private%s", e.Repo, e.Repo, e.addFlags())
	case NoToken:
		return "no GitHub token is stored: piway repo token set"
	case KeyRejected:
		return fmt.Sprintf("%s does not know this deploy key: add it to %s under %s (%s)", prov, e.Repo, deployKeysPage(e.Provider), DeployKeyURLFor(e.Provider, e.Host, e.Repo))
	case NotFound:
		if e.Auth == Token {
			return fmt.Sprintf("%s does not exist, or the token has no access to it: give the token Contents: read on this repository", e.Repo)
		}
		if e.Auth == DeployKey {
			return fmt.Sprintf("%s does not exist, or this deploy key was added to a different repository", e.Repo)
		}
		if IsGitLab(e.Provider) {
			return fmt.Sprintf("%s does not exist, or it is private: set auth to deploy-key", e.Repo)
		}
		return fmt.Sprintf("%s does not exist, or it is private: set auth to deploy-key or token", e.Repo)
	case TokenRejected:
		return "GitHub rejected the token: it is wrong, expired or revoked (piway repo token set)"
	case HostKey:
		return prov + "'s SSH host key does not match the one piway trusts; nothing was sent. If " + prov + " announced a new key, update piway"
	case UnknownHost:
		return fmt.Sprintf("piway knows no SSH host key for %s. Check the server's fingerprint with its administrator, then add its key: ssh-keyscan -t ed25519 %s >> %s", e.Host, e.Host, e.Detail)
	case Network:
		return "cannot reach " + prov + ": check the Pi's network and DNS"
	case NoRef:
		return "no such branch"
	}
	return "git failed"
}

// addFlags are the piway repo add flags that name a self-hosted GitLab.
func (e *Error) addFlags() string {
	switch {
	case IsGitLab(e.Provider) && e.Host != "" && e.Host != DefaultGitLabHost:
		return " -gitlab -host " + e.Host
	case IsGitLab(e.Provider):
		return " -gitlab"
	}
	return ""
}

func deployKeysPage(provider string) string {
	if IsGitLab(provider) {
		return "Settings > Repository > Deploy keys"
	}
	return "Settings > Deploy keys"
}

// classify turns a failed git command into an *Error from what git
// printed. GitHub's messages are stable enough to match on.
func classify(src Source, err error, stderr string) error {
	e := &Error{Kind: Other, Repo: src.Repo, Auth: src.Auth, Provider: src.Provider, Host: src.Host, Detail: lastLine(stderr)}
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
		if src.Auth == Public {
			e.Kind = NotFound // the provider asks for credentials for a private repository
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

// DeployKeyURL is a GitHub repository's page for adding a deploy key.
func DeployKeyURL(repo string) string {
	return DeployKeyURLFor(GitHub, "", repo)
}

// DeployKeyURLFor is the page for adding a deploy key to a repository of
// provider (on host, for GitLab).
func DeployKeyURLFor(provider, host, repo string) string {
	if IsGitLab(provider) {
		return "https://" + hostOf(Source{Host: host}) + "/" + repo + "/-/settings/repository"
	}
	return "https://github.com/" + repo + "/settings/keys/new"
}

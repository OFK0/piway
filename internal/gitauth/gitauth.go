// Package gitauth lets git reach private GitHub repositories, with a
// deploy key (SSH) or a fine-grained personal access token (HTTPS).
//
// # Deploy keys
//
// piway generates one ed25519 key pair per repository and keeps it in its
// keys directory with mode 0600. Only the public half is ever shown; it is
// added to the repository as a read-only deploy key. git connects with
// ssh -i <key>, with nothing else from ~/.ssh: no agent, no other identity,
// no user known_hosts. GitHub's host keys are pinned (hostKeys below), so a
// first connection trusts nothing it is shown. Networks that block port 22
// often allow GitHub's SSH service on ssh.github.com:443; when port 22
// cannot be reached, piway uses that instead, and keeps using it.
//
// # Tokens
//
// One token, with Contents: read on the chosen repositories, serves every
// repository whose auth is "token". It never goes into a URL or a command
// line, where it would show up in logs and process listings: git asks for
// it through GIT_ASKPASS, which runs piway itself in askpass mode (see
// Askpass) to read it from its file.
package gitauth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// Repository auth methods, as written in apps.yaml.
const (
	Public    = ""           // nothing: the repository is public
	DeployKey = "deploy-key" // an SSH key piway generated for the repository
	Token     = "token"      // the personal access token in the keys directory
)

// Valid reports whether method is one of Public, DeployKey and Token.
func Valid(method string) bool {
	return method == Public || method == DeployKey || method == Token
}

// AskpassEnv names the token file when piway runs as git's askpass
// program. main checks it before anything else.
const AskpassEnv = "PIWAY_ASKPASS_TOKEN_FILE"

// tokenUser is the user name GitHub expects with a token over HTTPS. Any
// non-empty name works; this one says what the password is.
const tokenUser = "x-access-token"

// hostKeys are GitHub's SSH host keys, as GitHub publishes them
// (https://api.github.com/meta, "ssh_keys"). They serve both github.com:22
// and ssh.github.com:443.
var hostKeys = []string{
	"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl",
	"ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEmKSENjQEezOmxkZMy7opKgwFB9nkt5YRrYMjNuG5N87uRgg6CLrbo5wAdT/y6v0mKV0U2w0WZ2YB/++Tpockg=",
	"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCj7ndNxQowgcQnjshcLrqPEiiphnt+VTTvDP6mHBL9j1aNUkY4Ue1gvwnGLVlOhGeYrnZaMgRK6+PKCUXaDbC7qtbW8gIkhL7aGCsOr/C56SJMy/BCZfxd1nWzAOxSDPgVsmerOBYfNqltV9/hWCqBywINIR+5dIg6JTJ72pcEpEjcYgXkE2YEFXV1JHnsKgbLWNlhScqb2UmyRkQyytRLtL+38TGxkxCflmO+5Z8CSSNY7GidjMIZ7Q4zMjA2n1nGrlTDkzwDCsw+wqFPGQA179cnfGWOWRVruj16z6XyvxvjJwbz0wQZ75XK5tKSb7FNyeIEs4TT4jk+S4dhPeAUC5y+bDYirYgM4GC7uEnztnZyaVWQ7B381AK4Qdrwt51ZqExKbQpTUNn+EjqoTwvqNj4kqx5QUCI0ThS/YkOxJCXmPUWZbhjpCg56i+2aB6CmK2JGhn57K5mj0MNdBXA4/WnwH6XoPWJzK5Nyu2zB3nAZp+S5hpQs+p1vN1/wsjk=",
}

// gitlabHostKeys are gitlab.com's SSH host keys. They were taken with
// ssh-keyscan, and checked against the fingerprints GitLab publishes
// (https://docs.gitlab.com/user/gitlab_com/#ssh-known_hosts-entries):
//
//	ED25519 SHA256:eUXGGm1YGsMAS7vkcx6JOJdOGHPem5gQp4taiCfCLB8
//	ECDSA   SHA256:HbW3g8zUjNSksFbqTiUWPWg2Bq1x8xdGUrliXFzSnUw
//	RSA     SHA256:ROQFvPThGrW4RuWLoL9tq9I9zJ42fK4XywyRtbOz/EQ
var gitlabHostKeys = []string{
	"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQCsj2bNKTBSpIYDEGk9KxsGh3mySTRgMtXL583qmBpzeQ+jqCMRgBqB98u3z++J1sKlXHWfM9dyhSevkMwSbhoR8XIq/U0tCNyokEi/ueaBMCvbcTHhO7FcwzY92WK4Yt0aGROY5qX2UKSeOvuP4D6TPqKF1onrSzH9bx9XUf2lEdWT/ia1NEKjunUqu1xOB/StKDHMoX4/OKyIzuS0q/T1zOATthvasJFoPrAjkohTyaDUz2LN5JoH839hViyEG82yB+MjcFV5MU3N1l1QL3cVUCh93xSaua1N85qivl+siMkPGbO5xR/En4iEY6K2XPASUEMaieWVNTRCtJ4S8H+9",
	"ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBFSMqzJeV9rUzU4kWitGjeR4PWSa29SPqJ1fVkhtj3Hw9xjLVXVYrU9QlYWrOLXBpQ6KWjbjTDTdDkoohFzgbEY=",
	"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAfuCHKVTjquxvt6CM6tdG4SLp1Btn/nOeHHE5UOzRdf",
}

// repoName is GitHub's owner/name. Owners have no '_' or '.', so
// owner_name is an unambiguous file name.
var repoName = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

// Access reaches GitHub repositories with the credentials in Dir. A nil
// *Access reaches public repositories only.
type Access struct {
	Dir string // keys directory: <owner>_<name>[.pub], github-token, known_hosts
	// Askpass is the program git runs for a token: the piway binary.
	Askpass string

	// Where GitHub is; tests point these elsewhere.
	git     string // the git binary
	https   string // https://github.com/
	ssh22   string // ssh://git@github.com/
	ssh443  string // ssh://git@ssh.github.com:443/
	timeout time.Duration
	use443  atomic.Bool // port 22 was unreachable; go straight to 443
}

// New returns an Access for the keys in dir, answering token prompts with
// this executable.
func New(dir string) *Access {
	a := &Access{Dir: dir}
	if exe, err := os.Executable(); err == nil {
		a.Askpass = exe
	}
	return a
}

func (a *Access) gitBin() string     { return or(a.git, "git") }
func (a *Access) httpsBase() string  { return or(a.https, "https://github.com/") }
func (a *Access) ssh22Base() string  { return or(a.ssh22, "ssh://git@github.com/") }
func (a *Access) ssh443Base() string { return or(a.ssh443, "ssh://git@ssh.github.com:443/") }

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// Source is a repository to read and how.
type Source struct {
	Repo string // owner/name; for GitLab the project path, such as group/sub/name
	Auth string // Public, DeployKey or Token
	// Provider is GitHub (the default, empty) or GitLab, and Host is
	// the GitLab server, gitlab.com when empty.
	Provider, Host string
	// URL is the clone URL a push named, used for a public repository;
	// empty means the repository's HTTPS address on its provider.
	URL string
}

// Providers.
const (
	GitHub = ""       // the default; "github" means the same
	GitLab = "gitlab" // gitlab.com, or a self-hosted server named by Host
)

// DefaultGitLabHost is where a GitLab repository lives without a host.
const DefaultGitLabHost = "gitlab.com"

// IsGitLab reports whether provider is GitLab.
func IsGitLab(provider string) bool { return strings.EqualFold(provider, GitLab) }

// ValidProvider reports whether provider is one piway knows.
func ValidProvider(provider string) bool {
	return provider == "" || strings.EqualFold(provider, "github") || IsGitLab(provider)
}

// hostOf is the GitLab server of src.
func hostOf(src Source) string {
	if src.Host == "" {
		return DefaultGitLabHost
	}
	return strings.ToLower(src.Host)
}

// ValidHost reports whether h can be the host of a self-hosted GitLab: a
// DNS name or an address, with no scheme, port or path.
func ValidHost(h string) bool { return gitlabHost.MatchString(h) }

var gitlabHost = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

// RepoURL is the HTTPS address of a repository on its provider.
func RepoURL(provider, host, repo string) string {
	if IsGitLab(provider) {
		return "https://" + hostOf(Source{Host: host}) + "/" + repo + ".git"
	}
	return "https://github.com/" + repo + ".git"
}

// ValidRepo reports whether repo can be a repository of provider: GitHub's
// owner/name, or a GitLab project path of two or more names.
func ValidRepo(provider, repo string) bool {
	if !IsGitLab(provider) {
		return repoName.MatchString(repo)
	}
	return gitlabPath(repo)
}

// gitlabPath matches group/project and group/subgroup/project: names of
// letters, digits, '.', '_' and '-', but never "." or "..", so a path cannot
// leave the directory it is used in.
func gitlabPath(repo string) bool {
	parts := strings.Split(repo, "/")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || !gitlabName.MatchString(p) || strings.HasSuffix(p, ".git") {
			return false
		}
	}
	return true
}

var gitlabName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validAny accepts a repository of either provider, for the key
// functions, which are told only the name.
func validAny(repo string) bool { return repoName.MatchString(repo) || gitlabPath(repo) }

// remote is one way to reach a source: a URL and the environment git
// needs for it.
type remote struct {
	url  string
	env  []string
	note string // said before trying it, when it is a fallback
}

// remotes lists the ways to reach src, best first.
func (a *Access) remotes(src Source) ([]remote, error) {
	base := []string{
		"GIT_TERMINAL_PROMPT=0", // fail instead of waiting for a password
		// No credential helper from the user's git config (such as gh's)
		// may answer, or store what piway sends: an empty helper resets
		// the list, URL-specific ones included. Only piway's own
		// credential is used, so what works here works in the service.
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=",
	}
	if src.Auth != Public && !ValidRepo(src.Provider, src.Repo) {
		return nil, fmt.Errorf("gitauth: %q is not a repository name", src.Repo)
	}
	if !ValidProvider(src.Provider) {
		return nil, fmt.Errorf("gitauth: unknown provider %q", src.Provider)
	}
	gl := IsGitLab(src.Provider)
	if gl && !ValidHost(hostOf(src)) {
		return nil, fmt.Errorf("gitauth: %q is not a GitLab host name", src.Host)
	}
	switch src.Auth {
	case Public:
		url := src.URL
		if url == "" {
			if gl {
				url = RepoURL(src.Provider, src.Host, src.Repo)
			} else {
				url = a.httpsBase() + src.Repo + ".git"
			}
		}
		return []remote{{url: url, env: base}}, nil

	case Token:
		if gl {
			return nil, fmt.Errorf("gitauth: %s repositories are read with a deploy key, not a token", "GitLab")
		}
		file := a.tokenFile()
		if _, err := os.Stat(file); err != nil {
			return nil, &Error{Kind: NoToken, Repo: src.Repo}
		}
		env := append(base, "GIT_ASKPASS="+a.Askpass, AskpassEnv+"="+file)
		return []remote{{url: a.httpsBase() + src.Repo + ".git", env: env}}, nil

	case DeployKey:
		key := a.keyFile(src.Repo)
		if _, err := os.Stat(key); err != nil {
			return nil, &Error{Kind: NoKey, Repo: src.Repo}
		}
		if gl && !a.knowsHost(hostOf(src)) {
			return nil, &Error{Kind: UnknownHost, Repo: src.Repo, Provider: src.Provider, Host: hostOf(src), Detail: a.extraHostsFile()}
		}
		known, err := a.knownHosts()
		if err != nil {
			return nil, err
		}
		env := append(base, "GIT_SSH_COMMAND="+sshCommand(key, known), "GIT_SSH_VARIANT=ssh")
		if gl { // GitLab's SSH service is on port 22; there is no second way in
			return []remote{{url: "ssh://git@" + hostOf(src) + "/" + src.Repo + ".git", env: env}}, nil
		}
		on22 := remote{url: a.ssh22Base() + src.Repo + ".git", env: env}
		on443 := remote{url: a.ssh443Base() + src.Repo + ".git", env: env,
			note: "github.com:22 is unreachable; trying ssh.github.com:443"}
		if a.use443.Load() {
			on443.note = ""
			return []remote{on443}, nil
		}
		return []remote{on22, on443}, nil
	}
	return nil, fmt.Errorf("gitauth: unknown auth %q (want %s or %s)", src.Auth, DeployKey, Token)
}

// sshCommand is GIT_SSH_COMMAND, which git runs through a shell: this
// key only, the pinned host keys only, and never a prompt.
func sshCommand(key, known string) string {
	return strings.Join([]string{
		"ssh", "-F", "/dev/null",
		"-i", shellQuote(key),
		"-o", "IdentitiesOnly=yes",
		"-o", "IdentityAgent=none",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + shellQuote(known),
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "UpdateHostKeys=no",
		"-o", "ConnectTimeout=15",
	}, " ")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Run runs git with args(url) in dir for each way to reach src until one
// works, writing the commands and git's output to out. It moves on to
// the next way only when the network failed, not when GitHub said no.
// The error is an *Error when git's output says what went wrong.
func (a *Access) Run(ctx context.Context, src Source, dir string, out io.Writer, args func(url string) []string) error {
	_, err := a.run(ctx, src, dir, out, args)
	return err
}

// run is Run, returning git's standard output as well.
func (a *Access) run(ctx context.Context, src Source, dir string, out io.Writer, args func(url string) []string) ([]byte, error) {
	if a == nil {
		a = &Access{} // public repositories only
	}
	rs, err := a.remotes(src)
	if err != nil {
		return nil, err
	}
	var last error
	for i, r := range rs {
		if r.note != "" {
			fmt.Fprintln(out, r.note)
		}
		argv := args(r.url)
		fmt.Fprintf(out, "$ git %s\n", strings.Join(argv, " "))
		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, a.gitBin(), argv...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), r.env...)
		cmd.Stdout = &stdout
		cmd.Stderr = io.MultiWriter(out, &stderr)
		cmd.WaitDelay = 5 * time.Second // after the kill, stop waiting on stuck pipes
		err := cmd.Run()
		if err != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			last = classify(src, err, stderr.String())
		}
		var e *Error
		network := errors.As(last, &e) && e.Kind == Network
		if i > 0 && (err == nil || !network) {
			a.use443.Store(true) // GitHub answered on 443 after 22 did not
		}
		if err == nil {
			return stdout.Bytes(), nil
		}
		if !network {
			break
		}
	}
	return nil, last
}

// Head returns branch's newest commit (git ls-remote).
func (a *Access) Head(ctx context.Context, src Source, branch string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, a.limit())
	defer cancel()
	out, err := a.run(ctx, src, "", io.Discard, func(url string) []string {
		return []string{"ls-remote", "--exit-code", url, "refs/heads/" + branch}
	})
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e.Kind == NoRef {
			return "", fmt.Errorf("no branch %q", branch)
		}
		return "", err
	}
	sha, _, _ := strings.Cut(string(out), "\t")
	if !fullSHA.MatchString(sha) {
		return "", fmt.Errorf("git ls-remote answered %q", strings.TrimSpace(string(out)))
	}
	return sha, nil
}

// Test checks that piway can read src: it asks GitHub for the
// repository's branches. The error is an *Error saying what to fix.
func (a *Access) Test(ctx context.Context, src Source) error {
	ctx, cancel := context.WithTimeout(ctx, a.limit())
	defer cancel()
	_, err := a.run(ctx, src, "", io.Discard, func(url string) []string {
		return []string{"ls-remote", "--heads", url}
	})
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Kind: Network, Repo: src.Repo, Provider: src.Provider, Host: src.Host, Detail: "no answer from " + providerName(src.Provider) + " in time"}
	}
	return err
}

func (a *Access) limit() time.Duration {
	if a != nil && a.timeout > 0 {
		return a.timeout
	}
	return 45 * time.Second
}

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

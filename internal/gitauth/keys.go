package gitauth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// keyFile is where repo's private key lives; the public key is next to
// it with .pub. Names are lower case, as GitHub's are case-insensitive.
func (a *Access) keyFile(repo string) string {
	return filepath.Join(a.Dir, keyName(repo))
}

// keyName is the file name of a repository's key. GitHub's owner/name keeps
// the name it always had. A GitLab project path can have several slashes,
// and names with '_' make '_' ambiguous, so it is escaped (group%2Fsub%2Fname).
// A repository name belongs to one provider at a time, so the two never
// meet.
func keyName(repo string) string {
	if strings.Count(repo, "/") == 1 && repoName.MatchString(repo) {
		return strings.ToLower(strings.Replace(repo, "/", "_", 1))
	}
	return strings.ToLower(url.QueryEscape(repo))
}

func (a *Access) tokenFile() string { return filepath.Join(a.Dir, "github-token") }

// HasKey reports whether repo has a deploy key.
func (a *Access) HasKey(repo string) bool {
	if !validAny(repo) {
		return false
	}
	_, err := os.Stat(a.keyFile(repo))
	return err == nil
}

// GenerateKey makes repo's deploy key if it has none, and returns the
// public key in authorized_keys form, to paste into GitHub. Calling it
// again returns the same key.
func (a *Access) GenerateKey(repo string) (string, error) {
	if !validAny(repo) {
		return "", fmt.Errorf("gitauth: %q is not owner/name", repo)
	}
	if pub, err := a.PublicKey(repo); err == nil {
		return pub, nil
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	comment := "piway " + repo
	if host, err := os.Hostname(); err == nil && host != "" {
		comment = "piway@" + host + " " + repo
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " " + comment
	key := a.keyFile(repo)
	// The public key last: its presence is what PublicKey trusts.
	if err := writeFile(key, pem.EncodeToMemory(block)); err != nil {
		return "", err
	}
	if err := writeFile(key+".pub", []byte(line+"\n")); err != nil {
		return "", err
	}
	return line, nil
}

// PublicKey returns repo's public deploy key in authorized_keys form.
func (a *Access) PublicKey(repo string) (string, error) {
	if !a.HasKey(repo) {
		return "", &Error{Kind: NoKey, Repo: repo}
	}
	b, err := os.ReadFile(a.keyFile(repo) + ".pub")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// RemoveKey deletes repo's deploy key. A repository without one is not
// an error.
func (a *Access) RemoveKey(repo string) error {
	if !validAny(repo) {
		return fmt.Errorf("gitauth: %q is not owner/name", repo)
	}
	key := a.keyFile(repo)
	return errors.Join(ignoreMissing(os.Remove(key)), ignoreMissing(os.Remove(key+".pub")))
}

func ignoreMissing(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// HasToken reports whether a token is stored.
func (a *Access) HasToken() bool {
	_, err := os.Stat(a.tokenFile())
	return err == nil
}

// SetToken stores the token every "auth: token" repository uses.
func (a *Access) SetToken(token string) error {
	token = strings.TrimSpace(token)
	switch {
	case token == "":
		return errors.New("the token is empty")
	case strings.ContainsAny(token, " \t\r\n"):
		return errors.New("a token is one word, without spaces")
	case len(token) > 1024:
		return errors.New("that is too long for a GitHub token")
	}
	return writeFile(a.tokenFile(), []byte(token+"\n"))
}

// RemoveToken deletes the stored token.
func (a *Access) RemoveToken() error {
	return ignoreMissing(os.Remove(a.tokenFile()))
}

// extraHostsFile is where host keys of self-hosted servers are added, in
// known_hosts format (ssh-keyscan writes it).
func (a *Access) extraHostsFile() string { return filepath.Join(a.Dir, "known_hosts_extra") }

// extraHosts returns the lines of the extra host keys file, which are
// well-formed: a host and a key.
func (a *Access) extraHosts() []string {
	b, err := os.ReadFile(a.extraHostsFile())
	if err != nil {
		return nil
	}
	var out []string
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, _, _, _, _, err := ssh.ParseKnownHosts([]byte(line)); err == nil {
			out = append(out, line)
		}
	}
	return out
}

// knowsHost reports whether piway has an SSH host key for a GitLab host:
// gitlab.com's are built in, any other server's come from the extra file.
func (a *Access) knowsHost(host string) bool {
	if host == DefaultGitLabHost {
		return true
	}
	for _, line := range a.extraHosts() {
		hosts, _, _ := strings.Cut(line, " ")
		for h := range strings.SplitSeq(hosts, ",") {
			if strings.EqualFold(strings.TrimPrefix(h, "@"), host) {
				return true
			}
		}
	}
	return false
}

// knownHosts writes the pinned host keys (GitHub's on both of its ports,
// gitlab.com's) and the extra ones, and returns the file's path.
func (a *Access) knownHosts() (string, error) {
	var b bytes.Buffer
	for _, host := range []string{"github.com", "[ssh.github.com]:443"} {
		for _, k := range hostKeys {
			fmt.Fprintf(&b, "%s %s\n", host, k)
		}
	}
	for _, k := range gitlabHostKeys {
		fmt.Fprintf(&b, "%s %s\n", DefaultGitLabHost, k)
	}
	for _, line := range a.extraHosts() {
		fmt.Fprintln(&b, line)
	}
	path := filepath.Join(a.Dir, "known_hosts")
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, b.Bytes()) {
		return path, nil
	}
	return path, writeFile(path, b.Bytes())
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

// Askpass answers one of git's prompts when piway runs as GIT_ASKPASS:
// the user name for a token, then the token from tokenFile. It returns
// the exit status.
func Askpass(prompt, tokenFile string, out io.Writer) int {
	switch {
	case strings.HasPrefix(prompt, "Username"):
		fmt.Fprintln(out, tokenUser)
		return 0
	case strings.HasPrefix(prompt, "Password"):
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			return 1
		}
		fmt.Fprintln(out, strings.TrimSpace(string(b)))
		return 0
	}
	return 1 // not a question piway can answer
}

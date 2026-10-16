package gitauth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// TestMain lets the test binary stand in for piway as git's askpass.
func TestMain(m *testing.M) {
	if file := os.Getenv(AskpassEnv); file != "" && len(os.Args) == 2 {
		os.Exit(Askpass(os.Args[1], file, os.Stdout))
	}
	os.Exit(m.Run())
}

func TestGenerateKey(t *testing.T) {
	a := New(filepath.Join(t.TempDir(), "keys"))
	if a.HasKey("OFK0/Blog") {
		t.Fatal("has a key before one was made")
	}
	pub, err := a.GenerateKey("OFK0/Blog")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pub, "ssh-ed25519 ") || !strings.HasSuffix(pub, " OFK0/Blog") {
		t.Errorf("public key %q", pub)
	}
	again, err := a.GenerateKey("ofk0/blog") // names are case-insensitive
	if err != nil || again != pub {
		t.Errorf("second GenerateKey = %q, %v; want the same key", again, err)
	}

	for _, name := range []string{"", "ofk0_blog", "ofk0_blog.pub"} {
		fi, err := os.Stat(filepath.Join(a.Dir, name))
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o600)
		if name == "" {
			want = 0o700 | os.ModeDir
		}
		if fi.Mode() != want {
			t.Errorf("%s: mode %v, want %v", name, fi.Mode(), want)
		}
	}

	// The private key on disk is the one that matches.
	b, err := os.ReadFile(a.keyFile("ofk0/blog"))
	if err != nil {
		t.Fatal(err)
	}
	priv, err := ssh.ParseRawPrivateKey(b)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := priv.(*ed25519.PrivateKey); !ok {
		t.Errorf("private key is %T", priv)
	}
	if got := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))); !strings.HasPrefix(pub, got+" ") {
		t.Errorf("private key's public half %q does not match %q", got, pub)
	}

	if err := a.RemoveKey("ofk0/blog"); err != nil {
		t.Fatal(err)
	}
	if a.HasKey("ofk0/blog") {
		t.Error("key still there after RemoveKey")
	}
	if err := a.RemoveKey("ofk0/blog"); err != nil {
		t.Errorf("removing a missing key: %v", err)
	}
	var e *Error
	if _, err := a.PublicKey("ofk0/blog"); !errors.As(err, &e) || e.Kind != NoKey {
		t.Errorf("PublicKey without a key: %v", err)
	}
	if _, err := a.GenerateKey("../etc"); err == nil {
		t.Error("GenerateKey accepted a path")
	}
}

func TestRemotes(t *testing.T) {
	a := New(t.TempDir())
	var e *Error

	rs, err := a.remotes(Source{Repo: "o/pub", URL: "https://github.com/o/pub.git"})
	if err != nil || len(rs) != 1 || rs[0].url != "https://github.com/o/pub.git" {
		t.Fatalf("public: %+v, %v", rs, err)
	}

	if _, err := a.remotes(Source{Repo: "o/app", Auth: DeployKey}); !errors.As(err, &e) || e.Kind != NoKey {
		t.Errorf("deploy key without a key: %v", err)
	}
	if _, err := a.remotes(Source{Repo: "o/app", Auth: Token}); !errors.As(err, &e) || e.Kind != NoToken {
		t.Errorf("token without a token: %v", err)
	}
	if _, err := a.remotes(Source{Repo: "o/app", Auth: "password"}); err == nil {
		t.Error("unknown auth accepted")
	}

	if _, err := a.GenerateKey("o/app"); err != nil {
		t.Fatal(err)
	}
	rs, err = a.remotes(Source{Repo: "o/app", Auth: DeployKey, URL: "https://github.com/o/app.git"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].url != "ssh://git@github.com/o/app.git" || rs[1].url != "ssh://git@ssh.github.com:443/o/app.git" {
		t.Fatalf("deploy key remotes: %+v", rs)
	}
	cmd := envValue(rs[0].env, "GIT_SSH_COMMAND")
	for _, want := range []string{"-F /dev/null", "IdentitiesOnly=yes", "IdentityAgent=none", "BatchMode=yes",
		"StrictHostKeyChecking=yes", "GlobalKnownHostsFile=/dev/null", "-i '" + a.keyFile("o/app") + "'"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("GIT_SSH_COMMAND %q lacks %q", cmd, want)
		}
	}
	known, err := os.ReadFile(filepath.Join(a.Dir, "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"github.com ssh-ed25519 ", "[ssh.github.com]:443 ssh-ed25519 "} {
		if !bytes.Contains(known, []byte(host)) {
			t.Errorf("known_hosts lacks %q", host)
		}
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(known)), "\n") {
		if _, _, _, _, _, err := ssh.ParseKnownHosts([]byte(line)); err != nil {
			t.Errorf("known_hosts line %q: %v", line, err)
		}
	}

	if err := a.SetToken("  ghp_example\n"); err != nil {
		t.Fatal(err)
	}
	rs, err = a.remotes(Source{Repo: "o/app", Auth: Token})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || rs[0].url != "https://github.com/o/app.git" || strings.Contains(strings.Join(rs[0].env, " "), "ghp_example") {
		t.Errorf("token remote: %+v", rs)
	}
	if envValue(rs[0].env, "GIT_CONFIG_KEY_0") != "credential.helper" {
		t.Errorf("credential helpers are not turned off: %v", rs[0].env)
	}
}

func envValue(env []string, key string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

func TestSetToken(t *testing.T) {
	a := New(t.TempDir())
	for _, bad := range []string{"", "  ", "two words"} {
		if err := a.SetToken(bad); err == nil {
			t.Errorf("SetToken(%q) accepted", bad)
		}
	}
	if a.HasToken() {
		t.Fatal("a rejected token was stored")
	}
	if err := a.SetToken("github_pat_x"); err != nil || !a.HasToken() {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(a.tokenFile()); fi.Mode() != 0o600 {
		t.Errorf("token file mode %v", fi.Mode())
	}
	if err := a.RemoveToken(); err != nil || a.HasToken() {
		t.Errorf("RemoveToken: %v", err)
	}
	if err := a.RemoveToken(); err != nil {
		t.Errorf("removing a missing token: %v", err)
	}
}

func TestClassify(t *testing.T) {
	exit128 := exec.Command("sh", "-c", "exit 128").Run()
	exit2 := exec.Command("sh", "-c", "exit 2").Run()
	for _, tc := range []struct {
		auth, stderr string
		err          error
		want         Kind
	}{
		{DeployKey, "git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.\n\nPlease make sure you have the correct access rights\nand the repository exists.\n", exit128, KeyRejected},
		{DeployKey, "ERROR: Repository not found.\nfatal: Could not read from remote repository.\n", exit128, NotFound},
		{DeployKey, "ssh: connect to host github.com port 22: Connection timed out\nfatal: Could not read from remote repository.\n", exit128, Network},
		{DeployKey, "ssh: Could not resolve hostname github.com: Temporary failure in name resolution\n", exit128, Network},
		{DeployKey, "kex_exchange_identification: Connection closed by remote host\n", exit128, Network},
		{DeployKey, "No ED25519 host key is known for github.com and you have requested strict checking.\nHost key verification failed.\nfatal: Could not read from remote repository.\n", exit128, HostKey},
		{Token, "remote: Repository not found.\nfatal: repository 'https://github.com/o/app.git/' not found\n", exit128, NotFound},
		{Token, "remote: Invalid username or token. Password authentication is not supported for Git operations.\nfatal: Authentication failed for 'https://github.com/o/app.git/'\n", exit128, TokenRejected},
		{Public, "fatal: could not read Username for 'https://github.com': terminal prompts disabled\n", exit128, NotFound},
		{Token, "fatal: could not read Password for 'https://x-access-token@github.com': terminal prompts disabled\n", exit128, TokenRejected},
		{Token, "fatal: unable to access 'https://github.com/o/app.git/': Could not resolve host: github.com\n", exit128, Network},
		{Public, "", exit2, NoRef},
		{Public, "fatal: something new\n", exit128, Other},
	} {
		var e *Error
		if err := classify(Source{Repo: "o/app", Auth: tc.auth}, tc.err, tc.stderr); !errors.As(err, &e) || e.Kind != tc.want {
			t.Errorf("%q: %v, want %s", tc.stderr, err, tc.want)
		}
	}

	e := classify(Source{Repo: "o/app", Auth: DeployKey}, exit128, "git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.\n\nPlease make sure you have the correct access rights\nand the repository exists.\n").(*Error)
	if e.Detail != "git@github.com: Permission denied (publickey)." {
		t.Errorf("detail %q", e.Detail)
	}
	if !strings.Contains(e.Error(), "https://github.com/o/app/settings/keys/new") {
		t.Errorf("message %q does not link to the deploy keys page", e.Error())
	}
}

// fakeGit writes a git that fails like an unreachable port 22 for
// github.com and answers for ssh.github.com:443, logging each URL.
func fakeGit(t *testing.T) (bin, log string) {
	dir := t.TempDir()
	bin, log = filepath.Join(dir, "git"), filepath.Join(dir, "calls")
	script := `#!/bin/sh
for a; do url=$a; done
case "$*" in *refs/heads/*) url=$3 ;; esac
echo "$url" >> ` + log + `
case "$url" in
ssh://git@ssh.github.com:443/*) printf '%s\trefs/heads/main\n' ` + strings.Repeat("a", 40) + ` ;;
*) echo "ssh: connect to host github.com port 22: Connection timed out" >&2; exit 128 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestFallbackTo443(t *testing.T) {
	a := New(t.TempDir())
	bin, log := fakeGit(t)
	a.git = bin
	if _, err := a.GenerateKey("o/app"); err != nil {
		t.Fatal(err)
	}
	src := Source{Repo: "o/app", Auth: DeployKey}
	var out bytes.Buffer
	sha, err := a.Head(context.Background(), src, "main")
	if err != nil || sha != strings.Repeat("a", 40) {
		t.Fatalf("Head = %q, %v", sha, err)
	}
	if err := a.Run(context.Background(), src, t.TempDir(), &out, func(url string) []string {
		return []string{"fetch", url}
	}); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(log)
	want := "ssh://git@github.com/o/app.git\nssh://git@ssh.github.com:443/o/app.git\nssh://git@ssh.github.com:443/o/app.git\n"
	if string(calls) != want {
		t.Errorf("tried\n%s\nwant\n%s", calls, want)
	}
	if strings.Contains(out.String(), "github.com:22 is unreachable") {
		t.Error("said it falls back after 443 was already known to work")
	}

	// GitHub saying no is not a reason to try another port.
	b := New(t.TempDir())
	b.git = writeScript(t, `echo "git@github.com: Permission denied (publickey)." >&2; exit 128`)
	b.GenerateKey("o/app")
	var e *Error
	if err := b.Test(context.Background(), src); !errors.As(err, &e) || e.Kind != KeyRejected {
		t.Errorf("Test = %v", err)
	}
	if b.use443.Load() {
		t.Error("switched to 443 after an answer on 22")
	}
}

func writeScript(t *testing.T, body string) string {
	bin := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// TestToken runs real git against git http-backend behind basic auth, so
// the token's whole path is exercised: GIT_ASKPASS, the user name, and
// that the token shows up nowhere but in the Authorization header.
func TestToken(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip(err)
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("no git-http-backend")
	}

	// A bare repository o/app.git with one commit on main.
	root := t.TempDir()
	work := filepath.Join(root, "work")
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	os.MkdirAll(work, 0o755)
	run(work, "init", "--quiet", "--initial-branch", "main")
	run(work, "commit", "--quiet", "--allow-empty", "-m", "first")
	head := run(work, "rev-parse", "HEAD")
	os.MkdirAll(filepath.Join(root, "srv", "o"), 0o755)
	run(root, "clone", "--quiet", "--bare", work, filepath.Join(root, "srv", "o", "app.git"))

	const token = "github_pat_SECRET123"
	var mu sync.Mutex
	var users []string
	cgiHandler := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + filepath.Join(root, "srv"), "GIT_HTTP_EXPORT_ALL=1"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		mu.Lock()
		users = append(users, user)
		mu.Unlock()
		if !ok || pass != token {
			w.Header().Set("WWW-Authenticate", `Basic realm="GitHub"`)
			http.Error(w, "Invalid username or token.", http.StatusUnauthorized)
			return
		}
		cgiHandler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	sent := func(user string) bool { // whether a request came with this user name
		mu.Lock()
		defer mu.Unlock()
		return slices.Contains(users, user)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	a := New(t.TempDir())
	a.Askpass = exe
	a.https = srv.URL + "/"
	src := Source{Repo: "o/app", Auth: Token}

	// A credential helper in the user's git config, like gh's, must not
	// answer for piway: only the stored token counts.
	global := filepath.Join(t.TempDir(), "gitconfig")
	helper := "[credential \"" + srv.URL + "\"]\n\thelper = \"!f() { echo username=helper; echo password=" + token + "; }; f\"\n"
	if err := os.WriteFile(global, []byte(helper), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	if err := a.Test(context.Background(), src); err == nil || sent("helper") {
		t.Fatalf("with no token stored: %v", err)
	}
	if err := a.Test(context.Background(), Source{Repo: "o/app"}); err == nil || sent("helper") {
		t.Fatalf("a public source used the helper: %v", err)
	}

	if err := a.SetToken(token); err != nil {
		t.Fatal(err)
	}
	sha, err := a.Head(context.Background(), src, "main")
	if err != nil || sha != head {
		t.Fatalf("Head = %q, %v; want %s", sha, err, head)
	}
	if !sent(tokenUser) {
		t.Errorf("no request with user %q", tokenUser)
	}

	// A fetch writes git's output to the build log; the token is not in it.
	var log bytes.Buffer
	dir := t.TempDir()
	run(dir, "init", "--quiet")
	err = a.Run(context.Background(), src, dir, &log, func(url string) []string {
		return []string{"fetch", "--depth", "1", url, head}
	})
	if err != nil {
		t.Fatalf("fetch: %v\n%s", err, log.String())
	}

	// A wrong token: a clear error, and the token appears nowhere.
	if err := a.SetToken("github_pat_WRONG456"); err != nil {
		t.Fatal(err)
	}
	log.Reset()
	err = a.Run(context.Background(), src, dir, &log, func(url string) []string {
		return []string{"fetch", "--depth", "1", url, head}
	})
	var e *Error
	if !errors.As(err, &e) || e.Kind != TokenRejected {
		t.Errorf("wrong token: %v\n%s", err, log.String())
	}
	if err := a.Test(context.Background(), src); !errors.As(err, &e) || e.Kind != TokenRejected {
		t.Errorf("Test with a wrong token: %v", err)
	}
	for _, s := range []string{log.String(), err.Error()} {
		if strings.Contains(s, "SECRET123") || strings.Contains(s, "WRONG456") {
			t.Errorf("token leaked: %q", s)
		}
	}
}

// The pinned GitLab keys are the ones GitLab publishes.
func TestGitLabHostKeysArePinned(t *testing.T) {
	want := map[string]string{
		"ssh-ed25519":         "SHA256:eUXGGm1YGsMAS7vkcx6JOJdOGHPem5gQp4taiCfCLB8",
		"ecdsa-sha2-nistp256": "SHA256:HbW3g8zUjNSksFbqTiUWPWg2Bq1x8xdGUrliXFzSnUw",
		"ssh-rsa":             "SHA256:ROQFvPThGrW4RuWLoL9tq9I9zJ42fK4XywyRtbOz/EQ",
	}
	if len(gitlabHostKeys) != len(want) {
		t.Fatalf("%d keys, want %d", len(gitlabHostKeys), len(want))
	}
	for _, k := range gitlabHostKeys {
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k))
		if err != nil {
			t.Fatal(err)
		}
		if got := ssh.FingerprintSHA256(pub); got != want[pub.Type()] {
			t.Errorf("%s key is %s, GitLab publishes %s", pub.Type(), got, want[pub.Type()])
		}
	}
}

func TestValidGitLab(t *testing.T) {
	for repo, ok := range map[string]bool{
		"group/project": true, "group/sub/project": true, "g.r_p/a-b/c.d": true, "a/b/c/d/e": true,
		"project": false, "group/": false, "/project": false, "group//project": false,
		"group/../etc": false, "group/./x": false, "group/project.git": false, "gr oup/p": false, "group/p?x": false,
	} {
		if got := ValidRepo(GitLab, repo); got != ok {
			t.Errorf("ValidRepo(gitlab, %q) = %v", repo, got)
		}
	}
	if !ValidRepo(GitHub, "o/app") || ValidRepo(GitHub, "g/sub/project") {
		t.Error("GitHub names are owner/name only")
	}
	for h, ok := range map[string]bool{"gitlab.com": true, "git.example.com": true, "10.0.0.5": true, "host": true,
		"": false, "https://git.example.com": false, "git.example.com:2222": false, "git.example.com/x": false, "-bad": false} {
		if ValidHost(h) != ok {
			t.Errorf("ValidHost(%q) = %v", h, !ok)
		}
	}
}

func TestGitLabRemotes(t *testing.T) {
	a := New(t.TempDir())
	var e *Error

	// Public: HTTPS on gitlab.com, or on the host, or the clone URL a push named.
	for src, want := range map[Source]string{
		{Repo: "grp/sub/app", Provider: GitLab}:                                "https://gitlab.com/grp/sub/app.git",
		{Repo: "grp/app", Provider: GitLab, Host: "Git.Example.com"}:           "https://git.example.com/grp/app.git",
		{Repo: "grp/app", Provider: GitLab, URL: "https://x.test/grp/app.git"}: "https://x.test/grp/app.git",
	} {
		rs, err := a.remotes(src)
		if err != nil || len(rs) != 1 || rs[0].url != want {
			t.Errorf("%+v: %+v %v, want %s", src, rs, err, want)
		}
	}

	// A token is for GitHub.
	if _, err := a.remotes(Source{Repo: "grp/app", Provider: GitLab, Auth: Token}); err == nil {
		t.Error("a token for GitLab was accepted")
	}
	if _, err := a.remotes(Source{Repo: "grp/app", Provider: "bitbucket"}); err == nil {
		t.Error("an unknown provider was accepted")
	}
	if _, err := a.remotes(Source{Repo: "grp/app", Provider: GitLab, Auth: DeployKey, Host: "https://evil"}); err == nil {
		t.Error("a host with a scheme was accepted")
	}

	// A deploy key: no key yet, then SSH to gitlab.com with only the pinned keys.
	src := Source{Repo: "grp/sub/app", Provider: GitLab, Auth: DeployKey}
	if _, err := a.remotes(src); !errors.As(err, &e) || e.Kind != NoKey {
		t.Fatalf("without a key: %v", err)
	}
	if _, err := a.GenerateKey("grp/sub/app"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a.Dir, "grp%2fsub%2fapp")); err != nil {
		t.Errorf("a nested project's key file: %v", err)
	}
	rs, err := a.remotes(src)
	if err != nil || len(rs) != 1 || rs[0].url != "ssh://git@gitlab.com/grp/sub/app.git" {
		t.Fatalf("remotes %+v %v; want one ssh remote, no second port", rs, err)
	}
	known, _ := os.ReadFile(filepath.Join(a.Dir, "known_hosts"))
	if !strings.Contains(string(known), "gitlab.com ssh-ed25519 ") || !strings.Contains(string(known), "github.com ssh-ed25519 ") {
		t.Errorf("known_hosts:\n%s", known)
	}

	// A self-hosted server needs its host key in the extra file, and says so.
	src = Source{Repo: "grp/app", Provider: GitLab, Auth: DeployKey, Host: "git.example.com"}
	a.GenerateKey("grp/app")
	if _, err := a.remotes(src); !errors.As(err, &e) || e.Kind != UnknownHost ||
		!strings.Contains(e.Message(), "ssh-keyscan -t ed25519 git.example.com >> "+a.extraHostsFile()) {
		t.Fatalf("an unknown host: %v", err)
	}
	pub, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(hostKeys[0]))
	os.WriteFile(a.extraHostsFile(), []byte("# added by hand\nnot a key line\ngit.example.com "+string(ssh.MarshalAuthorizedKey(pub))), 0o600)
	rs, err = a.remotes(src)
	if err != nil || rs[0].url != "ssh://git@git.example.com/grp/app.git" {
		t.Fatalf("with its key: %+v %v", rs, err)
	}
	known, _ = os.ReadFile(filepath.Join(a.Dir, "known_hosts"))
	if !strings.Contains(string(known), "git.example.com ssh-ed25519 ") || strings.Contains(string(known), "not a key line") {
		t.Errorf("known_hosts should hold the host's key and no junk:\n%s", known)
	}
}

func TestGitLabMessages(t *testing.T) {
	e := &Error{Kind: KeyRejected, Repo: "grp/sub/app", Provider: GitLab, Host: "git.example.com"}
	if m := e.Message(); !strings.Contains(m, "GitLab does not know this deploy key") || !strings.Contains(m, "https://git.example.com/grp/sub/app/-/settings/repository") || strings.Contains(m, "GitHub") {
		t.Errorf("message %q", m)
	}
	if m := (&Error{Kind: NoKey, Repo: "grp/app", Provider: GitLab, Host: "git.example.com"}).Message(); !strings.Contains(m, "piway repo add grp/app -private -gitlab -host git.example.com") {
		t.Errorf("message %q", m)
	}
	if got := DeployKeyURLFor(GitLab, "", "grp/app"); got != "https://gitlab.com/grp/app/-/settings/repository" {
		t.Errorf("url %q", got)
	}
	if got := DeployKeyURL("o/app"); got != "https://github.com/o/app/settings/keys/new" {
		t.Errorf("GitHub's page moved: %q", got)
	}
	// What git says is classified the same way for GitLab.
	err := classify(Source{Repo: "grp/app", Provider: GitLab, Auth: DeployKey}, exec.Command("sh", "-c", "exit 128").Run(),
		"git@gitlab.com: Permission denied (publickey).\nfatal: Could not read from remote repository.\n")
	var ge *Error
	if !errors.As(err, &ge) || ge.Kind != KeyRejected || ge.Provider != GitLab || !strings.Contains(ge.Error(), "GitLab does not know") {
		t.Errorf("classified as %v", err)
	}
}

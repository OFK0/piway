// Package auth makes the dashboard and the API piway's owner's alone.
//
// There is one user and one password, stored as an argon2id hash in a file
// (see SetPassword). Logging in starts a session: a random token in a
// cookie (HttpOnly, SameSite=Strict), of which piway keeps only a SHA-256
// hash, on disk so a restart does not sign anyone out. Changing the
// password signs every session out.
//
// Until a password is set, piway answers only requests from the Pi itself
// (loopback, and not forwarded by a proxy), such as through an SSH tunnel.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CookieName is the session cookie.
const CookieName = "piway_session"

// SessionTTL is how long a login lasts.
const SessionTTL = 30 * 24 * time.Hour

// Options configure Auth.
type Options struct {
	PasswordFile string // the argon2id hash; missing means no password yet
	SessionsFile string // sessions kept across restarts; "" keeps them in memory
	TokensFile   string // API tokens; "" keeps them in memory
	Logger       *slog.Logger
	now          func() time.Time
}

// Auth checks requests and serves login and logout.
type Auth struct {
	opts    Options
	limiter *limiter
	verify  chan struct{} // one argon2id verification at a time

	mu          sync.Mutex
	sessions    map[string]session // by the hex SHA-256 of the token
	pw          passwordCache
	tokens      []*Token // oldest first
	tokensSaved time.Time
}

type session struct {
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
	// Gen ties the session to the password it was made with.
	Gen string `json:"gen"`
}

// New loads the sessions, dropping expired ones.
func New(opts Options) (*Auth, error) {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.now == nil {
		opts.now = time.Now
	}
	a := &Auth{opts: opts, limiter: newLimiter(), verify: make(chan struct{}, 1), sessions: map[string]session{}}
	if opts.SessionsFile != "" {
		b, err := os.ReadFile(opts.SessionsFile)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return nil, err
		default:
			if err := json.Unmarshal(b, &a.sessions); err != nil {
				opts.Logger.Warn("ignoring unreadable sessions file; everyone signs in again", "file", opts.SessionsFile, "err", err)
				a.sessions = map[string]session{}
			}
		}
	}
	if err := a.loadTokens(); err != nil {
		return nil, fmt.Errorf("API tokens in %s: %w", opts.TokensFile, err)
	}
	a.mu.Lock()
	a.pruneLocked()
	a.mu.Unlock()
	return a, nil
}

// Close writes the tokens' last-used times, which are saved lazily.
func (a *Auth) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.tokens) == 0 {
		return nil
	}
	return a.saveTokensLocked()
}

// passwordCache rereads the password file only when it changes.
type passwordCache struct {
	mod  time.Time
	size int64
	phc  string
}

// password returns the stored hash and its generation, or "" when no
// password is set. Caller holds a.mu.
func (a *Auth) passwordLocked() (phc, gen string) {
	fi, err := os.Stat(a.opts.PasswordFile)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			a.opts.Logger.Error("read password file", "err", err)
		}
		a.pw = passwordCache{}
		return "", ""
	}
	if !fi.ModTime().Equal(a.pw.mod) || fi.Size() != a.pw.size {
		b, err := os.ReadFile(a.opts.PasswordFile)
		if err != nil {
			a.opts.Logger.Error("read password file", "err", err)
			return "", ""
		}
		a.pw = passwordCache{mod: fi.ModTime(), size: fi.Size(), phc: strings.TrimSpace(string(b))}
	}
	if a.pw.phc == "" {
		return "", ""
	}
	sum := sha256.Sum256([]byte(a.pw.phc))
	return a.pw.phc, hex.EncodeToString(sum[:8])
}

// Mode is "password" once a password is set, else "local".
func (a *Auth) Mode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if phc, _ := a.passwordLocked(); phc != "" {
		return "password"
	}
	return "local"
}

// Allowed reports whether r may use the API: it carries a valid API token
// or session, or no password is set and it comes from the Pi itself.
func (a *Auth) Allowed(r *http.Request) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, present := a.tokenLocked(r); present {
		return t != nil // a bad token is refused, even from loopback
	}
	return a.signedInLocked(r)
}

// signedInLocked is Allowed without API tokens: a browser session, or the
// Pi itself while no password is set.
func (a *Auth) signedInLocked(r *http.Request) bool {
	_, gen := a.passwordLocked()
	if gen == "" {
		return local(r)
	}
	s, ok := a.sessionLocked(r)
	return ok && s.Gen == gen
}

func (a *Auth) sessionLocked(r *http.Request) (session, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return session{}, false
	}
	s, ok := a.sessions[tokenKey(c.Value)]
	if !ok || !a.opts.now().Before(s.Expires) {
		return session{}, false
	}
	return s, true
}

// Require answers 401 (403 for a remote request while no password is set)
// unless r is Allowed.
func (a *Auth) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.Allowed(r) {
			next.ServeHTTP(w, r)
			return
		}
		if a.Mode() == "local" {
			writeError(w, http.StatusForbidden, "no password is set, so piway only answers the Pi itself: run piway -set-password, or use an SSH tunnel")
			return
		}
		writeError(w, http.StatusUnauthorized, "sign in first")
	})
}

// requireSession is Require without API tokens, for managing them.
func (a *Auth) requireSession(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		ok := r.Header.Get("Authorization") == "" && a.signedInLocked(r)
		a.mu.Unlock()
		if !ok {
			writeError(w, http.StatusForbidden, "API tokens are managed from a signed-in browser, not with a token")
			return
		}
		next(w, r)
	})
}

func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Routes registers the session endpoints, which need no session.
func (a *Auth) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/session", a.status)
	mux.HandleFunc("POST /api/v1/login", a.login)
	mux.HandleFunc("POST /api/v1/logout", a.logout)
	mux.Handle("GET /api/v1/tokens", a.requireSession(a.listTokens))
	mux.Handle("POST /api/v1/tokens", a.requireSession(a.createToken))
	mux.Handle("DELETE /api/v1/tokens/{id}", a.requireSession(a.revokeToken))
}

type statusBody struct {
	Mode          string     `json:"mode"`
	Authenticated bool       `json:"authenticated"`
	Expires       *time.Time `json:"expires_at,omitempty"`
}

func (a *Auth) status(w http.ResponseWriter, r *http.Request) {
	body := statusBody{Mode: a.Mode(), Authenticated: a.Allowed(r)}
	if body.Authenticated && body.Mode == "password" {
		a.mu.Lock()
		if s, ok := a.sessionLocked(r); ok {
			body.Expires = &s.Expires
		}
		a.mu.Unlock()
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, body)
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil || req.Password == "" {
		writeError(w, http.StatusBadRequest, `send {"password": "..."}`)
		return
	}
	a.mu.Lock()
	phc, gen := a.passwordLocked()
	a.mu.Unlock()
	if phc == "" {
		writeError(w, http.StatusConflict, "no password is set: run piway -set-password on the Pi")
		return
	}
	client := clientAddr(r)
	if wait := a.limiter.wait(client, a.opts.now()); wait > 0 {
		retryAfter(w, wait)
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("too many wrong passwords; try again in %v", wait.Round(time.Second)))
		return
	}

	ok, err := a.check(r.Context(), phc, req.Password)
	switch {
	case err != nil && r.Context().Err() != nil:
		return // the client went away while waiting
	case err != nil:
		a.opts.Logger.Error("verify password", "err", err)
		writeError(w, http.StatusInternalServerError, "the stored password hash is unreadable: run piway -set-password")
		return
	case !ok:
		wait := a.limiter.fail(client, a.opts.now())
		a.opts.Logger.Warn("wrong password", "client", client, "locked_for", wait)
		if wait > 0 {
			retryAfter(w, wait)
		}
		writeError(w, http.StatusUnauthorized, "wrong password")
		return
	}
	a.limiter.reset(client)

	token, err := newToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := a.opts.now()
	s := session{Created: now, Expires: now.Add(SessionTTL), Gen: gen}
	a.mu.Lock()
	a.sessions[tokenKey(token)] = s
	a.pruneLocked()
	err = a.saveLocked()
	a.mu.Unlock()
	if err != nil {
		a.opts.Logger.Error("save sessions", "err", err)
	}
	a.opts.Logger.Info("signed in", "client", client)
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/", Expires: s.Expires,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secure(r),
	})
	writeJSON(w, http.StatusOK, statusBody{Mode: "password", Authenticated: true, Expires: &s.Expires})
}

// check verifies one password at a time: each verification takes 64 MiB.
func (a *Auth) check(ctx context.Context, phc, password string) (bool, error) {
	select {
	case a.verify <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-a.verify }()
	return Verify(phc, password)
}

func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		a.mu.Lock()
		delete(a.sessions, tokenKey(c.Value))
		if err := a.saveLocked(); err != nil {
			a.opts.Logger.Error("save sessions", "err", err)
		}
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secure(r),
	})
	w.WriteHeader(http.StatusNoContent)
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// pruneLocked drops expired sessions and those of an old password.
func (a *Auth) pruneLocked() {
	_, gen := a.passwordLocked()
	now := a.opts.now()
	for k, s := range a.sessions {
		if !now.Before(s.Expires) || (gen != "" && s.Gen != gen) {
			delete(a.sessions, k)
		}
	}
}

// saveLocked writes the sessions file.
func (a *Auth) saveLocked() error { return writeFile(a.opts.SessionsFile, a.sessions) }

// writeFile writes v as JSON to path (mode 0600) in one rename; "" is a
// no-op.
func writeFile(path string, v any) error {
	if path == "" {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func clientKey(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func retryAfter(w http.ResponseWriter, d time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(d.Seconds()+0.999)))
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(append(b, '\n'))
}

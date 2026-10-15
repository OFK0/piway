package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const pw = "correct horse battery"

func TestHashAndVerify(t *testing.T) {
	h, err := Hash(pw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("hash = %q", h)
	}
	if h2, _ := Hash(pw); h2 == h {
		t.Error("two hashes of one password are equal: no salt")
	}
	if ok, err := Verify(h, pw); !ok || err != nil {
		t.Errorf("Verify(right) = %v, %v", ok, err)
	}
	if ok, _ := Verify(h, pw+"!"); ok {
		t.Error("Verify(wrong) = true")
	}
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA", "$argon2id$v=19$m=0,t=3,p=4$c2FsdA$aGFzaA", "$argon2id$v=19$m=65536,t=3,p=4$!!$aGFzaA"} {
		if _, err := Verify(bad, pw); err != ErrBadHash {
			t.Errorf("Verify(%q) err = %v, want ErrBadHash", bad, err)
		}
	}
}

func TestSetPassword(t *testing.T) {
	file := filepath.Join(t.TempDir(), "piway", "password")
	if err := SetPassword(file, "short"); err == nil {
		t.Error("accepted a short password")
	}
	if err := SetPassword(file, pw); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(file)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}
}

type env struct {
	a        *Auth
	pwFile   string
	sessions string
	tokens   string
	now      time.Time
	mu       sync.Mutex
}

func newEnv(t *testing.T, withPassword bool) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{pwFile: filepath.Join(dir, "password"), sessions: filepath.Join(dir, "state", "sessions.json"), now: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	if withPassword {
		if err := SetPassword(e.pwFile, pw); err != nil {
			t.Fatal(err)
		}
	}
	e.a = e.open(t)
	return e
}

func (e *env) open(t *testing.T) *Auth {
	t.Helper()
	a, err := New(Options{PasswordFile: e.pwFile, SessionsFile: e.sessions, TokensFile: e.tokens, now: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (e *env) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

// handler is a protected endpoint plus the session routes.
func handler(a *Auth) http.Handler {
	mux := http.NewServeMux()
	a.Routes(mux)
	mux.Handle("GET /api/v1/secret", a.Require(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("secret"))
	})))
	return mux
}

func do(h http.Handler, method, path, body, remote string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = remote
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

const lan = "192.0.2.7:51000"

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == CookieName && c.Value != "" {
			return c
		}
	}
	t.Fatalf("no session cookie: %d %s", w.Code, w.Body)
	return nil
}

func TestNoPasswordAllowsOnlyLoopback(t *testing.T) {
	e := newEnv(t, false)
	h := handler(e.a)
	for _, remote := range []string{"127.0.0.1:5000", "[::1]:5000"} {
		if w := do(h, "GET", "/api/v1/secret", "", remote, nil); w.Code != 200 {
			t.Errorf("%s: %d", remote, w.Code)
		}
	}
	w := do(h, "GET", "/api/v1/secret", "", lan, nil)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "-set-password") {
		t.Errorf("LAN without a password: %d %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/v1/login", `{"password":"x"}`, lan, nil); w.Code != http.StatusConflict {
		t.Errorf("login without a password: %d", w.Code)
	}
	if w := do(h, "GET", "/api/v1/session", "", lan, nil); !strings.Contains(w.Body.String(), `"mode":"local","authenticated":false`) {
		t.Errorf("session: %s", w.Body)
	}
}

func TestLoginLogout(t *testing.T) {
	e := newEnv(t, true)
	h := handler(e.a)
	// A password ends the loopback exception.
	if w := do(h, "GET", "/api/v1/secret", "", "127.0.0.1:5000", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("loopback with a password set: %d, want 401", w.Code)
	}
	if w := do(h, "POST", "/api/v1/login", `{"password":"nope"}`, lan, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: %d", w.Code)
	}
	w := do(h, "POST", "/api/v1/login", `{"password":"`+pw+`"}`, lan, nil)
	c := sessionCookie(t, w)
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Errorf("cookie = %+v", c)
	}
	if w := do(h, "GET", "/api/v1/secret", "", lan, c); w.Code != 200 || w.Body.String() != "secret" {
		t.Errorf("with the session: %d %s", w.Code, w.Body)
	}
	if w := do(h, "GET", "/api/v1/session", "", lan, c); !strings.Contains(w.Body.String(), `"authenticated":true,"expires_at":"2026-11-14T12:00:00Z"`) {
		t.Errorf("session: %s", w.Body)
	}
	if w := do(h, "GET", "/api/v1/secret", "", lan, &http.Cookie{Name: CookieName, Value: "forged"}); w.Code != http.StatusUnauthorized {
		t.Errorf("forged cookie: %d", w.Code)
	}

	if w := do(h, "POST", "/api/v1/logout", "", lan, c); w.Code != http.StatusNoContent {
		t.Errorf("logout: %d", w.Code)
	}
	if w := do(h, "GET", "/api/v1/secret", "", lan, c); w.Code != http.StatusUnauthorized {
		t.Errorf("after logout: %d", w.Code)
	}
}

func TestSessionsSurviveRestartAndExpire(t *testing.T) {
	e := newEnv(t, true)
	c := sessionCookie(t, do(handler(e.a), "POST", "/api/v1/login", `{"password":"`+pw+`"}`, lan, nil))

	data, _ := os.ReadFile(e.sessions)
	if strings.Contains(string(data), c.Value) {
		t.Error("the sessions file holds the token itself, not its hash")
	}
	if fi, _ := os.Stat(e.sessions); fi.Mode().Perm() != 0o600 {
		t.Errorf("sessions file mode %v", fi.Mode().Perm())
	}

	restarted := handler(e.open(t))
	if w := do(restarted, "GET", "/api/v1/secret", "", lan, c); w.Code != 200 {
		t.Errorf("after a restart: %d", w.Code)
	}
	e.advance(SessionTTL)
	if w := do(restarted, "GET", "/api/v1/secret", "", lan, c); w.Code != http.StatusUnauthorized {
		t.Errorf("after %v: %d", SessionTTL, w.Code)
	}
}

func TestChangingThePasswordSignsEveryoneOut(t *testing.T) {
	e := newEnv(t, true)
	h := handler(e.a)
	c := sessionCookie(t, do(h, "POST", "/api/v1/login", `{"password":"`+pw+`"}`, lan, nil))
	// Make sure the new file differs in modification time too.
	e.advance(time.Second)
	time.Sleep(10 * time.Millisecond)
	if err := SetPassword(e.pwFile, "another long password"); err != nil {
		t.Fatal(err)
	}
	if w := do(h, "GET", "/api/v1/secret", "", lan, c); w.Code != http.StatusUnauthorized {
		t.Errorf("old session after a password change: %d", w.Code)
	}
	if w := do(h, "POST", "/api/v1/login", `{"password":"another long password"}`, lan, nil); w.Code != 200 {
		t.Errorf("new password: %d", w.Code)
	}
}

func TestLockoutAfterWrongPasswords(t *testing.T) {
	e := newEnv(t, true)
	h := handler(e.a)
	wrong := `{"password":"wrong password"}`
	for i := 1; i < freeFails; i++ {
		if w := do(h, "POST", "/api/v1/login", wrong, lan, nil); w.Code != http.StatusUnauthorized || w.Header().Get("Retry-After") != "" {
			t.Fatalf("attempt %d: %d, Retry-After %q", i, w.Code, w.Header().Get("Retry-After"))
		}
	}
	if w := do(h, "POST", "/api/v1/login", wrong, lan, nil); w.Header().Get("Retry-After") != "30" {
		t.Errorf("attempt %d: Retry-After %q, want 30", freeFails, w.Header().Get("Retry-After"))
	}
	// Locked: even the right password is not checked.
	w := do(h, "POST", "/api/v1/login", `{"password":"`+pw+`"}`, lan, nil)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("while locked: %d", w.Code)
	}
	// Another client is not affected.
	if w := do(h, "POST", "/api/v1/login", `{"password":"`+pw+`"}`, "192.0.2.8:4000", nil); w.Code != 200 {
		t.Errorf("another client: %d", w.Code)
	}
	e.advance(31 * time.Second)
	if w := do(h, "POST", "/api/v1/login", wrong, lan, nil); w.Header().Get("Retry-After") != "60" {
		t.Errorf("next failure: Retry-After %q, want 60 (doubled)", w.Header().Get("Retry-After"))
	}
	e.advance(61 * time.Second)
	sessionCookie(t, do(h, "POST", "/api/v1/login", `{"password":"`+pw+`"}`, lan, nil))
}

func TestBadLoginRequests(t *testing.T) {
	h := handler(newEnv(t, true).a)
	for _, body := range []string{"", "{", `{"password":""}`, `{"password":"` + strings.Repeat("x", 5000) + `"}`} {
		if w := do(h, "POST", "/api/v1/login", body, lan, nil); w.Code != http.StatusBadRequest {
			t.Errorf("body %.20q: %d, want 400", body, w.Code)
		}
	}
}

func TestLimiterMemoryIsBounded(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	for i := range maxClients + 100 {
		l.fail(string(rune(i)), now)
	}
	if len(l.clients) > maxClients {
		t.Errorf("%d clients tracked, max %d", len(l.clients), maxClients)
	}
}

func doHeaders(h http.Handler, method, path, body, remote string, header map[string]string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = remote
	for k, v := range header {
		req.Header.Set(k, v)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestProxiedRequestsAreNotLocal(t *testing.T) {
	h := handler(newEnv(t, false).a)
	// "tailscale serve" or Caddy on the Pi: the connection is from loopback.
	for _, header := range []map[string]string{
		{"X-Forwarded-For": "100.64.0.7"},
		{"Forwarded": "for=100.64.0.7"},
		{"X-Real-Ip": "100.64.0.7"},
		{"Tailscale-User-Login": "someone@example.com"},
	} {
		if w := doHeaders(h, "GET", "/api/v1/secret", "", "127.0.0.1:4000", header, nil); w.Code != http.StatusForbidden {
			t.Errorf("proxied with %v and no password: %d, want 403", header, w.Code)
		}
	}
}

func TestBehindALocalProxy(t *testing.T) {
	e := newEnv(t, true)
	h := handler(e.a)
	proxy := "127.0.0.1:4000"
	viaTLS := map[string]string{"X-Forwarded-For": "100.64.0.7", "X-Forwarded-Proto": "https"}
	c := sessionCookie(t, doHeaders(h, "POST", "/api/v1/login", `{"password":"`+pw+`"}`, proxy, viaTLS, nil))
	if !c.Secure {
		t.Error("cookie not Secure behind a proxy that terminated TLS")
	}
	// A client cannot claim HTTPS or another address by itself.
	direct := sessionCookie(t, doHeaders(h, "POST", "/api/v1/login", `{"password":"`+pw+`"}`, lan, map[string]string{"X-Forwarded-Proto": "https"}, nil))
	if direct.Secure {
		t.Error("a remote client's X-Forwarded-Proto was trusted")
	}

	// Lockouts are per client behind the proxy, not one for everyone.
	wrong := `{"password":"wrong password"}`
	for range freeFails {
		doHeaders(h, "POST", "/api/v1/login", wrong, proxy, map[string]string{"X-Forwarded-For": "100.64.0.7"}, nil)
	}
	if w := doHeaders(h, "POST", "/api/v1/login", `{"password":"`+pw+`"}`, proxy, map[string]string{"X-Forwarded-For": "100.64.0.7"}, nil); w.Code != http.StatusTooManyRequests {
		t.Errorf("locked client: %d", w.Code)
	}
	if w := doHeaders(h, "POST", "/api/v1/login", `{"password":"`+pw+`"}`, proxy, map[string]string{"X-Forwarded-For": "100.64.0.8"}, nil); w.Code != 200 {
		t.Errorf("another client behind the same proxy: %d", w.Code)
	}
	// A spoofed X-Forwarded-For from the LAN does not dodge its own lockout.
	for range freeFails {
		doHeaders(h, "POST", "/api/v1/login", wrong, lan, nil, nil)
	}
	if w := doHeaders(h, "POST", "/api/v1/login", `{"password":"`+pw+`"}`, lan, map[string]string{"X-Forwarded-For": "100.64.0.9"}, nil); w.Code != http.StatusTooManyRequests {
		t.Errorf("LAN client with a forged X-Forwarded-For: %d, want 429", w.Code)
	}
}

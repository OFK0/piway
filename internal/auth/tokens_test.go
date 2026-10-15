package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func withBearer(h http.Handler, method, path, body, remote, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = remote
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

type created struct {
	Token
	Secret string `json:"token"`
}

func createToken(t *testing.T, h http.Handler, name string, c *http.Cookie) created {
	t.Helper()
	w := do(h, "POST", "/api/v1/tokens", `{"name":"`+name+`"}`, lan, c)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var out created
	json.Unmarshal(w.Body.Bytes(), &out)
	return out
}

func tokensEnv(t *testing.T) (*env, http.Handler, *http.Cookie) {
	t.Helper()
	e := newEnv(t, true)
	e.tokens = filepath.Join(filepath.Dir(e.sessions), "tokens.json")
	e.a = e.open(t)
	h := handler(e.a)
	c := sessionCookie(t, do(h, "POST", "/api/v1/login", `{"password":"`+pw+`"}`, lan, nil))
	return e, h, c
}

func TestAPITokens(t *testing.T) {
	e, h, c := tokensEnv(t)
	tok := createToken(t, h, "backup script", c)
	if !strings.HasPrefix(tok.Secret, TokenPrefix) || len(tok.Secret) < 40 || tok.Hint != tok.Secret[:10] || tok.ID == "" {
		t.Errorf("created %+v", tok)
	}

	if w := withBearer(h, "GET", "/api/v1/secret", "", lan, tok.Secret); w.Code != 200 {
		t.Errorf("with the token: %d", w.Code)
	}
	for _, bad := range []string{"piway_forged", "not-a-piway-token", tok.Secret + "x"} {
		if w := withBearer(h, "GET", "/api/v1/secret", "", lan, bad); w.Code != http.StatusUnauthorized {
			t.Errorf("token %q: %d", bad, w.Code)
		}
	}

	// The list never shows the secret or its hash.
	w := do(h, "GET", "/api/v1/tokens", "", lan, c)
	if strings.Contains(w.Body.String(), tok.Secret) || strings.Contains(w.Body.String(), tokenKey(tok.Secret)) || !strings.Contains(w.Body.String(), `"last_used_at"`) {
		t.Errorf("list: %s", w.Body)
	}
	data, _ := os.ReadFile(e.tokens)
	if strings.Contains(string(data), tok.Secret) {
		t.Error("the tokens file holds the token itself")
	}

	// A token cannot manage tokens.
	for _, r := range []struct{ method, path string }{{"GET", "/api/v1/tokens"}, {"POST", "/api/v1/tokens"}, {"DELETE", "/api/v1/tokens/" + tok.ID}} {
		if w := withBearer(h, r.method, r.path, `{"name":"more"}`, lan, tok.Secret); w.Code != http.StatusForbidden {
			t.Errorf("%s %s with a token: %d, want 403", r.method, r.path, w.Code)
		}
	}

	if w := do(h, "DELETE", "/api/v1/tokens/"+tok.ID, "", lan, c); w.Code != http.StatusNoContent {
		t.Errorf("revoke: %d", w.Code)
	}
	if w := withBearer(h, "GET", "/api/v1/secret", "", lan, tok.Secret); w.Code != http.StatusUnauthorized {
		t.Errorf("revoked token: %d", w.Code)
	}
	if w := do(h, "DELETE", "/api/v1/tokens/"+tok.ID, "", lan, c); w.Code != http.StatusNotFound {
		t.Errorf("revoke twice: %d", w.Code)
	}
}

func TestTokensOutliveRestartsAndPasswordChanges(t *testing.T) {
	e, h, c := tokensEnv(t)
	tok := createToken(t, h, "ci", c)
	e.advance(2 * lastUsedEvery)
	withBearer(h, "GET", "/api/v1/secret", "", lan, tok.Secret)
	e.a.Close()

	e.advance(time.Second)
	time.Sleep(10 * time.Millisecond)
	if err := SetPassword(e.pwFile, "a brand new password"); err != nil {
		t.Fatal(err)
	}
	restarted := e.open(t)
	h2 := handler(restarted)
	if w := withBearer(h2, "GET", "/api/v1/secret", "", lan, tok.Secret); w.Code != 200 {
		t.Errorf("token after a restart and a password change: %d", w.Code)
	}
	if restarted.tokens[0].LastUsed == nil {
		t.Error("last use not saved")
	}
}

func TestBadTokenIsRefusedEvenFromLoopback(t *testing.T) {
	e := newEnv(t, false) // no password: loopback is trusted
	h := handler(e.a)
	if w := withBearer(h, "GET", "/api/v1/secret", "", "127.0.0.1:1", "piway_forged"); w.Code == 200 {
		t.Error("a bad token was let through because the request is local")
	}
	// Without a password, tokens are made from the Pi itself and work from anywhere.
	w := do(h, "POST", "/api/v1/tokens", `{"name":"local"}`, "127.0.0.1:1", nil)
	var tok created
	json.Unmarshal(w.Body.Bytes(), &tok)
	if w.Code != http.StatusCreated {
		t.Fatalf("create from loopback: %d", w.Code)
	}
	if w := withBearer(h, "GET", "/api/v1/secret", "", lan, tok.Secret); w.Code != 200 {
		t.Errorf("token from the LAN without a password: %d", w.Code)
	}
	if w := do(h, "POST", "/api/v1/tokens", `{"name":"x"}`, lan, nil); w.Code != http.StatusForbidden {
		t.Errorf("create from the LAN without a session: %d", w.Code)
	}
}

func TestTokenNamesAndLimit(t *testing.T) {
	_, h, c := tokensEnv(t)
	for _, body := range []string{`{}`, `{"name":"   "}`, `{"name":"` + strings.Repeat("x", 65) + `"}`, `not json`} {
		if w := do(h, "POST", "/api/v1/tokens", body, lan, c); w.Code != http.StatusBadRequest {
			t.Errorf("body %.20q: %d", body, w.Code)
		}
	}
	for i := range maxTokens {
		createToken(t, h, fmt.Sprint("t", i), c)
	}
	if w := do(h, "POST", "/api/v1/tokens", `{"name":"one more"}`, lan, c); w.Code != http.StatusConflict {
		t.Errorf("token %d: %d, want 409", maxTokens+1, w.Code)
	}
}

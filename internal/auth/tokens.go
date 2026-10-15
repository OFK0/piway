package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// API tokens let scripts and the MCP server (M6) use the API without a
// browser: "Authorization: Bearer piway_…". Like session tokens, only
// their SHA-256 hash is stored. They do not depend on the password, so
// changing it does not break automation; they end only when revoked.
// Managing them needs a browser session: a leaked token cannot mint more.

// TokenPrefix starts every API token, so secret scanners can find leaks.
const TokenPrefix = "piway_"

const (
	maxTokens       = 50
	maxTokenName    = 64
	lastUsedEvery   = time.Minute // how often last-used times reach the disk
	tokenRandomSize = 32
)

// Token describes an API token. The token itself is shown only once.
type Token struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Hint     string     `json:"hint"` // the first characters, to tell tokens apart
	Created  time.Time  `json:"created_at"`
	LastUsed *time.Time `json:"last_used_at,omitempty"`
	hash     string
}

// storedToken is a Token on disk.
type storedToken struct {
	Token
	Hash string `json:"hash"`
}

func (a *Auth) loadTokens() error {
	if a.opts.TokensFile == "" {
		return nil
	}
	b, err := os.ReadFile(a.opts.TokensFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var stored []storedToken
	if err := json.Unmarshal(b, &stored); err != nil {
		return err // unlike sessions, silently dropping tokens would break automation
	}
	for _, s := range stored {
		t := s.Token
		t.hash = s.Hash
		a.tokens = append(a.tokens, &t)
	}
	return nil
}

// saveTokensLocked writes the tokens file (mode 0600) in one rename.
func (a *Auth) saveTokensLocked() error {
	stored := make([]storedToken, 0, len(a.tokens))
	for _, t := range a.tokens {
		stored = append(stored, storedToken{Token: *t, Hash: t.hash})
	}
	a.tokensSaved = a.opts.now()
	return writeFile(a.opts.TokensFile, stored)
}

// tokenLocked returns the token r carries in its Authorization header:
// ok reports a valid one, present any attempt at one.
func (a *Auth) tokenLocked(r *http.Request) (t *Token, present bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return nil, false
	}
	scheme, value, _ := strings.Cut(h, " ")
	if !strings.EqualFold(scheme, "Bearer") || !strings.HasPrefix(value, TokenPrefix) {
		return nil, true
	}
	hash := tokenKey(value)
	for _, t := range a.tokens {
		if t.hash == hash {
			now := a.opts.now()
			t.LastUsed = &now
			if now.Sub(a.tokensSaved) >= lastUsedEvery {
				if err := a.saveTokensLocked(); err != nil {
					a.opts.Logger.Error("save tokens", "err", err)
				}
			}
			return t, true
		}
	}
	return nil, true
}

func (a *Auth) listTokens(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	out := make([]Token, 0, len(a.tokens))
	for _, t := range slices.Backward(a.tokens) { // newest first
		out = append(out, *t)
	}
	a.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

func (a *Auth) createToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, `send {"name": "..."}`)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > maxTokenName {
		writeError(w, http.StatusBadRequest, "name the token (1 to 64 characters), such as what will use it")
		return
	}
	raw := make([]byte, tokenRandomSize)
	id := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rand.Read(id)
	secret := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	t := &Token{ID: hex.EncodeToString(id), Name: name, Hint: secret[:len(TokenPrefix)+4], Created: a.opts.now(), hash: tokenKey(secret)}

	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.tokens) >= maxTokens {
		writeError(w, http.StatusConflict, "too many tokens: revoke some first")
		return
	}
	a.tokens = append(a.tokens, t)
	if err := a.saveTokensLocked(); err != nil {
		a.tokens = a.tokens[:len(a.tokens)-1]
		writeError(w, http.StatusInternalServerError, "save tokens: "+err.Error())
		return
	}
	a.opts.Logger.Info("API token created", "id", t.ID, "name", t.Name)
	writeJSON(w, http.StatusCreated, struct {
		Token
		Secret string `json:"token"`
	}{*t, secret})
}

func (a *Auth) revokeToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a.mu.Lock()
	defer a.mu.Unlock()
	i := slices.IndexFunc(a.tokens, func(t *Token) bool { return t.ID == id })
	if i < 0 {
		writeError(w, http.StatusNotFound, "no token with that id")
		return
	}
	t := a.tokens[i]
	a.tokens = slices.Delete(a.tokens, i, i+1)
	if err := a.saveTokensLocked(); err != nil {
		a.tokens = slices.Insert(a.tokens, i, t)
		writeError(w, http.StatusInternalServerError, "save tokens: "+err.Error())
		return
	}
	a.opts.Logger.Info("API token revoked", "id", t.ID, "name", t.Name)
	w.WriteHeader(http.StatusNoContent)
}

package push

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// browser is a subscriber: its keys and the push service it uses.
type browser struct {
	priv *ecdh.PrivateKey
	auth []byte
}

func newBrowser(t *testing.T) browser {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	return browser{k, auth}
}

func (b browser) keys() (p256dh, auth string) {
	return base64.RawURLEncoding.EncodeToString(b.priv.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(b.auth)
}

// decrypt is the browser's side of RFC 8291.
func (b browser) decrypt(t *testing.T, body []byte) []byte {
	t.Helper()
	salt, idlen := body[:16], int(body[20])
	asPub, ct := body[21:21+idlen], body[21+idlen:]
	as, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := b.priv.ECDH(as)
	info := append(append([]byte("WebPush: info\x00"), b.priv.PublicKey().Bytes()...), asPub...)
	ikm, _ := hkdf.Key(sha256.New, shared, b.auth, string(info), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if plain[len(plain)-1] != 2 {
		t.Fatalf("no last-record delimiter")
	}
	return plain[:len(plain)-1]
}

// pushService is a fake FCM: it records what arrives and answers status.
type pushService struct {
	srv    *httptest.Server
	mu     sync.Mutex
	status int
	got    []*http.Request
	bodies [][]byte
}

func newPushService(t *testing.T) *pushService {
	ps := &pushService{status: http.StatusCreated}
	ps.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ps.mu.Lock()
		ps.got, ps.bodies = append(ps.got, r), append(ps.bodies, b)
		status := ps.status
		ps.mu.Unlock()
		if status == http.StatusTemporaryRedirect {
			http.Redirect(w, r, "https://elsewhere.invalid/", status)
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(ps.srv.Close)
	return ps
}

// client reaches the fake service for any host name; its certificate is
// for example.com.
func (ps *pushService) client() *http.Client {
	c := ps.srv.Client()
	tr := c.Transport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, ps.srv.Listener.Addr().String())
	}
	c.Transport = tr
	return c
}

func (ps *pushService) setStatus(code int) {
	ps.mu.Lock()
	ps.status = code
	ps.mu.Unlock()
}

func (ps *pushService) count() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return len(ps.got)
}

func (ps *pushService) last() (*http.Request, []byte) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if len(ps.got) == 0 {
		return nil, nil
	}
	return ps.got[len(ps.got)-1], ps.bodies[len(ps.bodies)-1]
}

func newService(t *testing.T, dir string, ps *pushService) *Service {
	t.Helper()
	s, err := New(Options{Dir: dir, Subject: "https://example.org/contact", Client: ps.client(), AllowedHosts: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func call(s *Service, method, path, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	s.Routes(mux, func(h http.Handler) http.Handler { return h })
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	return w
}

func subscribe(t *testing.T, s *Service, b browser, endpoint, label string) Subscription {
	t.Helper()
	p, a := b.keys()
	body := fmt.Sprintf(`{"subscription":{"endpoint":%q,"keys":{"p256dh":%q,"auth":%q}},"label":%q}`, endpoint, p, a, label)
	w := call(s, "POST", "/api/v1/push/subscriptions", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("subscribe: %d %s", w.Code, w.Body)
	}
	var sub Subscription
	json.Unmarshal(w.Body.Bytes(), &sub)
	return sub
}

func TestDeliversEncryptedSignedPushes(t *testing.T) {
	ps := newPushService(t)
	s := newService(t, t.TempDir(), ps)
	b := newBrowser(t)
	sub := subscribe(t, s, b, "https://example.com/fcm/send/abc", "Phone")
	if sub.Service != "example.com" || sub.Label != "Phone" || sub.ID == "" {
		t.Errorf("subscription %+v", sub)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	s.Notify(Message{Title: "CPU is hot", Body: "CPU is hot: 80 °C (> 75 °C)", Tag: "alert:x", URL: "/", Urgent: true})
	var req *http.Request
	var body []byte
	for deadline := time.Now().Add(5 * time.Second); req == nil && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		req, body = ps.last()
	}
	cancel()
	<-done
	if req == nil {
		t.Fatal("nothing arrived")
	}

	var m map[string]string
	if err := json.Unmarshal(b.decrypt(t, body), &m); err != nil || m["title"] != "CPU is hot" || m["tag"] != "alert:x" || m["url"] != "/" {
		t.Errorf("payload %v, %v", m, err)
	}
	h := req.Header
	if h.Get("Content-Encoding") != "aes128gcm" || h.Get("TTL") != "86400" || h.Get("Urgency") != "high" || len(h.Get("Topic")) != 32 {
		t.Errorf("headers %v", h)
	}
	// Authorization: vapid t=<jwt>, k=<key>, and the JWT verifies with k.
	authz := strings.TrimPrefix(h.Get("Authorization"), "vapid ")
	tok, key, _ := strings.Cut(authz, ", k=")
	tok = strings.TrimPrefix(tok, "t=")
	if key != s.PublicKey() || !verifyJWT(t, tok, key) {
		t.Errorf("Authorization %q does not verify", h.Get("Authorization"))
	}
	parts := strings.Split(tok, ".")
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Aud string `json:"aud"`
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
	}
	json.Unmarshal(claims, &c)
	if c.Aud != "https://example.com" || c.Sub != "https://example.org/contact" || c.Exp <= time.Now().Unix() || c.Exp > time.Now().Add(24*time.Hour).Unix() {
		t.Errorf("claims %+v", c)
	}
}

func verifyJWT(t *testing.T, token, key string) bool {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil {
		return false
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), raw) //nolint:staticcheck // fine for a test
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	return ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
}

func TestGoneSubscriptionsAreRemovedAndErrorsKept(t *testing.T) {
	ps := newPushService(t)
	dir := t.TempDir()
	s := newService(t, dir, ps)
	gone := subscribe(t, s, newBrowser(t), "https://example.com/a", "Old phone")
	subscribe(t, s, newBrowser(t), "https://example.com/b", "Laptop")

	ps.setStatus(http.StatusGone)
	w := call(s, "POST", "/api/v1/push/test", `{"id":"`+gone.ID+`"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "removed") {
		t.Errorf("test to a gone subscription: %d %s", w.Code, w.Body)
	}
	ps.setStatus(http.StatusInternalServerError)
	call(s, "POST", "/api/v1/push/test", "")

	reloaded := newService(t, dir, ps)
	var st struct{ Subscriptions []Subscription }
	json.Unmarshal(call(reloaded, "GET", "/api/v1/push", "").Body.Bytes(), &st)
	if len(st.Subscriptions) != 1 || st.Subscriptions[0].Label != "Laptop" || !strings.Contains(st.Subscriptions[0].LastError, "500") {
		t.Errorf("after 410 and 500, then a restart: %+v", st.Subscriptions)
	}
	if strings.Contains(call(reloaded, "GET", "/api/v1/push", "").Body.String(), "example.com/b") {
		t.Error("the API shows the endpoint, which is a capability")
	}
	if w := call(reloaded, "POST", "/api/v1/push/test", `{"id":"nope"}`); w.Code != http.StatusNotFound {
		t.Errorf("test to an unknown id: %d", w.Code)
	}
}

func TestSubscribeValidation(t *testing.T) {
	s := newService(t, t.TempDir(), newPushService(t))
	p, a := newBrowser(t).keys()
	bad := map[string]string{
		"plain http":        `{"subscription":{"endpoint":"http://example.com/x","keys":{"p256dh":"` + p + `","auth":"` + a + `"}}}`,
		"unknown service":   `{"subscription":{"endpoint":"https://evil.example.net/x","keys":{"p256dh":"` + p + `","auth":"` + a + `"}}}`,
		"lookalike host":    `{"subscription":{"endpoint":"https://notexample.com/x","keys":{"p256dh":"` + p + `","auth":"` + a + `"}}}`,
		"other port":        `{"subscription":{"endpoint":"https://example.com:8443/x","keys":{"p256dh":"` + p + `","auth":"` + a + `"}}}`,
		"address":           `{"subscription":{"endpoint":"https://127.0.0.1/x","keys":{"p256dh":"` + p + `","auth":"` + a + `"}}}`,
		"user info":         `{"subscription":{"endpoint":"https://u:p@example.com/x","keys":{"p256dh":"` + p + `","auth":"` + a + `"}}}`,
		"bad key":           `{"subscription":{"endpoint":"https://example.com/x","keys":{"p256dh":"AAAA","auth":"` + a + `"}}}`,
		"short auth secret": `{"subscription":{"endpoint":"https://example.com/x","keys":{"p256dh":"` + p + `","auth":"AAAA"}}}`,
		"not json":          `nope`,
	}
	for name, body := range bad {
		if w := call(s, "POST", "/api/v1/push/subscriptions", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}

	b := newBrowser(t)
	first := subscribe(t, s, b, "https://sub.example.com/x", "")
	again := subscribe(t, s, b, "https://sub.example.com/x", "Renamed")
	if first.ID != again.ID || first.Label != "Unnamed device" || again.Label != "Renamed" {
		t.Errorf("resubscribe: %+v then %+v", first, again)
	}
	if w := call(s, "DELETE", "/api/v1/push/subscriptions/"+first.ID, ""); w.Code != http.StatusNoContent {
		t.Errorf("delete: %d", w.Code)
	}
	if w := call(s, "DELETE", "/api/v1/push/subscriptions/"+first.ID, ""); w.Code != http.StatusNotFound {
		t.Errorf("delete twice: %d", w.Code)
	}
	for i := range maxSubscriptions {
		subscribe(t, s, newBrowser(t), fmt.Sprintf("https://example.com/%d", i), "")
	}
	pp, aa := newBrowser(t).keys()
	if w := call(s, "POST", "/api/v1/push/subscriptions", `{"subscription":{"endpoint":"https://example.com/more","keys":{"p256dh":"`+pp+`","auth":"`+aa+`"}}}`); w.Code != http.StatusConflict {
		t.Errorf("subscription %d: %d", maxSubscriptions+1, w.Code)
	}
}

func TestVAPIDKeyPersists(t *testing.T) {
	dir := t.TempDir()
	ps := newPushService(t)
	a := newService(t, dir, ps)
	b := newService(t, dir, ps)
	if a.PublicKey() != b.PublicKey() || len(a.PublicKey()) != 87 { // 65 bytes, base64url
		t.Errorf("keys %q and %q", a.PublicKey(), b.PublicKey())
	}
	if fi, err := os.Stat(filepath.Join(dir, "vapid.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("vapid.json: %v, %v", fi.Mode().Perm(), err)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	ps := newPushService(t)
	ps.setStatus(http.StatusTemporaryRedirect)
	s, err := New(Options{Dir: t.TempDir(), AllowedHosts: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	s.opts.Client.Transport = ps.client().Transport // the default client's redirect policy, the fake's network
	subscribe(t, s, newBrowser(t), "https://example.com/x", "")
	w := call(s, "POST", "/api/v1/push/test", "")
	if !strings.Contains(w.Body.String(), "307") {
		t.Errorf("redirect: %s", w.Body)
	}
	if n := ps.count(); n != 1 {
		t.Errorf("%d requests: the redirect was followed", n)
	}
}

func TestPayloadLimit(t *testing.T) {
	b := newBrowser(t)
	if _, err := encrypt(make([]byte, maxPayload+1), b.priv.PublicKey().Bytes(), b.auth, rand.Reader); err == nil {
		t.Error("accepted a payload that does not fit one record")
	}
	out, err := encrypt(make([]byte, maxPayload), b.priv.PublicKey().Bytes(), b.auth, rand.Reader)
	if err != nil || len(out) > 4096 {
		t.Errorf("largest payload: %d bytes, %v", len(out), err)
	}
}

package push

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"
)

// VAPID (RFC 8292) identifies piway to push services: each request carries
// a JWT signed with piway's key, and browsers only accept pushes signed by
// the key they subscribed with. The key pair is made on first start and
// kept in the data directory; replacing it invalidates every subscription.

type vapidFile struct {
	Private string `json:"private"` // the P-256 scalar, base64url
}

type vapid struct {
	key *ecdsa.PrivateKey
	pub string // uncompressed point, base64url: the applicationServerKey
}

func loadOrCreateVAPID(path string) (*vapid, bool, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		var f vapidFile
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, false, fmt.Errorf("%s: %w", path, err)
		}
		d, err := base64.RawURLEncoding.DecodeString(f.Private)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", path, err)
		}
		key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), d)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", path, err)
		}
		return newVAPID(key), false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, false, err
	}
	d, err := key.Bytes()
	if err != nil {
		return nil, false, err
	}
	if err := writeJSON(path, vapidFile{Private: base64.RawURLEncoding.EncodeToString(d)}); err != nil {
		return nil, false, err
	}
	return newVAPID(key), true, nil
}

func newVAPID(key *ecdsa.PrivateKey) *vapid {
	pub, _ := key.PublicKey.Bytes() // uncompressed, 65 bytes
	return &vapid{key: key, pub: base64.RawURLEncoding.EncodeToString(pub)}
}

// authorization is the Authorization header for a push to endpoint:
// "vapid t=<JWT>, k=<public key>". subject is a mailto: or https: URL push
// services can use to reach the sender.
func (v *vapid) authorization(endpoint, subject string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{
		"aud": u.Scheme + "://" + u.Host,
		"exp": now.Add(12 * time.Hour).Unix(), // at most 24 h (RFC 8292)
		"sub": subject,
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, v.key, sum[:])
	if err != nil {
		return "", err
	}
	// JWS ES256: r and s as 32-byte big-endian integers, concatenated.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + signing + "." + enc.EncodeToString(sig) + ", k=" + v.pub, nil
}

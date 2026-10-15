package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// recordSize is the aes128gcm record size piway declares. A notification
// is one record, so it only has to be larger than the payload.
const recordSize = 4096

// maxPayload keeps a message within one record and within what push
// services accept (4096 bytes after encryption, RFC 8030).
const maxPayload = recordSize - 16 - 1 - 86 // tag, delimiter, header

// encrypt encrypts plaintext for a subscriber (RFC 8291, aes128gcm from
// RFC 8188) with the subscriber's P-256 public key and auth secret. rnd
// supplies the ephemeral key and salt; tests pass fixed bytes.
func encrypt(plaintext []byte, uaPublic, authSecret []byte, rnd io.Reader) ([]byte, error) {
	if len(plaintext) > maxPayload {
		return nil, fmt.Errorf("push: payload of %d bytes is over %d", len(plaintext), maxPayload)
	}
	if len(authSecret) != 16 {
		return nil, errors.New("push: auth secret must be 16 bytes")
	}
	curve := ecdh.P256()
	ua, err := curve.NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("push: subscriber key: %w", err)
	}
	as, err := curve.GenerateKey(rnd)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rnd, salt); err != nil {
		return nil, err
	}
	return encryptWith(plaintext, ua, as, authSecret, salt)
}

func encryptWith(plaintext []byte, ua *ecdh.PublicKey, as *ecdh.PrivateKey, authSecret, salt []byte) ([]byte, error) {
	shared, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPublic := as.PublicKey().Bytes()

	// RFC 8291 section 3.4: combine the ECDH secret with the auth secret.
	keyInfo := append(append([]byte("WebPush: info\x00"), ua.Bytes()...), asPublic...)
	ikm, err := hkdf.Key(sha256.New, shared, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	// RFC 8188 section 2.2: the content encryption key and nonce.
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// Header: salt, record size, key id length, key id (our public key).
	out := make([]byte, 0, 16+4+1+len(asPublic)+len(plaintext)+1+gcm.Overhead())
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, recordSize)
	out = append(out, byte(len(asPublic)))
	out = append(out, asPublic...)
	// One record, so it is the last: padding delimiter 0x02.
	return gcm.Seal(out, nonce, append(plaintext, 2), nil), nil
}

// randReader is crypto/rand outside tests.
var randReader io.Reader = rand.Reader

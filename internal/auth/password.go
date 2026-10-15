package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters: RFC 9106's second recommended option (64 MiB, 3
// passes, 4 lanes). About 0.3 s on a Raspberry Pi 4, so guessing is slow,
// and logins are verified one at a time so the memory stays bounded.
const (
	argonMemory  = 64 * 1024 // KiB
	argonTime    = 3
	argonThreads = 4
	argonKeyLen  = 32
	saltLen      = 16
)

// MinPasswordLen is the shortest password SetPassword accepts.
const MinPasswordLen = 10

var b64 = base64.RawStdEncoding

// Hash returns password as an argon2id PHC string:
// $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>.
func Hash(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// ErrBadHash means a stored hash is not an argon2id PHC string.
var ErrBadHash = errors.New("not an argon2id password hash")

// Verify reports whether password matches the PHC string phc. The
// parameters come from phc, so hashes made with older ones still verify.
func Verify(phc, password string) (bool, error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, ErrBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrBadHash
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || t == 0 || p == 0 || m > 1<<20 {
		return false, ErrBadHash
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, ErrBadHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, ErrBadHash
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// SetPassword hashes password and writes it to file (mode 0600), replacing
// the file in one rename. A running piway picks it up at the next request
// and signs every session out.
func SetPassword(file, password string) error {
	if len([]rune(password)) < MinPasswordLen {
		return fmt.Errorf("the password must be at least %d characters", MinPasswordLen)
	}
	phc, err := Hash(password)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".password-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op after the rename
	if _, err := tmp.WriteString(phc + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file) // CreateTemp makes it 0600
}

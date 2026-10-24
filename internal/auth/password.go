// Package auth implements admin passwords and sessions, and the Steam sign-in of players.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	iterations = 600_000 // OWASP recommendation for PBKDF2-HMAC-SHA256
	saltLen    = 16
	keyLen     = 32
	// MinPasswordLen is the shortest accepted password.
	MinPasswordLen = 8
)

var b64 = base64.RawStdEncoding

// HashPassword returns "pbkdf2-sha256$<iterations>$<salt>$<hash>".
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, keyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", iterations, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// CheckPassword reports whether password matches a hash made by HashPassword.
func CheckPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false
	}
	salt, err1 := b64.DecodeString(parts[2])
	want, err2 := b64.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// ValidatePassword checks the password policy.
func ValidatePassword(password string) error {
	if utf8.RuneCountInString(password) < MinPasswordLen {
		return fmt.Errorf("password must be at least %d characters long", MinPasswordLen)
	}
	return nil
}

// ValidateUsername checks a user name.
func ValidateUsername(name string) error {
	if name == "" || utf8.RuneCountInString(name) > 64 || strings.TrimSpace(name) != name {
		return errors.New("username must be 1-64 characters without leading or trailing spaces")
	}
	return nil
}

// NewToken returns a random session token and the hash under which it is stored.
func NewToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken hashes a session token for storage, so a leaked database does not leak sessions.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

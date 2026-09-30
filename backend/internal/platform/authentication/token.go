package authentication

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

const (
	tokenBytes  = 32
	tokenLength = 43 // base64.RawURLEncoding length of 32 bytes
)

// newToken returns a fresh random session token. The raw token is handed to
// the client once; only its hash is stored.
func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken returns the SHA-256 digest under which a token is stored.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// validTokenFormat reports whether token could have been issued by newToken.
func validTokenFormat(token string) bool {
	if len(token) != tokenLength {
		return false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(token)
	return err == nil && len(b) == tokenBytes
}

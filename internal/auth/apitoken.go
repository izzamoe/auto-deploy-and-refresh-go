package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// TokenPrefix starts every API token value.
const TokenPrefix = "adt_"

// tokenDisplayPrefixLen is how many characters after TokenPrefix are kept for display.
const tokenDisplayPrefixLen = 8

// GenerateToken returns a new full token (`adt_` + 32 random bytes, base64url),
// its sha256 hex (what is stored) and the display prefix (D7).
func GenerateToken() (token, hash, prefix string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", "", fmt.Errorf("auth: generate token: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(b)
	token = TokenPrefix + body
	return token, HashToken(token), body[:tokenDisplayPrefixLen], nil
}

// HashToken returns the sha256 hex of a full token. Tokens are random with high
// entropy, so an unsalted fast hash is appropriate (same as webhook secrets).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

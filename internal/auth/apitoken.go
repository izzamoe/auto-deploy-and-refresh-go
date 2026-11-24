package auth

// TokenPrefix starts every API token value.
const TokenPrefix = "adt_"

// GenerateToken returns a new full token, its sha256 hex and display prefix.
func GenerateToken() (token, hash, prefix string, err error) { return "", "", "", errNotImplemented }

// HashToken returns the sha256 hex of a full token.
func HashToken(token string) string { return "" }

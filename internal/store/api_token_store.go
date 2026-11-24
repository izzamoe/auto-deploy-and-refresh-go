package store

import (
	"errors"
	"time"
)

// ErrAPITokenNotFound is returned when no active token matches.
var ErrAPITokenNotFound = errors.New("api token not found")

// APIToken is one row of api_tokens (data-model §1.3). Only the sha256 of the
// full token is stored; the full value is shown once, at creation.
type APIToken struct {
	ID         string
	UserID     string
	Name       string
	TokenHash  string
	Prefix     string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

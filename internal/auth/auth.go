// Package auth implements admin authentication: persistent-key signed sessions
// backed by a server-side session table, API tokens for automation, CSRF checks,
// rate limiting and trusted-proxy client IP resolution.
//
// The package must not import internal/admin; shared HTTP helpers live in
// internal/httpx.
package auth

import (
	"errors"
	"time"

	"github.com/izzamoe/auto-deploy/internal/store"
)

// errNotImplemented is returned by the compiled skeleton (T025) so that the RED
// tests fail at run time, not at build time. It disappears as each piece lands.
var errNotImplemented = errors.New("auth: not implemented")

// UserSource loads admin users by stable ID.
type UserSource interface {
	// GetByID returns store.ErrAdminUserNotFound when the user does not exist.
	GetByID(id string) (*store.AdminUser, error)
}

// SessionSource persists server-side session rows.
type SessionSource interface {
	Create(s *store.AdminSession) error
	// Get returns store.ErrAdminSessionNotFound when the row does not exist.
	Get(id string) (*store.AdminSession, error)
	// Renew extends the idle expiry and records the last time the session was seen.
	Renew(id string, expiresAt, seenAt time.Time) error
	Revoke(id string, at time.Time) error
}

// TokenSource looks up API tokens by the sha256 of their full value.
type TokenSource interface {
	// FindActiveByHash returns store.ErrAPITokenNotFound for unknown or revoked tokens.
	FindActiveByHash(hash string) (*store.APIToken, error)
	MarkUsed(id string, at time.Time) error
}

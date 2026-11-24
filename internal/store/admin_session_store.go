package store

import (
	"database/sql"
	"errors"
	"time"
)

var errSessionStoreNotImplemented = errors.New("admin session store: not implemented")

// AdminSessionStore persists admin_sessions rows (skeleton; implemented in T027).
type AdminSessionStore struct{ db *sql.DB }

// NewAdminSessionStore opens the session store.
func NewAdminSessionStore(db *sql.DB) (*AdminSessionStore, error) {
	return &AdminSessionStore{db: db}, nil
}

// Create inserts a session row.
func (s *AdminSessionStore) Create(*AdminSession) error { return errSessionStoreNotImplemented }

// Get loads a session row by ID.
func (s *AdminSessionStore) Get(string) (*AdminSession, error) {
	return nil, errSessionStoreNotImplemented
}

// Renew extends the idle expiry and records last_seen_at.
func (s *AdminSessionStore) Renew(string, time.Time, time.Time) error {
	return errSessionStoreNotImplemented
}

// Revoke marks the session revoked.
func (s *AdminSessionStore) Revoke(string, time.Time) error { return errSessionStoreNotImplemented }

// ErrAdminSessionNotFound is returned when a session row does not exist.
var ErrAdminSessionNotFound = errors.New("admin session not found")

// AdminSession is one row of admin_sessions (data-model §1.2). The signed cookie
// carries only the ID (claim "sid"); the row is the server-side source of truth
// for revocation and expiry.
type AdminSession struct {
	ID                string
	UserID            string
	CreatedAt         time.Time
	LastSeenAt        time.Time
	ExpiresAt         time.Time
	AbsoluteExpiresAt time.Time
	RevokedAt         *time.Time
	IP                string
	UserAgent         string
}

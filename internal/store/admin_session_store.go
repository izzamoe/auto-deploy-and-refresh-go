package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

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

// AdminSessionStore persists admin_sessions rows.
type AdminSessionStore struct{ db *sql.DB }

// NewAdminSessionStore opens the session store, creating the table (and index)
// when Migrate has not run yet. The DDL is identical to migration v1.
func NewAdminSessionStore(db *sql.DB) (*AdminSessionStore, error) {
	for _, stmt := range []string{adminSessionsDDL, adminSessionsIndexDDL} {
		if _, err := db.Exec(stmt); err != nil {
			return nil, fmt.Errorf("admin_session_store: ensure schema: %w", err)
		}
	}
	return &AdminSessionStore{db: db}, nil
}

// Create inserts a session row.
func (s *AdminSessionStore) Create(sess *AdminSession) error {
	_, err := s.db.Exec(
		`INSERT INTO admin_sessions (id, user_id, created_at, last_seen_at, expires_at, absolute_expires_at, ip, user_agent)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.UserID, sess.CreatedAt.UTC(), sess.LastSeenAt.UTC(), sess.ExpiresAt.UTC(), sess.AbsoluteExpiresAt.UTC(), sess.IP, sess.UserAgent,
	)
	if err != nil {
		return fmt.Errorf("admin_session_store: create: %w", err)
	}
	return nil
}

// Get loads a session row by ID; ErrAdminSessionNotFound when absent.
func (s *AdminSessionStore) Get(id string) (*AdminSession, error) {
	var (
		sess    AdminSession
		revoked sql.NullTime
	)
	err := s.db.QueryRow(
		`SELECT id, user_id, created_at, last_seen_at, expires_at, absolute_expires_at, revoked_at, ip, user_agent
		 FROM admin_sessions WHERE id = ?`, id,
	).Scan(&sess.ID, &sess.UserID, &sess.CreatedAt, &sess.LastSeenAt, &sess.ExpiresAt, &sess.AbsoluteExpiresAt, &revoked, &sess.IP, &sess.UserAgent)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAdminSessionNotFound
		}
		return nil, fmt.Errorf("admin_session_store: get: %w", err)
	}
	if revoked.Valid {
		t := revoked.Time
		sess.RevokedAt = &t
	}
	return &sess, nil
}

// Renew sets the idle expiry and last_seen_at of a live (not revoked) session.
func (s *AdminSessionStore) Renew(id string, expiresAt, seenAt time.Time) error {
	res, err := s.db.Exec(
		`UPDATE admin_sessions SET expires_at = ?, last_seen_at = ? WHERE id = ? AND revoked_at IS NULL`,
		expiresAt.UTC(), seenAt.UTC(), id,
	)
	if err != nil {
		return fmt.Errorf("admin_session_store: renew: %w", err)
	}
	return requireAffected(res, ErrAdminSessionNotFound)
}

// Revoke marks the session revoked. Revoking an already revoked session keeps the
// original timestamp and succeeds; an unknown ID is ErrAdminSessionNotFound.
func (s *AdminSessionStore) Revoke(id string, at time.Time) error {
	res, err := s.db.Exec(
		`UPDATE admin_sessions SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?`,
		at.UTC(), id,
	)
	if err != nil {
		return fmt.Errorf("admin_session_store: revoke: %w", err)
	}
	return requireAffected(res, ErrAdminSessionNotFound)
}

// DeleteStale removes sessions whose absolute expiry or revocation is older than
// cutoff (retention, data-model §1.2) and returns how many rows were deleted.
func (s *AdminSessionStore) DeleteStale(cutoff time.Time) (int64, error) {
	res, err := s.db.Exec(
		`DELETE FROM admin_sessions WHERE absolute_expires_at < ? OR (revoked_at IS NOT NULL AND revoked_at < ?)`,
		cutoff.UTC(), cutoff.UTC(),
	)
	if err != nil {
		return 0, fmt.Errorf("admin_session_store: delete stale: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("admin_session_store: delete stale: %w", err)
	}
	return n, nil
}

func requireAffected(res sql.Result, notFound error) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return notFound
	}
	return nil
}

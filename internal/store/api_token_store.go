package store

import (
	"database/sql"
	"errors"
	"fmt"
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

// APITokenStore persists api_tokens rows.
type APITokenStore struct{ db *sql.DB }

// NewAPITokenStore opens the token store, creating the table when Migrate has not
// run yet (same DDL as migration v1).
func NewAPITokenStore(db *sql.DB) (*APITokenStore, error) {
	if _, err := db.Exec(apiTokensDDL); err != nil {
		return nil, fmt.Errorf("api_token_store: ensure schema: %w", err)
	}
	return &APITokenStore{db: db}, nil
}

// NewAPITokenID returns a fresh 16-byte hex token ID.
func NewAPITokenID() (string, error) { return generateID() }

// Create inserts a token row. t.ID must be set; CreatedAt defaults to now.
func (s *APITokenStore) Create(t *APIToken) error {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.Exec(
		`INSERT INTO api_tokens (id, user_id, name, token_hash, prefix, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		t.ID, t.UserID, t.Name, t.TokenHash, t.Prefix, t.CreatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("api_token_store: create: %w", err)
	}
	return nil
}

const apiTokenColumns = `id, user_id, name, token_hash, prefix, created_at, last_used_at, revoked_at`

// List returns the user's tokens (revoked ones included), newest first.
func (s *APITokenStore) List(userID string) ([]APIToken, error) {
	rows, err := s.db.Query(`SELECT `+apiTokenColumns+` FROM api_tokens WHERE user_id = ? ORDER BY created_at DESC, id`, userID)
	if err != nil {
		return nil, fmt.Errorf("api_token_store: list: %w", err)
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, fmt.Errorf("api_token_store: list scan: %w", err)
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("api_token_store: list: %w", err)
	}
	return out, nil
}

// FindActiveByHash returns the non-revoked token with this sha256 hex, or
// ErrAPITokenNotFound.
func (s *APITokenStore) FindActiveByHash(hash string) (*APIToken, error) {
	row := s.db.QueryRow(`SELECT `+apiTokenColumns+` FROM api_tokens WHERE token_hash = ? AND revoked_at IS NULL`, hash)
	t, err := scanAPIToken(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAPITokenNotFound
		}
		return nil, fmt.Errorf("api_token_store: find: %w", err)
	}
	return t, nil
}

// MarkUsed records last_used_at.
func (s *APITokenStore) MarkUsed(id string, at time.Time) error {
	if _, err := s.db.Exec(`UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, at.UTC(), id); err != nil {
		return fmt.Errorf("api_token_store: mark used: %w", err)
	}
	return nil
}

// Revoke revokes the user's token. A token that does not exist or belongs to
// someone else is ErrAPITokenNotFound; revoking twice keeps the first timestamp.
func (s *APITokenStore) Revoke(userID, id string, at time.Time) error {
	res, err := s.db.Exec(
		`UPDATE api_tokens SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ? AND user_id = ?`,
		at.UTC(), id, userID,
	)
	if err != nil {
		return fmt.Errorf("api_token_store: revoke: %w", err)
	}
	return requireAffected(res, ErrAPITokenNotFound)
}

func scanAPIToken(r rowScanner) (*APIToken, error) {
	var (
		t         APIToken
		lastUsed  sql.NullTime
		revokedAt sql.NullTime
	)
	if err := r.Scan(&t.ID, &t.UserID, &t.Name, &t.TokenHash, &t.Prefix, &t.CreatedAt, &lastUsed, &revokedAt); err != nil {
		return nil, err
	}
	if lastUsed.Valid {
		v := lastUsed.Time
		t.LastUsedAt = &v
	}
	if revokedAt.Valid {
		v := revokedAt.Time
		t.RevokedAt = &v
	}
	return &t, nil
}

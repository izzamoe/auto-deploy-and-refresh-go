package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var ErrAdminUserNotFound = errors.New("admin user not found")

// AdminUser is an admin account whose credentials live in the database rather
// than in environment variables.
type AdminUser struct {
	ID                 string
	Username           string
	PasswordHash       string
	MustChangePassword bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type AdminUserStore struct {
	db *sql.DB
}

func NewAdminUserStore(db *sql.DB) (*AdminUserStore, error) {
	schema := `CREATE TABLE IF NOT EXISTS admin_users (
		id                   TEXT PRIMARY KEY,
		username             TEXT NOT NULL UNIQUE,
		password_hash        TEXT NOT NULL,
		must_change_password INTEGER NOT NULL DEFAULT 0,
		created_at           DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at           DATETIME DEFAULT CURRENT_TIMESTAMP
	)`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("admin_user_store: create table: %w", err)
	}
	return &AdminUserStore{db: db}, nil
}

// EnsureSeed inserts a single default admin account when the table is empty.
// The seeded account is flagged must_change_password so the operator is forced
// to replace the well-known default before it can be used elsewhere.
// It returns true when a seed was inserted.
func (s *AdminUserStore) EnsureSeed(username, password string) (bool, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM admin_users`).Scan(&count); err != nil {
		return false, fmt.Errorf("admin_user_store: count: %w", err)
	}
	if count > 0 {
		return false, nil
	}

	hash, err := hashPassword(password)
	if err != nil {
		return false, err
	}
	id, err := generateID()
	if err != nil {
		return false, err
	}
	if _, err := s.db.Exec(
		`INSERT INTO admin_users (id, username, password_hash, must_change_password) VALUES (?, ?, ?, 1)`,
		id, username, hash,
	); err != nil {
		return false, fmt.Errorf("admin_user_store: seed: %w", err)
	}
	return true, nil
}

func (s *AdminUserStore) GetByUsername(username string) (*AdminUser, error) {
	row := s.db.QueryRow(
		`SELECT id, username, password_hash, must_change_password, created_at, updated_at FROM admin_users WHERE username = ?`,
		username,
	)
	user, err := scanAdminUser(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAdminUserNotFound
		}
		return nil, fmt.Errorf("admin_user_store: get by username: %w", err)
	}
	return user, nil
}

// VerifyPassword returns the user when username exists and the password matches
// its bcrypt hash. ok is false for both a missing user and a wrong password, so
// callers cannot distinguish the two.
func (s *AdminUserStore) VerifyPassword(username, password string) (*AdminUser, bool, error) {
	user, err := s.GetByUsername(username)
	if err != nil {
		if errors.Is(err, ErrAdminUserNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return nil, false, nil
	}
	return user, true, nil
}

// UpdatePassword sets a new bcrypt hash and clears the must_change_password flag.
func (s *AdminUserStore) UpdatePassword(id, newPassword string) error {
	hash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.execUpdate(
		`UPDATE admin_users SET password_hash = ?, must_change_password = 0, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		hash, id,
	)
}

// UpdateUsername renames the account.
func (s *AdminUserStore) UpdateUsername(id, newUsername string) error {
	err := s.execUpdate(
		`UPDATE admin_users SET username = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		newUsername, id,
	)
	if err != nil && isUniqueViolation(err) {
		return ErrDuplicateApp
	}
	return err
}

func (s *AdminUserStore) execUpdate(query string, args ...any) error {
	result, err := s.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("admin_user_store: update: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("admin_user_store: update rows affected: %w", err)
	}
	if n == 0 {
		return ErrAdminUserNotFound
	}
	return nil
}

func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("admin_user_store: hash password: %w", err)
	}
	return string(hash), nil
}

func scanAdminUser(row *sql.Row) (*AdminUser, error) {
	var user AdminUser
	var mustChange int
	if err := row.Scan(&user.ID, &user.Username, &user.PasswordHash, &mustChange, &user.CreatedAt, &user.UpdatedAt); err != nil {
		return nil, err
	}
	user.MustChangePassword = mustChange == 1
	return &user, nil
}

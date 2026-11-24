package store

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"sync"
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
	// TokenVersion is bumped on password/username change and "revoke all"; a
	// session token carrying an older version is rejected (FR-005).
	TokenVersion int
	CreatedAt    time.Time
	UpdatedAt    time.Time
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
	// token_version is added by migration v1 too (same definition); adding it here
	// keeps the store usable before Migrate has run. Idempotent.
	if err := ensureTokenVersionColumn(db); err != nil {
		return nil, fmt.Errorf("admin_user_store: %w", err)
	}
	return &AdminUserStore{db: db}, nil
}

func ensureTokenVersionColumn(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := addColumnIfMissing(tx, "admin_users", "token_version", "token_version INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	return tx.Commit()
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

// CreateUser inserts an account with a bcrypt-hashed password.
func (s *AdminUserStore) CreateUser(username, password string, mustChange bool) (*AdminUser, error) {
	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	id, err := generateID()
	if err != nil {
		return nil, err
	}
	flag := 0
	if mustChange {
		flag = 1
	}
	if _, err := s.db.Exec(
		`INSERT INTO admin_users (id, username, password_hash, must_change_password) VALUES (?, ?, ?, ?)`,
		id, username, hash, flag,
	); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicateApp
		}
		return nil, fmt.Errorf("admin_user_store: create: %w", err)
	}
	return s.GetByUsername(username)
}

// GetByID returns the user with the stable ID; ErrAdminUserNotFound when absent.
func (s *AdminUserStore) GetByID(id string) (*AdminUser, error) {
	row := s.db.QueryRow(`SELECT `+adminUserColumns+` FROM admin_users WHERE id = ?`, id)
	user, err := scanAdminUser(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAdminUserNotFound
		}
		return nil, fmt.Errorf("admin_user_store: get by id: %w", err)
	}
	return user, nil
}

func (s *AdminUserStore) GetByUsername(username string) (*AdminUser, error) {
	row := s.db.QueryRow(`SELECT `+adminUserColumns+` FROM admin_users WHERE username = ?`, username)
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
			// B29: spend the same bcrypt time as for a real user so response timing
			// does not reveal which usernames exist.
			_ = bcrypt.CompareHashAndPassword(dummyPasswordHash(), []byte(password))
			return nil, false, nil
		}
		return nil, false, err
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return nil, false, nil
	}
	return user, true, nil
}

// UpdatePassword sets a new bcrypt hash, clears must_change_password and bumps
// token_version so every session issued before the change is rejected (B4, FR-005).
func (s *AdminUserStore) UpdatePassword(id, newPassword string) error {
	hash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.execUpdate(
		`UPDATE admin_users SET password_hash = ?, must_change_password = 0, token_version = token_version + 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		hash, id,
	)
}

// UpdateUsername renames the account and bumps token_version (B3, FR-005).
func (s *AdminUserStore) UpdateUsername(id, newUsername string) error {
	err := s.execUpdate(
		`UPDATE admin_users SET username = ?, token_version = token_version + 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		newUsername, id,
	)
	if err != nil && isUniqueViolation(err) {
		return ErrDuplicateApp
	}
	return err
}

// BumpTokenVersion revokes every outstanding session of the user ("log out all").
func (s *AdminUserStore) BumpTokenVersion(id string) error {
	return s.execUpdate(`UPDATE admin_users SET token_version = token_version + 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
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

const adminUserColumns = `id, username, password_hash, must_change_password, token_version, created_at, updated_at`

var (
	dummyHashOnce  sync.Once
	dummyHashBytes []byte
)

// dummyPasswordHash is a bcrypt hash (default cost) of a random secret, computed
// once per process, compared against when the username does not exist.
func dummyPasswordHash() []byte {
	dummyHashOnce.Do(func() {
		secret := make([]byte, 24)
		_, _ = rand.Read(secret)
		h, err := bcrypt.GenerateFromPassword(secret, bcrypt.DefaultCost)
		if err != nil {
			panic("admin_user_store: dummy hash: " + err.Error())
		}
		dummyHashBytes = h
	})
	return dummyHashBytes
}

func scanAdminUser(row *sql.Row) (*AdminUser, error) {
	var user AdminUser
	var mustChange int
	if err := row.Scan(&user.ID, &user.Username, &user.PasswordHash, &mustChange, &user.TokenVersion, &user.CreatedAt, &user.UpdatedAt); err != nil {
		return nil, err
	}
	user.MustChangePassword = mustChange == 1
	return &user, nil
}

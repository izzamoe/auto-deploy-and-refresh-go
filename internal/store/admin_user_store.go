package store

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

const (
	// InitialAdminUsername is the name of the account created on a fresh install.
	InitialAdminUsername = "admin"
	// InitialAdminPasswordFile is the file, next to the database, that holds the
	// one-time random password of that account (mode 0600).
	InitialAdminPasswordFile = "initial-admin-password"

	initialPasswordLen      = 24
	initialPasswordAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
)

// EnsureInitialAdmin creates the first admin account when the table is empty
// (Q-3, FR-009). The password is random (24 alphanumeric characters, about 143
// bits) and is written to <dir>/initial-admin-password with mode 0600; it is never
// logged, only the path is. The account is flagged must_change_password, and the
// file is deleted once the password has been changed.
//
// An install that already has any account is left untouched (created=false, no
// file). The file is written BEFORE the row is inserted and removed again if the
// insert fails, so a password that cannot log in is never left behind and an
// account whose password was never recorded is never created.
func (s *AdminUserStore) EnsureInitialAdmin(dir string) (created bool, passwordPath string, err error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM admin_users`).Scan(&count); err != nil {
		return false, "", fmt.Errorf("admin_user_store: count: %w", err)
	}
	if count > 0 {
		return false, "", nil
	}

	password, err := randomPassword(initialPasswordLen)
	if err != nil {
		return false, "", err
	}
	passwordPath = filepath.Join(dir, InitialAdminPasswordFile)
	if err := writeSecretFile(passwordPath, []byte(password+"\n")); err != nil {
		return false, "", fmt.Errorf("admin_user_store: write initial password file: %w", err)
	}
	if _, err := s.CreateUser(InitialAdminUsername, password, true); err != nil {
		_ = os.Remove(passwordPath)
		return false, "", fmt.Errorf("admin_user_store: create initial admin: %w", err)
	}
	return true, passwordPath, nil
}

// randomPassword returns n characters drawn uniformly from the alphabet using
// rejection sampling, so no character is more likely than another.
func randomPassword(n int) (string, error) {
	const limit = 256 - 256%len(initialPasswordAlphabet) // largest multiple of the alphabet size
	out := make([]byte, 0, n)
	buf := make([]byte, n*2)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("admin_user_store: random password: %w", err)
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, initialPasswordAlphabet[int(b)%len(initialPasswordAlphabet)])
			if len(out) == n {
				break
			}
		}
	}
	return string(out), nil
}

// writeSecretFile writes data to path with mode 0600, replacing any previous file,
// and flushes it to disk.
func writeSecretFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil { // a pre-existing file may have looser bits
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
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

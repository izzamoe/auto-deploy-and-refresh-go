package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// maxServiceArgLen caps a single command-line argument. systemd itself allows
// far longer, but a runaway paste in the admin textarea should be rejected at
// the API boundary rather than silently written into a unit file.
const maxServiceArgLen = 4096

// maxServiceArgs caps how many arguments one app may declare, for the same
// reason.
const maxServiceArgs = 256

// ValidateServiceArg reports whether arg is safe to interpolate into the
// ExecStart= line of a generated systemd unit. Control characters are rejected
// outright: a newline would end the directive and let the argument inject
// arbitrary systemd settings, and the rest cannot survive a round trip through
// the unit file anyway. An empty argument is allowed -- an empty string is a
// legitimate argv entry and is rendered as "".
func ValidateServiceArg(arg string) error {
	if len(arg) > maxServiceArgLen {
		return fmt.Errorf("argument too long: %d characters, max %d", len(arg), maxServiceArgLen)
	}
	for _, r := range arg {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("argument %q is invalid: control characters are not allowed", arg)
		}
	}
	return nil
}

// AppArgsStore persists the per-app command-line arguments appended to the
// service's ExecStart line, as a single JSON array per app. It mirrors
// AppEnvStore: a table of its own, so the apps CRUD path stays untouched.
type AppArgsStore struct {
	db *sql.DB
}

// NewAppArgsStore creates the app_args table if it does not exist yet and
// returns a store bound to db.
func NewAppArgsStore(db *sql.DB) (*AppArgsStore, error) {
	schema := `CREATE TABLE IF NOT EXISTS app_args (
		app_id     TEXT PRIMARY KEY,
		args       TEXT NOT NULL DEFAULT '[]',
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("app_args_store: create table: %w", err)
	}
	return &AppArgsStore{db: db}, nil
}

// Get returns the command-line arguments for appID, or an empty slice if none
// are stored (never a sql.ErrNoRows the caller must handle).
func (s *AppArgsStore) Get(appID string) ([]string, error) {
	var raw string
	err := s.db.QueryRow(`SELECT args FROM app_args WHERE app_id = ?`, appID).Scan(&raw)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("app_args_store: get: %w", err)
	}
	if raw == "" {
		return nil, nil
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, fmt.Errorf("app_args_store: decode: %w", err)
	}
	return args, nil
}

// Set replaces the stored command-line arguments for appID. Every argument is
// validated; an empty slice clears them.
func (s *AppArgsStore) Set(appID string, args []string) error {
	if len(args) > maxServiceArgs {
		return fmt.Errorf("too many arguments: %d, max %d", len(args), maxServiceArgs)
	}
	for _, a := range args {
		if err := ValidateServiceArg(a); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("app_args_store: encode: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO app_args (app_id, args, updated_at)
		 VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(app_id) DO UPDATE SET
		   args = excluded.args,
		   updated_at = CURRENT_TIMESTAMP`,
		appID, string(encoded),
	)
	if err != nil {
		return fmt.Errorf("app_args_store: set: %w", err)
	}
	return nil
}

// Delete removes any stored arguments for appID (e.g. when the app is
// deleted). Absence is not an error.
func (s *AppArgsStore) Delete(appID string) error {
	if _, err := s.db.Exec(`DELETE FROM app_args WHERE app_id = ?`, appID); err != nil {
		return fmt.Errorf("app_args_store: delete: %w", err)
	}
	return nil
}

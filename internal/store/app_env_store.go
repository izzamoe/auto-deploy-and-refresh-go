package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
)

// envVarNameRe matches a valid environment variable name: a letter or
// underscore followed by letters, digits, or underscores. This is enforced so
// a name can never inject a newline or "=" that would corrupt the generated
// systemd unit.
var envVarNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateEnvVarName reports whether name is a syntactically valid environment
// variable name.
func ValidateEnvVarName(name string) error {
	if name == "" {
		return fmt.Errorf("env var name must not be empty")
	}
	if !envVarNameRe.MatchString(name) {
		return fmt.Errorf("env var name %q is invalid: must match [A-Za-z_][A-Za-z0-9_]*", name)
	}
	return nil
}

// AppEnvStore persists per-app environment variables as a single JSON blob per
// app, kept out of the apps table so the app CRUD path is untouched.
type AppEnvStore struct {
	db *sql.DB
}

// NewAppEnvStore creates the app_env table if it does not exist yet and
// returns a store bound to db.
func NewAppEnvStore(db *sql.DB) (*AppEnvStore, error) {
	schema := `CREATE TABLE IF NOT EXISTS app_env (
		app_id     TEXT PRIMARY KEY,
		vars       TEXT NOT NULL DEFAULT '[]',
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("app_env_store: create table: %w", err)
	}
	return &AppEnvStore{db: db}, nil
}

// Get returns the environment variables for appID, or an empty slice if none
// are stored (never a sql.ErrNoRows the caller must handle).
func (s *AppEnvStore) Get(appID string) ([]EnvVar, error) {
	var raw string
	err := s.db.QueryRow(`SELECT vars FROM app_env WHERE app_id = ?`, appID).Scan(&raw)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("app_env_store: get: %w", err)
	}
	if raw == "" {
		return nil, nil
	}
	var vars []EnvVar
	if err := json.Unmarshal([]byte(raw), &vars); err != nil {
		return nil, fmt.Errorf("app_env_store: decode: %w", err)
	}
	return vars, nil
}

// Set replaces the stored environment variables for appID. Each name is
// validated; an empty slice clears them.
func (s *AppEnvStore) Set(appID string, vars []EnvVar) error {
	for _, v := range vars {
		if err := ValidateEnvVarName(v.Name); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(vars)
	if err != nil {
		return fmt.Errorf("app_env_store: encode: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO app_env (app_id, vars, updated_at)
		 VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(app_id) DO UPDATE SET
		   vars = excluded.vars,
		   updated_at = CURRENT_TIMESTAMP`,
		appID, string(encoded),
	)
	if err != nil {
		return fmt.Errorf("app_env_store: set: %w", err)
	}
	return nil
}

// Delete removes any stored environment variables for appID (e.g. when the app
// is deleted). Absence is not an error.
func (s *AppEnvStore) Delete(appID string) error {
	if _, err := s.db.Exec(`DELETE FROM app_env WHERE app_id = ?`, appID); err != nil {
		return fmt.Errorf("app_env_store: delete: %w", err)
	}
	return nil
}

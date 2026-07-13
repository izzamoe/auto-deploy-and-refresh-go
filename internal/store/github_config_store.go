package store

import (
	"database/sql"
	"fmt"
)

// githubConfigRowID is the literal primary key of the single config row
// stored in the github_config table.
const githubConfigRowID = "config"

// GitHubConfig holds the settings used when calling the GitHub Releases API.
// Currently only a personal access token, used to raise the request rate
// limit (5000/hr authenticated vs 60/hr anonymous) and to read private repos.
type GitHubConfig struct {
	Token string
}

// GitHubConfigStore persists a single row of GitHub API settings.
type GitHubConfigStore struct {
	db *sql.DB
}

// NewGitHubConfigStore creates the github_config table if it does not exist
// yet and returns a store bound to db.
func NewGitHubConfigStore(db *sql.DB) (*GitHubConfigStore, error) {
	schema := `CREATE TABLE IF NOT EXISTS github_config (
		id         TEXT PRIMARY KEY,
		token      TEXT NOT NULL DEFAULT '',
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("github_config_store: create table: %w", err)
	}
	return &GitHubConfigStore{db: db}, nil
}

// Get returns the current GitHub config. If no config has been saved yet, it
// returns a zero-value GitHubConfig (empty token) and no error, so callers
// never need to special-case sql.ErrNoRows.
func (s *GitHubConfigStore) Get() (*GitHubConfig, error) {
	row := s.db.QueryRow(
		`SELECT token FROM github_config WHERE id = ?`,
		githubConfigRowID,
	)

	var cfg GitHubConfig
	err := row.Scan(&cfg.Token)
	if err != nil {
		if err == sql.ErrNoRows {
			return &GitHubConfig{}, nil
		}
		return nil, fmt.Errorf("github_config_store: get: %w", err)
	}
	return &cfg, nil
}

// Save upserts the single GitHub config row.
func (s *GitHubConfigStore) Save(cfg GitHubConfig) error {
	_, err := s.db.Exec(
		`INSERT INTO github_config (id, token, updated_at)
		 VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(id) DO UPDATE SET
		   token = excluded.token,
		   updated_at = CURRENT_TIMESTAMP`,
		githubConfigRowID, cfg.Token,
	)
	if err != nil {
		return fmt.Errorf("github_config_store: save: %w", err)
	}
	return nil
}

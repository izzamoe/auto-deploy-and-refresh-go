package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrDuplicateApp = errors.New("duplicate app: uniqueness constraint violated")

type App struct {
	ID                string
	Name              string
	WebhookSecretHash string
	BinaryPath        string
	ServiceName       string
	GithubRepo        string
	ArtifactName      string
	Enabled           bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type AppStore struct {
	db *sql.DB
}

func NewAppStore(db *sql.DB) (*AppStore, error) {
	schema := `CREATE TABLE IF NOT EXISTS apps (
		id                  TEXT PRIMARY KEY,
		name                TEXT NOT NULL,
		webhook_secret_hash TEXT NOT NULL UNIQUE,
		binary_path         TEXT NOT NULL UNIQUE,
		service_name        TEXT NOT NULL UNIQUE,
		github_repo         TEXT NOT NULL,
		artifact_name       TEXT NOT NULL,
		enabled             INTEGER NOT NULL DEFAULT 1,
		created_at          DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at          DATETIME DEFAULT CURRENT_TIMESTAMP
	)`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("app_store: create table: %w", err)
	}
	return &AppStore{db: db}, nil
}

func HashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("app_store: generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (s *AppStore) Create(name, secret, binaryPath, serviceName, githubRepo, artifactName string) (*App, error) {
	id, err := generateID()
	if err != nil {
		return nil, err
	}

	hash := HashSecret(secret)

	_, err = s.db.Exec(
		`INSERT INTO apps (id, name, webhook_secret_hash, binary_path, service_name, github_repo, artifact_name)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, name, hash, binaryPath, serviceName, githubRepo, artifactName,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicateApp
		}
		return nil, fmt.Errorf("app_store: insert: %w", err)
	}

	return s.Get(id)
}

func (s *AppStore) List() ([]App, error) {
	rows, err := s.db.Query(
		`SELECT id, name, webhook_secret_hash, binary_path, service_name, github_repo, artifact_name, enabled, created_at, updated_at
		 FROM apps ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("app_store: list: %w", err)
	}
	defer rows.Close()

	var apps []App
	for rows.Next() {
		app, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

func (s *AppStore) Get(id string) (*App, error) {
	row := s.db.QueryRow(
		`SELECT id, name, webhook_secret_hash, binary_path, service_name, github_repo, artifact_name, enabled, created_at, updated_at
		 FROM apps WHERE id = ?`,
		id,
	)
	app, err := scanAppRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("app_store: app %q not found", id)
		}
		return nil, fmt.Errorf("app_store: get: %w", err)
	}
	return &app, nil
}

func (s *AppStore) GetBySecretHash(hash string) (*App, error) {
	row := s.db.QueryRow(
		`SELECT id, name, webhook_secret_hash, binary_path, service_name, github_repo, artifact_name, enabled, created_at, updated_at
		 FROM apps WHERE webhook_secret_hash = ? AND enabled = 1`,
		hash,
	)
	app, err := scanAppRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("app_store: get by secret hash: %w", err)
	}
	return &app, nil
}

func (s *AppStore) Update(id, name, binaryPath, serviceName, githubRepo, artifactName string, enabled bool) error {
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}

	result, err := s.db.Exec(
		`UPDATE apps SET name = ?, binary_path = ?, service_name = ?, github_repo = ?, artifact_name = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		name, binaryPath, serviceName, githubRepo, artifactName, enabledInt, id,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicateApp
		}
		return fmt.Errorf("app_store: update: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("app_store: update rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("app_store: app %q not found", id)
	}
	return nil
}

func (s *AppStore) RotateSecret(id, newSecret string) error {
	hash := HashSecret(newSecret)

	result, err := s.db.Exec(
		`UPDATE apps SET webhook_secret_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		hash, id,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicateApp
		}
		return fmt.Errorf("app_store: rotate secret: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("app_store: rotate secret rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("app_store: app %q not found", id)
	}
	return nil
}

func (s *AppStore) SetEnabled(id string, enabled bool) error {
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}

	result, err := s.db.Exec(
		`UPDATE apps SET enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		enabledInt, id,
	)
	if err != nil {
		return fmt.Errorf("app_store: set enabled: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("app_store: set enabled rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("app_store: app %q not found", id)
	}
	return nil
}

func (s *AppStore) BootstrapIfEmpty(legacy *LegacyBootstrapConfig) (*App, error) {
	if legacy == nil {
		return nil, nil
	}

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM apps`).Scan(&count); err != nil {
		return nil, fmt.Errorf("app_store: bootstrap count: %w", err)
	}
	if count > 0 {
		row := s.db.QueryRow(
			`SELECT id, name, webhook_secret_hash, binary_path, service_name, github_repo, artifact_name, enabled, created_at, updated_at
			 FROM apps ORDER BY created_at ASC LIMIT 1`,
		)
		app, err := scanAppRow(row)
		if err != nil {
			return nil, fmt.Errorf("app_store: bootstrap get existing: %w", err)
		}
		return &app, nil
	}

	return s.Create("default", legacy.Secret, legacy.BinaryPath, legacy.ServiceName, legacy.GithubRepo, legacy.ArtifactName)
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func scanApp(rows *sql.Rows) (App, error) {
	var app App
	var enabled int
	if err := rows.Scan(
		&app.ID, &app.Name, &app.WebhookSecretHash, &app.BinaryPath,
		&app.ServiceName, &app.GithubRepo, &app.ArtifactName,
		&enabled, &app.CreatedAt, &app.UpdatedAt,
	); err != nil {
		return App{}, fmt.Errorf("app_store: scan: %w", err)
	}
	app.Enabled = enabled == 1
	return app, nil
}

func scanAppRow(row *sql.Row) (App, error) {
	var app App
	var enabled int
	if err := row.Scan(
		&app.ID, &app.Name, &app.WebhookSecretHash, &app.BinaryPath,
		&app.ServiceName, &app.GithubRepo, &app.ArtifactName,
		&enabled, &app.CreatedAt, &app.UpdatedAt,
	); err != nil {
		return App{}, err
	}
	app.Enabled = enabled == 1
	return app, nil
}

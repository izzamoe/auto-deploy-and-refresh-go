package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/izzamoe/auto-deploy/internal/config"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"modernc.org/sqlite"
)

var ErrDuplicateApp = errors.New("duplicate app: uniqueness constraint violated")
var ErrActiveDeployExists = errors.New("active deploy in progress")

const sqliteConstraintUnique = 2067 // SQLITE_CONSTRAINT_UNIQUE

type AppWithLastDeploy struct {
	App
	LastDeployTag        string
	LastDeployStatus     string
	LastDeployTime       *time.Time
	LastJobID            string
	LastJobStatus        string
	LastDownloadBytes    int64
	LastDownloadSpeedBPS float64
	LiveProgress         *progress.ProgressSnapshot
}

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

func (s *AppStore) DB() *sql.DB {
	return s.db
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

func (s *AppStore) UpdateWithOptionalSecret(id, name, secret, binaryPath, serviceName, githubRepo, artifactName string, enabled bool) error {
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}

	var result interface{ RowsAffected() (int64, error) }
	var err error
	if secret == "" {
		result, err = s.db.Exec(
			`UPDATE apps SET name = ?, binary_path = ?, service_name = ?, github_repo = ?, artifact_name = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			name, binaryPath, serviceName, githubRepo, artifactName, enabledInt, id,
		)
	} else {
		result, err = s.db.Exec(
			`UPDATE apps SET name = ?, webhook_secret_hash = ?, binary_path = ?, service_name = ?, github_repo = ?, artifact_name = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			name, HashSecret(secret), binaryPath, serviceName, githubRepo, artifactName, enabledInt, id,
		)
	}
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicateApp
		}
		return fmt.Errorf("app_store: update with optional secret: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("app_store: update with optional secret rows affected: %w", err)
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

func (s *AppStore) BootstrapIfEmpty(legacy *config.LegacyBootstrapConfig) (*App, error) {
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

func (s *AppStore) Delete(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("app_store: begin tx: %w", err)
	}
	defer tx.Rollback()

	var activeCount int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM deploy_jobs WHERE app_id = ? AND status = 'in_progress'`, id).Scan(&activeCount); err != nil {
		return fmt.Errorf("app_store: check active deploys: %w", err)
	}
	if activeCount > 0 {
		return ErrActiveDeployExists
	}

	if _, err := tx.Exec(`DELETE FROM deploy_jobs WHERE app_id = ?`, id); err != nil {
		return fmt.Errorf("app_store: delete jobs: %w", err)
	}

	result, err := tx.Exec(`DELETE FROM apps WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("app_store: delete app: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("app_store: app %q not found", id)
	}

	return tx.Commit()
}

func (s *AppStore) ListWithLastDeploy() ([]AppWithLastDeploy, error) {
	rows, err := s.db.Query(`
		SELECT a.id, a.name, a.webhook_secret_hash, a.binary_path, a.service_name,
		       a.github_repo, a.artifact_name, a.enabled, a.created_at, a.updated_at,
		       j.id, j.tag, j.status, j.download_bytes, j.download_speed_bps, j.created_at
		FROM apps a
		LEFT JOIN (
		  SELECT id, app_id, tag, status, download_bytes, download_speed_bps, created_at,
		         ROW_NUMBER() OVER (PARTITION BY app_id ORDER BY created_at DESC) as rn
		  FROM deploy_jobs
		) j ON j.app_id = a.id AND j.rn = 1
		ORDER BY a.created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("app_store: list with last deploy: %w", err)
	}
	defer rows.Close()

	var apps []AppWithLastDeploy
	for rows.Next() {
		var a AppWithLastDeploy
		var enabled int
		var jobID, tag, status sql.NullString
		var downloadBytes sql.NullInt64
		var downloadSpeed sql.NullFloat64
		var deployedAt sql.NullTime
		if err := rows.Scan(
			&a.ID, &a.Name, &a.WebhookSecretHash, &a.BinaryPath,
			&a.ServiceName, &a.GithubRepo, &a.ArtifactName,
			&enabled, &a.CreatedAt, &a.UpdatedAt,
			&jobID, &tag, &status, &downloadBytes, &downloadSpeed, &deployedAt,
		); err != nil {
			return nil, fmt.Errorf("app_store: scan with last deploy: %w", err)
		}
		a.Enabled = enabled == 1
		if jobID.Valid {
			a.LastJobID = jobID.String
		}
		if tag.Valid {
			a.LastDeployTag = tag.String
		}
		if status.Valid {
			a.LastDeployStatus = status.String
			a.LastJobStatus = status.String
		}
		if downloadBytes.Valid {
			a.LastDownloadBytes = downloadBytes.Int64
		}
		if downloadSpeed.Valid {
			a.LastDownloadSpeedBPS = downloadSpeed.Float64
		}
		if deployedAt.Valid {
			t := deployedAt.Time
			a.LastDeployTime = &t
		}
		apps = append(apps, a)
	}
	return apps, rows.Err()
}

func isUniqueViolation(err error) bool {
	sqliteErr, ok := errors.AsType[*sqlite.Error](err)
	return ok && sqliteErr.Code() == sqliteConstraintUnique
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

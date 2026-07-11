package store

import (
	"database/sql"
	"fmt"
)

// telegramConfigRowID is the literal primary key of the single config row
// stored in the telegram_config table.
const telegramConfigRowID = "config"

// TelegramConfig holds the credentials and target needed to send Telegram
// deploy notifications via gotd/td.
type TelegramConfig struct {
	AppID        int
	AppHash      string
	BotToken     string
	ChatUsername string
	Enabled      bool
}

// TelegramConfigStore persists a single row of Telegram notification
// settings.
type TelegramConfigStore struct {
	db *sql.DB
}

// NewTelegramConfigStore creates the telegram_config table if it does not
// exist yet and returns a store bound to db.
func NewTelegramConfigStore(db *sql.DB) (*TelegramConfigStore, error) {
	schema := `CREATE TABLE IF NOT EXISTS telegram_config (
		id            TEXT PRIMARY KEY,
		app_id        INTEGER NOT NULL DEFAULT 0,
		app_hash      TEXT NOT NULL DEFAULT '',
		bot_token     TEXT NOT NULL DEFAULT '',
		chat_username TEXT NOT NULL DEFAULT '',
		enabled       INTEGER NOT NULL DEFAULT 0,
		updated_at    DATETIME DEFAULT CURRENT_TIMESTAMP
	)`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("telegram_config_store: create table: %w", err)
	}
	return &TelegramConfigStore{db: db}, nil
}

// Get returns the current Telegram config. If no config has been saved yet,
// it returns a zero-value TelegramConfig (Enabled: false) and no error, so
// callers never need to special-case sql.ErrNoRows.
func (s *TelegramConfigStore) Get() (*TelegramConfig, error) {
	row := s.db.QueryRow(
		`SELECT app_id, app_hash, bot_token, chat_username, enabled FROM telegram_config WHERE id = ?`,
		telegramConfigRowID,
	)

	var cfg TelegramConfig
	var enabled int
	err := row.Scan(&cfg.AppID, &cfg.AppHash, &cfg.BotToken, &cfg.ChatUsername, &enabled)
	if err != nil {
		if err == sql.ErrNoRows {
			return &TelegramConfig{}, nil
		}
		return nil, fmt.Errorf("telegram_config_store: get: %w", err)
	}
	cfg.Enabled = enabled == 1
	return &cfg, nil
}

// Save upserts the single Telegram config row.
func (s *TelegramConfigStore) Save(cfg TelegramConfig) error {
	enabled := 0
	if cfg.Enabled {
		enabled = 1
	}

	_, err := s.db.Exec(
		`INSERT INTO telegram_config (id, app_id, app_hash, bot_token, chat_username, enabled, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(id) DO UPDATE SET
		   app_id = excluded.app_id,
		   app_hash = excluded.app_hash,
		   bot_token = excluded.bot_token,
		   chat_username = excluded.chat_username,
		   enabled = excluded.enabled,
		   updated_at = CURRENT_TIMESTAMP`,
		telegramConfigRowID, cfg.AppID, cfg.AppHash, cfg.BotToken, cfg.ChatUsername, enabled,
	)
	if err != nil {
		return fmt.Errorf("telegram_config_store: save: %w", err)
	}
	return nil
}

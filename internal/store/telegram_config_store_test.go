package store

import (
	"testing"
)

func newTestTelegramConfigStore(t *testing.T) *TelegramConfigStore {
	t.Helper()
	db := newTestDB(t)
	s, err := NewTelegramConfigStore(db)
	if err != nil {
		t.Fatalf("NewTelegramConfigStore: %v", err)
	}
	return s
}

func TestTelegramConfigStoreGetReturnsZeroValueWhenMissing(t *testing.T) {
	t.Parallel()
	s := newTestTelegramConfigStore(t)

	cfg, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cfg == nil {
		t.Fatal("Get returned nil config")
	}
	if cfg.Enabled {
		t.Error("expected Enabled to default to false")
	}
	if cfg.AppID != 0 || cfg.AppHash != "" || cfg.BotToken != "" || cfg.ChatUsername != "" {
		t.Errorf("expected zero-value config, got %+v", cfg)
	}
}

func TestTelegramConfigStoreSaveAndGet(t *testing.T) {
	t.Parallel()
	s := newTestTelegramConfigStore(t)

	cfg := TelegramConfig{
		AppID:        12345,
		AppHash:      "abcdef0123456789",
		BotToken:     "123456:ABC-DEF",
		ChatUsername: "@mychannel",
		Enabled:      true,
	}
	if err := s.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if *got != cfg {
		t.Fatalf("Get() = %+v, want %+v", *got, cfg)
	}
}

func TestTelegramConfigStoreSaveUpserts(t *testing.T) {
	t.Parallel()
	s := newTestTelegramConfigStore(t)

	first := TelegramConfig{
		AppID:        1,
		AppHash:      "hash1",
		BotToken:     "token1",
		ChatUsername: "@one",
		Enabled:      true,
	}
	if err := s.Save(first); err != nil {
		t.Fatalf("Save first: %v", err)
	}

	second := TelegramConfig{
		AppID:        2,
		AppHash:      "hash2",
		BotToken:     "token2",
		ChatUsername: "@two",
		Enabled:      false,
	}
	if err := s.Save(second); err != nil {
		t.Fatalf("Save second: %v", err)
	}

	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if *got != second {
		t.Fatalf("Get() = %+v, want %+v (upsert should replace, not add a row)", *got, second)
	}

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM telegram_config`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 row after upsert, got %d", count)
	}
}

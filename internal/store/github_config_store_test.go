package store

import (
	"testing"
)

func newTestGitHubConfigStore(t *testing.T) *GitHubConfigStore {
	t.Helper()
	db := newTestDB(t)
	s, err := NewGitHubConfigStore(db)
	if err != nil {
		t.Fatalf("NewGitHubConfigStore: %v", err)
	}
	return s
}

func TestGitHubConfigStoreGetReturnsZeroValueWhenMissing(t *testing.T) {
	t.Parallel()
	s := newTestGitHubConfigStore(t)

	cfg, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cfg == nil {
		t.Fatal("Get returned nil config")
	}
	if cfg.Token != "" {
		t.Errorf("expected empty token by default, got %q", cfg.Token)
	}
}

func TestGitHubConfigStoreSaveAndGet(t *testing.T) {
	t.Parallel()
	s := newTestGitHubConfigStore(t)

	cfg := GitHubConfig{Token: "github_pat_example123"}
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

func TestGitHubConfigStoreSaveUpserts(t *testing.T) {
	t.Parallel()
	s := newTestGitHubConfigStore(t)

	if err := s.Save(GitHubConfig{Token: "first"}); err != nil {
		t.Fatalf("Save first: %v", err)
	}
	if err := s.Save(GitHubConfig{Token: "second"}); err != nil {
		t.Fatalf("Save second: %v", err)
	}

	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Token != "second" {
		t.Fatalf("Get().Token = %q, want %q (upsert should replace)", got.Token, "second")
	}

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM github_config`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 row after upsert, got %d", count)
	}
}

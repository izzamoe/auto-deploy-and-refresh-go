package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newTestAPITokenStore(t *testing.T) *APITokenStore {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "tokens.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	s, err := NewAPITokenStore(db)
	if err != nil {
		t.Fatalf("NewAPITokenStore: %v", err)
	}
	return s
}

func TestAPIToken_CreateFindListRevoke(t *testing.T) {
	s := newTestAPITokenStore(t)
	t1 := &APIToken{ID: "t1", UserID: "u1", Name: "ci", TokenHash: "hash-1", Prefix: "abcdefgh", CreatedAt: time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)}
	t2 := &APIToken{ID: "t2", UserID: "u1", Name: "backup", TokenHash: "hash-2", Prefix: "ijklmnop", CreatedAt: time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)}
	other := &APIToken{ID: "t3", UserID: "u2", Name: "theirs", TokenHash: "hash-3", Prefix: "qrstuvwx"}
	for _, tk := range []*APIToken{t1, t2, other} {
		if err := s.Create(tk); err != nil {
			t.Fatalf("Create(%s): %v", tk.ID, err)
		}
	}
	// token_hash is UNIQUE.
	if err := s.Create(&APIToken{ID: "dup", UserID: "u1", Name: "dup", TokenHash: "hash-1", Prefix: "zzzzzzzz"}); err == nil {
		t.Error("duplicate token_hash must be rejected")
	}

	got, err := s.FindActiveByHash("hash-1")
	if err != nil || got.ID != "t1" || got.Name != "ci" || got.RevokedAt != nil || got.LastUsedAt != nil {
		t.Fatalf("FindActiveByHash: %v %+v", err, got)
	}
	if _, err := s.FindActiveByHash("nope"); !errors.Is(err, ErrAPITokenNotFound) {
		t.Errorf("unknown hash: err = %v, want ErrAPITokenNotFound", err)
	}

	used := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	if err := s.MarkUsed("t1", used); err != nil {
		t.Fatal(err)
	}
	got, _ = s.FindActiveByHash("hash-1")
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(used) {
		t.Errorf("last_used_at = %v, want %v", got.LastUsedAt, used)
	}

	list, err := s.List("u1")
	if err != nil || len(list) != 2 || list[0].ID != "t2" || list[1].ID != "t1" {
		t.Fatalf("List(u1) = %v %+v, want t2 then t1 (newest first, only u1's)", err, list)
	}

	// Another user cannot revoke u1's token.
	if err := s.Revoke("u2", "t1", used); !errors.Is(err, ErrAPITokenNotFound) {
		t.Errorf("cross-user revoke: err = %v, want ErrAPITokenNotFound", err)
	}
	if _, err := s.FindActiveByHash("hash-1"); err != nil {
		t.Errorf("token must still be active after a foreign revoke attempt: %v", err)
	}
	if err := s.Revoke("u1", "t1", used); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := s.Revoke("u1", "t1", used.Add(time.Hour)); err != nil {
		t.Fatalf("second Revoke must be idempotent: %v", err)
	}
	if _, err := s.FindActiveByHash("hash-1"); !errors.Is(err, ErrAPITokenNotFound) {
		t.Errorf("revoked token must not be found: %v", err)
	}
	list, _ = s.List("u1")
	var revoked *APIToken
	for i := range list {
		if list[i].ID == "t1" {
			revoked = &list[i]
		}
	}
	if revoked == nil || revoked.RevokedAt == nil || !revoked.RevokedAt.Equal(used) {
		t.Errorf("List must keep revoked tokens with their first revoked_at: %+v", revoked)
	}
	if err := s.Revoke("u1", "ghost", used); !errors.Is(err, ErrAPITokenNotFound) {
		t.Errorf("unknown id: err = %v", err)
	}
}

func TestAPIToken_NeverStoresFullToken(t *testing.T) {
	s := newTestAPITokenStore(t)
	if err := s.Create(&APIToken{ID: "t1", UserID: "u1", Name: "ci", TokenHash: "only-the-hash", Prefix: "abcdefgh"}); err != nil {
		t.Fatal(err)
	}
	var cols int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('api_tokens') WHERE name IN ('token', 'secret', 'value')`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if cols != 0 {
		t.Error("api_tokens must not have a column that could hold the full token")
	}
}

package store

import (
	"errors"
	"path/filepath"
	"testing"

	"database/sql"

	_ "modernc.org/sqlite"
)

func newTestAdminUserStore(t *testing.T) *AdminUserStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "admin.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	s, err := NewAdminUserStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("NewAdminUserStore: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return s
}

func TestAdminUserStoreSeedsDefaultOnce(t *testing.T) {
	t.Parallel()
	s := newTestAdminUserStore(t)

	seeded, err := s.EnsureSeed("admin", "11")
	if err != nil {
		t.Fatalf("EnsureSeed: %v", err)
	}
	if !seeded {
		t.Fatal("expected first EnsureSeed to seed")
	}

	user, err := s.GetByUsername("admin")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if !user.MustChangePassword {
		t.Error("seeded user must have must_change_password set")
	}
	if user.PasswordHash == "11" || user.PasswordHash == "" {
		t.Errorf("password must be hashed, got %q", user.PasswordHash)
	}

	// Second call must be a no-op (idempotent), not a duplicate seed.
	seeded, err = s.EnsureSeed("admin", "11")
	if err != nil {
		t.Fatalf("second EnsureSeed: %v", err)
	}
	if seeded {
		t.Error("expected second EnsureSeed to be a no-op")
	}
}

func TestAdminUserStoreVerifyPassword(t *testing.T) {
	t.Parallel()
	s := newTestAdminUserStore(t)
	if _, err := s.EnsureSeed("admin", "11"); err != nil {
		t.Fatalf("EnsureSeed: %v", err)
	}

	if _, ok, err := s.VerifyPassword("admin", "11"); err != nil || !ok {
		t.Fatalf("correct password: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.VerifyPassword("admin", "wrong"); err != nil || ok {
		t.Fatalf("wrong password should fail: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.VerifyPassword("ghost", "11"); err != nil || ok {
		t.Fatalf("missing user should fail without error: ok=%v err=%v", ok, err)
	}
}

func TestAdminUserStoreUpdatePasswordClearsMustChange(t *testing.T) {
	t.Parallel()
	s := newTestAdminUserStore(t)
	if _, err := s.EnsureSeed("admin", "11"); err != nil {
		t.Fatalf("EnsureSeed: %v", err)
	}
	user, _ := s.GetByUsername("admin")

	if err := s.UpdatePassword(user.ID, "new-strong-pass"); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}

	updated, _ := s.GetByUsername("admin")
	if updated.MustChangePassword {
		t.Error("must_change_password should be cleared after password change")
	}
	if _, ok, _ := s.VerifyPassword("admin", "new-strong-pass"); !ok {
		t.Error("new password should verify")
	}
	if _, ok, _ := s.VerifyPassword("admin", "11"); ok {
		t.Error("old password should no longer verify")
	}
}

func TestAdminUserStoreUpdateUsername(t *testing.T) {
	t.Parallel()
	s := newTestAdminUserStore(t)
	if _, err := s.EnsureSeed("admin", "11"); err != nil {
		t.Fatalf("EnsureSeed: %v", err)
	}
	user, _ := s.GetByUsername("admin")

	if err := s.UpdateUsername(user.ID, "operator"); err != nil {
		t.Fatalf("UpdateUsername: %v", err)
	}
	if _, err := s.GetByUsername("operator"); err != nil {
		t.Fatalf("expected renamed user: %v", err)
	}
	if _, err := s.GetByUsername("admin"); !errors.Is(err, ErrAdminUserNotFound) {
		t.Errorf("old username should be gone, got %v", err)
	}
}

func TestAdminUserStoreUpdateMissingUserErrors(t *testing.T) {
	t.Parallel()
	s := newTestAdminUserStore(t)
	if err := s.UpdatePassword("nonexistent", "x"); !errors.Is(err, ErrAdminUserNotFound) {
		t.Errorf("UpdatePassword on missing user = %v, want ErrAdminUserNotFound", err)
	}
}

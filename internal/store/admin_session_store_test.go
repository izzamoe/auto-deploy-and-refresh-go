package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newTestSessionStore(t *testing.T) *AdminSessionStore {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	s, err := NewAdminSessionStore(db)
	if err != nil {
		t.Fatalf("NewAdminSessionStore: %v", err)
	}
	return s
}

func sampleSession(id string, now time.Time) *AdminSession {
	return &AdminSession{
		ID: id, UserID: "u1", CreatedAt: now, LastSeenAt: now,
		ExpiresAt: now.Add(12 * time.Hour), AbsoluteExpiresAt: now.Add(7 * 24 * time.Hour),
		IP: "203.0.113.7", UserAgent: "ua",
	}
}

func TestAdminSession_CreateGetRoundTrip(t *testing.T) {
	s := newTestSessionStore(t)
	now := time.Date(2026, 11, 25, 8, 0, 0, 123456789, time.UTC)
	if err := s.Create(sampleSession("sid1", now)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get("sid1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.UserID != "u1" || got.IP != "203.0.113.7" || got.UserAgent != "ua" || got.RevokedAt != nil {
		t.Errorf("unexpected row: %+v", got)
	}
	if !got.ExpiresAt.Equal(now.Add(12*time.Hour)) || !got.AbsoluteExpiresAt.Equal(now.Add(7*24*time.Hour)) {
		t.Errorf("times did not round-trip: exp=%v aexp=%v", got.ExpiresAt, got.AbsoluteExpiresAt)
	}
	if _, err := s.Get("nope"); !errors.Is(err, ErrAdminSessionNotFound) {
		t.Errorf("unknown id: err = %v, want ErrAdminSessionNotFound", err)
	}
}

func TestAdminSession_RenewAndRevoke(t *testing.T) {
	s := newTestSessionStore(t)
	now := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	if err := s.Create(sampleSession("sid1", now)); err != nil {
		t.Fatal(err)
	}
	newExp := now.Add(20 * time.Hour)
	if err := s.Renew("sid1", newExp, now.Add(time.Hour)); err != nil {
		t.Fatalf("Renew: %v", err)
	}
	got, _ := s.Get("sid1")
	if !got.ExpiresAt.Equal(newExp) || !got.LastSeenAt.Equal(now.Add(time.Hour)) {
		t.Errorf("renew not applied: %+v", got)
	}

	revokedAt := now.Add(2 * time.Hour)
	if err := s.Revoke("sid1", revokedAt); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	// Revoking again is idempotent and keeps the first timestamp.
	if err := s.Revoke("sid1", revokedAt.Add(time.Hour)); err != nil {
		t.Fatalf("second Revoke: %v", err)
	}
	got, _ = s.Get("sid1")
	if got.RevokedAt == nil || !got.RevokedAt.Equal(revokedAt) {
		t.Fatalf("revoked_at = %v, want %v", got.RevokedAt, revokedAt)
	}
	// A revoked session can no longer be extended.
	if err := s.Renew("sid1", newExp.Add(time.Hour), now); !errors.Is(err, ErrAdminSessionNotFound) {
		t.Errorf("Renew of a revoked session: err = %v, want ErrAdminSessionNotFound", err)
	}
	if err := s.Revoke("ghost", revokedAt); !errors.Is(err, ErrAdminSessionNotFound) {
		t.Errorf("Revoke unknown: err = %v, want ErrAdminSessionNotFound", err)
	}
}

func TestAdminSession_DeleteStale(t *testing.T) {
	s := newTestSessionStore(t)
	now := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)

	old := sampleSession("old", now.Add(-30*24*time.Hour)) // absolute expiry 23 days ago
	fresh := sampleSession("fresh", now)
	revokedOld := sampleSession("revoked-old", now)
	revokedRecent := sampleSession("revoked-recent", now)
	for _, sess := range []*AdminSession{old, fresh, revokedOld, revokedRecent} {
		if err := s.Create(sess); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Revoke("revoked-old", now.Add(-10*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke("revoked-recent", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	n, err := s.DeleteStale(now.Add(-7 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("DeleteStale: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted %d rows, want 2 (old + revoked-old)", n)
	}
	for id, want := range map[string]bool{"old": false, "revoked-old": false, "fresh": true, "revoked-recent": true} {
		_, err := s.Get(id)
		if (err == nil) != want {
			t.Errorf("session %s present=%v, want %v", id, err == nil, want)
		}
	}
}

func TestAdminSession_WorksWithoutMigrate(t *testing.T) {
	// The constructor creates the table itself, with the same DDL as migration v1.
	s := newTestSessionStore(t)
	if err := s.Create(sampleSession("sid1", time.Now())); err != nil {
		t.Fatalf("Create on a fresh database: %v", err)
	}
}

func TestAdminUser_TokenVersionBumpsOnCredentialChange(t *testing.T) {
	s := newTestAdminUserStore(t)
	u, err := s.CreateUser("admin", "first-password", true)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.TokenVersion != 0 || !u.MustChangePassword {
		t.Fatalf("new user: %+v", u)
	}
	byID, err := s.GetByID(u.ID)
	if err != nil || byID.Username != "admin" {
		t.Fatalf("GetByID: %v %+v", err, byID)
	}
	if _, err := s.GetByID("missing"); !errors.Is(err, ErrAdminUserNotFound) {
		t.Errorf("GetByID(missing): err = %v, want ErrAdminUserNotFound", err)
	}

	if err := s.UpdateUsername(u.ID, "renamed"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetByID(u.ID); got.TokenVersion != 1 || !got.MustChangePassword {
		t.Errorf("after rename: version=%d mustChange=%v, want 1/true (B3: rename must not clear the gate)", got.TokenVersion, got.MustChangePassword)
	}
	if err := s.UpdatePassword(u.ID, "second-password"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetByID(u.ID); got.TokenVersion != 2 || got.MustChangePassword {
		t.Errorf("after password change: version=%d mustChange=%v, want 2/false", got.TokenVersion, got.MustChangePassword)
	}
	if err := s.BumpTokenVersion(u.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetByID(u.ID); got.TokenVersion != 3 {
		t.Errorf("after revoke-all: version=%d, want 3", got.TokenVersion)
	}
	if err := s.BumpTokenVersion("missing"); !errors.Is(err, ErrAdminUserNotFound) {
		t.Errorf("BumpTokenVersion(missing): err = %v", err)
	}
}

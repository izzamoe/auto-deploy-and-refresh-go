package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

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

// newTestAdminUserStoreWithUser returns a store holding one account "admin" with
// the given password (must-change flag set, like a fresh install).
func newTestAdminUserStoreWithUser(t *testing.T, password string) *AdminUserStore {
	t.Helper()
	s := newTestAdminUserStore(t)
	if _, err := s.CreateUser("admin", password, true); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return s
}

var alnum24 = regexp.MustCompile(`^[A-Za-z0-9]{24}$`)

func TestEnsureInitialAdmin_CreatesRandomAccountAndOwnerOnlyFile(t *testing.T) {
	t.Parallel()
	s := newTestAdminUserStore(t)
	dir := t.TempDir()

	created, path, err := s.EnsureInitialAdmin(dir)
	if err != nil {
		t.Fatalf("EnsureInitialAdmin: %v", err)
	}
	if !created {
		t.Fatal("expected the first call on an empty database to create the account")
	}
	if want := filepath.Join(dir, InitialAdminPasswordFile); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("password file missing: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("password file mode = %v, want 0600", fi.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	password := strings.TrimSpace(string(raw))
	if !alnum24.MatchString(password) {
		t.Errorf("initial password %q is not 24 alphanumeric characters", password)
	}
	if password == "11" || password == "hehe" {
		t.Errorf("B5/FR-009: a well-known default password was generated: %q", password)
	}

	user, err := s.GetByUsername(InitialAdminUsername)
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if !user.MustChangePassword {
		t.Error("the initial account must be flagged must_change_password")
	}
	if user.PasswordHash == password || user.PasswordHash == "" {
		t.Error("the password must be stored hashed")
	}
	if _, ok, err := s.VerifyPassword(InitialAdminUsername, password); err != nil || !ok {
		t.Errorf("the password in the file must log in: ok=%v err=%v", ok, err)
	}
}

func TestEnsureInitialAdmin_IsIdempotentAndNeverOverwrites(t *testing.T) {
	t.Parallel()
	s := newTestAdminUserStore(t)
	dir := t.TempDir()
	if _, _, err := s.EnsureInitialAdmin(dir); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, InitialAdminPasswordFile))

	created, path, err := s.EnsureInitialAdmin(dir)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if created || path != "" {
		t.Errorf("second call must be a no-op, got created=%v path=%q", created, path)
	}
	after, _ := os.ReadFile(filepath.Join(dir, InitialAdminPasswordFile))
	if string(before) != string(after) {
		t.Error("the existing password file must not be rewritten")
	}
}

// FR-009: upgrading an install that already has an account must not create another
// one or leave a password file lying around.
func TestEnsureInitialAdmin_ExistingUsersAreLeftAlone(t *testing.T) {
	t.Parallel()
	s := newTestAdminUserStoreWithUser(t, "an-existing-password")
	dir := t.TempDir()

	created, _, err := s.EnsureInitialAdmin(dir)
	if err != nil || created {
		t.Fatalf("created=%v err=%v, want false/nil", created, err)
	}
	if _, err := os.Stat(filepath.Join(dir, InitialAdminPasswordFile)); !os.IsNotExist(err) {
		t.Errorf("no password file may be written for an existing install (stat err = %v)", err)
	}
	if _, ok, _ := s.VerifyPassword("admin", "an-existing-password"); !ok {
		t.Error("the existing account must be untouched")
	}
}

func TestEnsureInitialAdmin_PasswordsDifferPerInstall(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		s := newTestAdminUserStore(t)
		dir := t.TempDir()
		if _, _, err := s.EnsureInitialAdmin(dir); err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(filepath.Join(dir, InitialAdminPasswordFile))
		if seen[string(raw)] {
			t.Fatalf("password %q generated twice", raw)
		}
		seen[string(raw)] = true
	}
}

func TestEnsureInitialAdmin_FailedInsertLeavesNoPasswordFile(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "admin.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewAdminUserStore(db)
	if err != nil {
		t.Fatal(err)
	}
	// The count succeeds (empty table) and the file gets written, then the insert is
	// aborted: the cleanup path must remove the file again.
	if _, err := db.Exec(`CREATE TRIGGER refuse_insert BEFORE INSERT ON admin_users BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, _, err := s.EnsureInitialAdmin(dir); err == nil {
		t.Fatal("expected an error when the account cannot be stored")
	}
	if _, err := os.Stat(filepath.Join(dir, InitialAdminPasswordFile)); !os.IsNotExist(err) {
		t.Errorf("a password for an account that was never stored must not be left on disk (stat err = %v)", err)
	}
	db.Close()
}

func TestAdminUserStoreVerifyPassword(t *testing.T) {
	t.Parallel()
	s := newTestAdminUserStoreWithUser(t, "s3cret-pass")

	if _, ok, err := s.VerifyPassword("admin", "s3cret-pass"); err != nil || !ok {
		t.Fatalf("correct password: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.VerifyPassword("admin", "wrong"); err != nil || ok {
		t.Fatalf("wrong password should fail: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.VerifyPassword("ghost", "s3cret-pass"); err != nil || ok {
		t.Fatalf("missing user should fail without error: ok=%v err=%v", ok, err)
	}
}

func TestAdminUserStoreUpdatePasswordClearsMustChange(t *testing.T) {
	t.Parallel()
	s := newTestAdminUserStoreWithUser(t, "first-pass")
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
	if _, ok, _ := s.VerifyPassword("admin", "first-pass"); ok {
		t.Error("old password should no longer verify")
	}
}

func TestAdminUserStoreUpdateUsername(t *testing.T) {
	t.Parallel()
	s := newTestAdminUserStoreWithUser(t, "first-pass")
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

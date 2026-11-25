package auth

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/izzamoe/auto-deploy/internal/store"
)

// ---- in-memory backends and clock -------------------------------------------------

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock {
	return &testClock{t: time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeBackend implements UserSource, SessionSource and TokenSource in memory.
type fakeBackend struct {
	mu       sync.Mutex
	users    map[string]*store.AdminUser
	sessions map[string]*store.AdminSession
	tokens   map[string]*store.APIToken // keyed by token hash
	used     map[string]time.Time
	userErr  error // returned by GetByID instead of a lookup (simulates a DB failure)
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		users:    map[string]*store.AdminUser{},
		sessions: map[string]*store.AdminSession{},
		tokens:   map[string]*store.APIToken{},
		used:     map[string]time.Time{},
	}
}

func (b *fakeBackend) GetByID(id string) (*store.AdminUser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.userErr != nil {
		return nil, b.userErr
	}
	u, ok := b.users[id]
	if !ok {
		return nil, store.ErrAdminUserNotFound
	}
	cp := *u
	return &cp, nil
}

func (b *fakeBackend) Create(s *store.AdminSession) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	cp := *s
	b.sessions[s.ID] = &cp
	return nil
}

func (b *fakeBackend) Get(id string) (*store.AdminSession, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.sessions[id]
	if !ok {
		return nil, store.ErrAdminSessionNotFound
	}
	cp := *s
	return &cp, nil
}

func (b *fakeBackend) Renew(id string, expiresAt, seenAt time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.sessions[id]
	if !ok {
		return store.ErrAdminSessionNotFound
	}
	s.ExpiresAt, s.LastSeenAt = expiresAt, seenAt
	return nil
}

func (b *fakeBackend) Revoke(id string, at time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.sessions[id]
	if !ok {
		return store.ErrAdminSessionNotFound
	}
	s.RevokedAt = &at
	return nil
}

func (b *fakeBackend) FindActiveByHash(hash string) (*store.APIToken, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tokens[hash]
	if !ok || t.RevokedAt != nil {
		return nil, store.ErrAPITokenNotFound
	}
	cp := *t
	return &cp, nil
}

func (b *fakeBackend) MarkUsed(id string, at time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.used[id] = at
	return nil
}

func (b *fakeBackend) setUser(mut func(u *store.AdminUser)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	mut(b.users["u1"])
}

// addToken registers a new API token for u1 and returns its full value.
func (b *fakeBackend) addToken(t *testing.T) (full string, id string) {
	t.Helper()
	full, hash, prefix, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	id = "tok1"
	b.mu.Lock()
	b.tokens[hash] = &store.APIToken{ID: id, UserID: "u1", Name: "ci", TokenHash: hash, Prefix: prefix, CreatedAt: time.Now()}
	b.mu.Unlock()
	return full, id
}

type testEnv struct {
	be  *fakeBackend
	clk *testClock
	mgr *Manager
}

var testKey = bytes.Repeat([]byte{0x42}, 32)

func newTestEnv(t *testing.T, mustChange bool) *testEnv {
	t.Helper()
	be := newFakeBackend()
	be.users["u1"] = &store.AdminUser{ID: "u1", Username: "admin", MustChangePassword: mustChange}
	clk := newTestClock()
	mgr, err := NewManager(SessionConfig{Key: testKey, Idle: DefaultIdle, Absolute: DefaultAbsolute, Secure: true, Now: clk.Now}, be, be)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return &testEnv{be: be, clk: clk, mgr: mgr}
}

func (e *testEnv) issue(t *testing.T) *Issued {
	t.Helper()
	u, _ := e.be.GetByID("u1")
	iss, err := e.mgr.Issue(u, "203.0.113.9", "test-agent")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss == nil || iss.Token == "" || iss.SessionID == "" {
		t.Fatalf("Issue returned empty session: %+v", iss)
	}
	return iss
}

// ---- key file (T026) ---------------------------------------------------------------

func TestKeyFile_CreatesOwnerOnly32Bytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "session.key")
	key, regenerated, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("LoadOrCreateKey: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("key length = %d, want 32", len(key))
	}
	if regenerated {
		t.Error("a missing file is a first start, not a regeneration")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", fi.Mode().Perm())
	}
	again, regenerated, err := LoadOrCreateKey(path)
	if err != nil || regenerated || !bytes.Equal(key, again) {
		t.Errorf("second load must return the same key (err=%v regenerated=%v equal=%v)", err, regenerated, bytes.Equal(key, again))
	}
}

func TestKeyFile_RegeneratesWhenCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.key")
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, regenerated, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("LoadOrCreateKey: %v", err)
	}
	if !regenerated || len(key) != 32 {
		t.Fatalf("corrupt file must be regenerated to 32 bytes (regenerated=%v len=%d)", regenerated, len(key))
	}
	onDisk, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(onDisk, key) {
		t.Errorf("regenerated key must be persisted (err=%v)", err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("regenerated file mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestKeyFile_TightensLooseMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.key")
	want := bytes.Repeat([]byte{9}, 32)
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatal(err)
	}
	key, regenerated, err := LoadOrCreateKey(path)
	if err != nil || regenerated || !bytes.Equal(key, want) {
		t.Fatalf("valid key must be kept (err=%v regenerated=%v)", err, regenerated)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("loose mode must be tightened to 0600, got %v", fi.Mode().Perm())
	}
}

func TestKeyFile_UnwritableDirFails(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreateKey(filepath.Join(blocker, "session.key")); err == nil {
		t.Fatal("expected an error when the key cannot be stored")
	}
}

// ---- sessions (T027) ---------------------------------------------------------------

func TestSessionSurvivesRestartWithPersistentKey(t *testing.T) {
	be := newFakeBackend()
	be.users["u1"] = &store.AdminUser{ID: "u1", Username: "admin"}
	clk := newTestClock()
	path := filepath.Join(t.TempDir(), "session.key")

	key1, _, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("LoadOrCreateKey: %v", err)
	}
	m1, err := NewManager(SessionConfig{Key: key1, Idle: DefaultIdle, Absolute: DefaultAbsolute, Secure: true, Now: clk.Now}, be, be)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := be.GetByID("u1")
	iss, err := m1.Issue(u, "198.51.100.1", "ua")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// "Restart": a brand new Manager that only knows the key file and the database.
	key2, regenerated, err := LoadOrCreateKey(path)
	if err != nil || regenerated {
		t.Fatalf("key reload err=%v regenerated=%v", err, regenerated)
	}
	m2, err := NewManager(SessionConfig{Key: key2, Idle: DefaultIdle, Absolute: DefaultAbsolute, Secure: true, Now: clk.Now}, be, be)
	if err != nil {
		t.Fatal(err)
	}
	v, err := m2.Verify(iss.Token)
	if err != nil {
		t.Fatalf("session issued before restart must verify after restart: %v", err)
	}
	if v.User == nil || v.User.ID != "u1" || v.SessionID != iss.SessionID {
		t.Errorf("unexpected verified identity: %+v", v)
	}

	// A different key (e.g. a regenerated one) must not verify the old token.
	m3, _ := NewManager(SessionConfig{Key: bytes.Repeat([]byte{1}, 32), Idle: DefaultIdle, Absolute: DefaultAbsolute, Secure: true, Now: clk.Now}, be, be)
	if _, err := m3.Verify(iss.Token); err != ErrSessionExpired {
		t.Errorf("token signed with another key: err = %v, want ErrSessionExpired", err)
	}
}

func TestSlidingRenewal(t *testing.T) {
	e := newTestEnv(t, false)
	iss := e.issue(t)
	t0 := e.clk.Now()

	// 5h in: 7h of idle time left (> idle/2) -> no renewal.
	e.clk.Advance(5 * time.Hour)
	v, err := e.mgr.Verify(iss.Token)
	if err != nil {
		t.Fatalf("Verify at +5h: %v", err)
	}
	if v.Renewed != nil {
		t.Fatal("must not renew while more than half of the idle window is left")
	}

	// 7h in: 5h left (< idle/2) -> renewed to now+idle, and the row is extended.
	e.clk.Advance(2 * time.Hour)
	v, err = e.mgr.Verify(iss.Token)
	if err != nil {
		t.Fatalf("Verify at +7h: %v", err)
	}
	if v.Renewed == nil {
		t.Fatal("expected a renewed cookie once less than half of the idle window is left")
	}
	wantExp := e.clk.Now().Add(DefaultIdle)
	if !v.Renewed.ExpiresAt.Equal(wantExp) {
		t.Errorf("renewed exp = %v, want %v", v.Renewed.ExpiresAt, wantExp)
	}
	if v.Renewed.SessionID != iss.SessionID {
		t.Error("renewal must keep the session id")
	}
	row, _ := e.be.Get(iss.SessionID)
	if !row.ExpiresAt.Equal(wantExp) {
		t.Errorf("row expires_at = %v, want %v", row.ExpiresAt, wantExp)
	}
	if !v.Renewed.AbsoluteExpiresAt.Equal(t0.Add(DefaultAbsolute)) {
		t.Errorf("absolute expiry must not slide: %v", v.Renewed.AbsoluteExpiresAt)
	}

	// Near the absolute limit the renewed expiry is clamped to it.
	// Keep the session alive with a request every 7h; expiry must never pass the
	// absolute limit and must be clamped to it near the end.
	aexp := t0.Add(DefaultAbsolute)
	tok := v.Renewed.Token
	clamped := false
	for e.clk.Now().Add(7 * time.Hour).Before(aexp) {
		e.clk.Advance(7 * time.Hour)
		v, err = e.mgr.Verify(tok)
		if err != nil {
			t.Fatalf("Verify at %v: %v", e.clk.Now().Sub(t0), err)
		}
		if v.Renewed == nil {
			continue
		}
		tok = v.Renewed.Token
		if v.Renewed.ExpiresAt.After(aexp) {
			t.Fatalf("renewed exp %v exceeds the absolute limit %v", v.Renewed.ExpiresAt, aexp)
		}
		if v.Renewed.ExpiresAt.Equal(aexp) {
			clamped = true
		}
	}
	if !clamped {
		t.Error("expected a renewal clamped to the absolute limit near the end of the session")
	}

	// Past the absolute limit the session is over, whatever the last renewal said.
	e.clk.Advance(aexp.Sub(e.clk.Now()) + time.Second)
	if _, err := e.mgr.Verify(tok); err != ErrSessionExpired {
		t.Errorf("past the absolute limit: err = %v, want ErrSessionExpired", err)
	}
}

func TestTokenVersionRevokesOldTokens(t *testing.T) {
	e := newTestEnv(t, false)
	iss := e.issue(t)
	if _, err := e.mgr.Verify(iss.Token); err != nil {
		t.Fatalf("fresh token must verify: %v", err)
	}

	e.be.setUser(func(u *store.AdminUser) { u.TokenVersion++ })
	if _, err := e.mgr.Verify(iss.Token); err != ErrSessionRevoked {
		t.Fatalf("token issued before the version bump: err = %v, want ErrSessionRevoked", err)
	}

	// The current session is reissued with the new version and works again.
	u, _ := e.be.GetByID("u1")
	again, err := e.mgr.Reissue(iss.SessionID, u)
	if err != nil {
		t.Fatalf("Reissue: %v", err)
	}
	if _, err := e.mgr.Verify(again.Token); err != nil {
		t.Errorf("reissued token must verify: %v", err)
	}
}

func TestLogoutRevokesServerSide(t *testing.T) {
	e := newTestEnv(t, false)
	iss := e.issue(t)
	if _, err := e.mgr.Verify(iss.Token); err != nil {
		t.Fatalf("fresh token must verify: %v", err)
	}
	if err := e.mgr.Revoke(iss.SessionID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	row, err := e.be.Get(iss.SessionID)
	if err != nil || row.RevokedAt == nil {
		t.Fatalf("the session row must be marked revoked server-side (err=%v row=%+v)", err, row)
	}
	// The same cookie value still exists in the browser/attacker's hands: it must be dead.
	if _, err := e.mgr.Verify(iss.Token); err != ErrSessionRevoked {
		t.Errorf("replaying a logged-out token: err = %v, want ErrSessionRevoked", err)
	}
}

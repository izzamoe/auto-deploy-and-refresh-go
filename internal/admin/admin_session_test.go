package admin

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/izzamoe/auto-deploy/internal/auth"
	"github.com/izzamoe/auto-deploy/internal/config"
	"github.com/izzamoe/auto-deploy/internal/httpx"
	"github.com/izzamoe/auto-deploy/internal/store"
)

const sessionTestPassword = "correct-horse-battery"

// sessionEnv is a self-contained login stack: its own database, session key and limiter.
type sessionEnv struct {
	h        *server.Hertz
	users    *store.AdminUserStore
	sessions *store.AdminSessionStore
	mgr      *auth.Manager
	cfg      *config.ServiceConfig
}

func newSessionEnv(t *testing.T, cfg *config.ServiceConfig) *sessionEnv {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	// Migrate ALTERs apps/deploy_jobs, so their stores must exist first (as in main.go).
	q, err := store.NewDeployQueue(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NewAppStore(db); err != nil {
		t.Fatal(err)
	}
	users, err := store.NewAdminUserStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	sessions, err := store.NewAdminSessionStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.CreateUser("admin", sessionTestPassword, false); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	mgr, err := auth.NewManager(auth.SessionConfig{
		Key: bytes.Repeat([]byte{5}, 32), Idle: auth.DefaultIdle, Absolute: auth.DefaultAbsolute, Secure: cfg.CookieSecure,
	}, users, sessions)
	if err != nil {
		t.Fatal(err)
	}
	limiter := auth.NewLoginLimiter(nil)
	resolver := auth.NewClientIPResolver(nil)
	h := server.New()
	h.Use(httpx.RequestID())
	mw := auth.Middleware(auth.MiddlewareConfig{
		Sessions: mgr, Tokens: nil, TokenLimiter: auth.NewWindow(10, time.Minute, nil), ClientIP: resolver,
	})
	RegisterSessionRoutesHertz(h, NewSessionHandler(SessionHandlerConfig{
		Sessions: mgr, Users: users, Limiter: limiter, ClientIP: resolver,
	}), mw)
	return &sessionEnv{h: h, users: users, sessions: sessions, mgr: mgr, cfg: cfg}
}

// defaultConfig is what a start with no environment gets from the loader.
func defaultConfig(t *testing.T) *config.ServiceConfig {
	t.Helper()
	t.Setenv("COOKIE_SECURE", "")
	cfg, err := config.LoadServiceConfig()
	if err != nil {
		t.Fatalf("LoadServiceConfig: %v", err)
	}
	return cfg
}

type loginResult struct {
	status     int
	body       string
	setCookie  string
	retryAfter string
}

func (e *sessionEnv) login(user, pass string) loginResult {
	payload, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	w := ut.PerformRequest(e.h.Engine, "POST", "http://app.test/admin/api/v1/session",
		&ut.Body{Body: bytes.NewReader(payload), Len: len(payload)},
		ut.Header{Key: "Content-Type", Value: "application/json"},
		ut.Header{Key: "X-Admin-Request", Value: "true"},
		ut.Header{Key: "Sec-Fetch-Site", Value: "same-origin"})
	r := w.Result()
	return loginResult{
		status:     r.StatusCode(),
		body:       string(r.Body()),
		setCookie:  string(r.Header.Peek("Set-Cookie")),
		retryAfter: string(r.Header.Peek("Retry-After")),
	}
}

func TestCookieSecureByDefault(t *testing.T) {
	e := newSessionEnv(t, defaultConfig(t))
	r := e.login("admin", sessionTestPassword)
	if r.status != 200 {
		t.Fatalf("login status = %d body=%s", r.status, r.body)
	}
	if !e.cfg.CookieSecure {
		t.Error("B6/FR-010: COOKIE_SECURE unset must default to Secure=true")
	}
	sc := r.setCookie
	if !strings.HasPrefix(sc, "__Host-admin_session=") {
		t.Errorf("Set-Cookie must use the __Host- prefix when Secure: %q", sc)
	}
	for _, attr := range []string{"Secure", "HttpOnly", "SameSite=Strict", "Path=/"} {
		if !strings.Contains(sc, attr) {
			t.Errorf("Set-Cookie lacks %q: %q", attr, sc)
		}
	}
	if strings.Contains(strings.ToLower(sc), "domain=") {
		t.Errorf("__Host- cookies must not set Domain: %q", sc)
	}

	// Explicit opt-out (plain HTTP development) drops Secure and the prefix.
	t.Setenv("COOKIE_SECURE", "false")
	devCfg, err := config.LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if devCfg.CookieSecure {
		t.Fatal("COOKIE_SECURE=false must disable Secure")
	}
	d := newSessionEnv(t, devCfg).login("admin", sessionTestPassword)
	if !strings.HasPrefix(d.setCookie, "admin_session=") || strings.Contains(d.setCookie, "Secure") {
		t.Errorf("dev cookie must be admin_session without Secure: %q", d.setCookie)
	}
}

func TestLoginRateLimited(t *testing.T) {
	e := newSessionEnv(t, defaultConfig(t))

	// 5 failures per (IP, username) are answered 401; the 6th attempt is blocked
	// even with the right password.
	for i := 0; i < 5; i++ {
		if r := e.login("admin", "wrong"); r.status != 401 {
			t.Fatalf("failure %d: status = %d body=%s, want 401", i+1, r.status, r.body)
		}
	}
	r := e.login("admin", sessionTestPassword)
	if r.status != 429 {
		t.Fatalf("6th attempt: status = %d, want 429 (B5)", r.status)
	}
	secs, err := strconv.Atoi(r.retryAfter)
	if err != nil || secs <= 0 {
		t.Errorf("429 must carry a positive Retry-After, got %q", r.retryAfter)
	}
	var eb struct{ Code string }
	if err := json.Unmarshal([]byte(r.body), &eb); err != nil || eb.Code != "rate_limited" {
		t.Errorf("429 body = %s, want code rate_limited", r.body)
	}
	// Another username from the same IP is a different (IP, username) bucket.
	if r := e.login("someone-else", "wrong"); r.status != 401 {
		t.Errorf("other username: status = %d, want 401", r.status)
	}

	// Per-IP ceiling: 20 attempts per minute whatever the username.
	f := newSessionEnv(t, defaultConfig(t))
	var codes []int
	for i := 0; i < 25; i++ {
		codes = append(codes, f.login("user"+strconv.Itoa(i), "x").status)
	}
	for i := 0; i < 20; i++ {
		if codes[i] != 401 {
			t.Fatalf("attempt %d: status = %d, want 401 (histogram %v)", i+1, codes[i], codes)
		}
	}
	for i := 20; i < 25; i++ {
		if codes[i] != 429 {
			t.Errorf("attempt %d beyond 20/min per IP: status = %d, want 429", i+1, codes[i])
		}
	}
}

// B29: verifying an unknown user must cost about as much as verifying a known one
// (a bcrypt comparison against a dummy hash), so login timing does not reveal usernames.
func TestLoginTimingUnknownUserUsesDummyHash(t *testing.T) {
	e := newSessionEnv(t, defaultConfig(t))
	measure := func(user string) time.Duration {
		start := time.Now()
		if r := e.login(user, "wrong-password"); r.status != 401 {
			t.Fatalf("login(%s) status = %d, want 401", user, r.status)
		}
		return time.Since(start)
	}
	var known, unknown []time.Duration
	for i := 0; i < 3; i++ {
		known = append(known, measure("admin"))
		unknown = append(unknown, measure("ghost"+strconv.Itoa(i)))
	}
	sort.Slice(known, func(i, j int) bool { return known[i] < known[j] })
	sort.Slice(unknown, func(i, j int) bool { return unknown[i] < unknown[j] })
	k, u := known[1], unknown[1] // medians
	t.Logf("median wrong-password login: known user %v, unknown user %v", k, u)
	if u < k/2 {
		t.Errorf("B29: unknown-user login (%v) is much faster than known-user login (%v): no dummy hash comparison", u, k)
	}
}

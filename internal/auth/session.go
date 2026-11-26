package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/izzamoe/auto-deploy/internal/store"
)

// Session lifetimes (Q-2).
const (
	DefaultIdle     = 12 * time.Hour
	DefaultAbsolute = 7 * 24 * time.Hour

	// touchInterval throttles last_seen_at writes (data-model §1.2).
	touchInterval = time.Minute
	maxUserAgent  = 256

	cookieNameSecure = "__Host-admin_session"
	cookieNamePlain  = "admin_session"
)

var (
	// ErrSessionExpired: token malformed, badly signed, past exp/aexp, no row, or the
	// backing store failed (fail closed).
	ErrSessionExpired = errors.New("session expired")
	// ErrSessionRevoked: row revoked, user gone, or token_version changed.
	ErrSessionRevoked = errors.New("session revoked")
)

// SessionConfig configures a Manager.
type SessionConfig struct {
	Key      []byte
	Idle     time.Duration
	Absolute time.Duration
	Secure   bool
	Now      func() time.Time
}

// Issued is a freshly signed session cookie value.
type Issued struct {
	Token             string
	SessionID         string
	ExpiresAt         time.Time
	AbsoluteExpiresAt time.Time
}

// Verified is the outcome of a successful Verify.
type Verified struct {
	User              *store.AdminUser
	SessionID         string
	ExpiresAt         time.Time
	AbsoluteExpiresAt time.Time
	// Renewed is non-nil when the sliding window moved and a new cookie must be set.
	Renewed *Issued
	// TokenVersion is the token_version the verified cookie was signed with.
	TokenVersion int
}

// claims is the JWT payload (D5). sub is the stable user ID, never the username.
type claims struct {
	Sub  string `json:"sub"`
	Sid  string `json:"sid"`
	Ver  int    `json:"ver"`
	Iat  int64  `json:"iat"`
	Exp  int64  `json:"exp"`
	Aexp int64  `json:"aexp"`
}

// jwtHeader is the only header accepted: a fixed alg prevents algorithm confusion.
var jwtHeader = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

// Manager issues and verifies session cookies.
type Manager struct {
	key      []byte
	idle     time.Duration
	absolute time.Duration
	secure   bool
	now      func() time.Time
	users    UserSource
	sessions SessionSource

	touchMu sync.Mutex
	touched map[string]time.Time

	subMu sync.Mutex
	subs  map[chan struct{}]struct{}
}

// NewManager builds a Manager. Idle/Absolute default to Q-2 values.
func NewManager(cfg SessionConfig, users UserSource, sessions SessionSource) (*Manager, error) {
	if len(cfg.Key) < keyLen {
		return nil, fmt.Errorf("auth: session key must be at least %d bytes, got %d", keyLen, len(cfg.Key))
	}
	if users == nil || sessions == nil {
		return nil, errors.New("auth: user and session sources are required")
	}
	m := &Manager{
		key:      append([]byte(nil), cfg.Key...),
		idle:     cfg.Idle,
		absolute: cfg.Absolute,
		secure:   cfg.Secure,
		now:      cfg.Now,
		users:    users,
		sessions: sessions,
		touched:  map[string]time.Time{},
		subs:     map[chan struct{}]struct{}{},
	}
	if m.idle <= 0 {
		m.idle = DefaultIdle
	}
	if m.absolute <= 0 {
		m.absolute = DefaultAbsolute
	}
	if m.now == nil {
		m.now = time.Now
	}
	return m, nil
}

// Users returns the user source the Manager verifies sessions against.
func (m *Manager) Users() UserSource { return m.users }

// CookieName is __Host-admin_session when Secure, admin_session otherwise (FR-010).
func (m *Manager) CookieName() string {
	if m.secure {
		return cookieNameSecure
	}
	return cookieNamePlain
}

// Issue creates a session row and signs its cookie value.
func (m *Manager) Issue(user *store.AdminUser, ip, userAgent string) (*Issued, error) {
	id, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	now := m.now().UTC()
	if len(userAgent) > maxUserAgent {
		userAgent = userAgent[:maxUserAgent]
	}
	row := &store.AdminSession{
		ID:                id,
		UserID:            user.ID,
		CreatedAt:         now,
		LastSeenAt:        now,
		ExpiresAt:         now.Add(m.idle),
		AbsoluteExpiresAt: now.Add(m.absolute),
		IP:                ip,
		UserAgent:         userAgent,
	}
	if err := m.sessions.Create(row); err != nil {
		return nil, fmt.Errorf("auth: create session: %w", err)
	}
	return m.sign(user, row, now, row.ExpiresAt)
}

// Reissue signs a new cookie value for an existing session with the user's
// current token_version (used after password/username change and "revoke all":
// the current session survives, every other one is rejected).
func (m *Manager) Reissue(sessionID string, user *store.AdminUser) (*Issued, error) {
	row, err := m.sessions.Get(sessionID)
	if err != nil {
		return nil, fmt.Errorf("auth: reissue: %w", err)
	}
	now := m.now().UTC()
	if row.RevokedAt != nil || row.UserID != user.ID || !now.Before(row.AbsoluteExpiresAt) {
		return nil, ErrSessionRevoked
	}
	exp := minTime(now.Add(m.idle), row.AbsoluteExpiresAt)
	if err := m.sessions.Renew(sessionID, exp, now); err != nil {
		return nil, fmt.Errorf("auth: reissue: %w", err)
	}
	m.notify() // token_version moved: sockets of the older cookies must re-check
	return m.sign(user, row, now, exp)
}

// Verify checks a cookie value against the key, the session row and the user
// (data-model §5): signature -> exp/aexp -> row exists and is not revoked -> user
// exists (fail closed) -> token_version matches.
func (m *Manager) Verify(token string) (*Verified, error) {
	return m.verify(token, true)
}

// VerifyNoRenew is Verify for a request that must not keep the session alive (T089 CR-3,
// Q-2=A, FR-031): a refetch the panel makes on its own because of an event, or a WebSocket
// handshake. Every check of Verify applies (an expired or revoked session fails the same
// way); only the sliding renewal and the last-seen touch are skipped.
func (m *Manager) VerifyNoRenew(token string) (*Verified, error) {
	return m.verify(token, false)
}

func (m *Manager) verify(token string, renew bool) (*Verified, error) {
	c, err := m.parse(token)
	if err != nil {
		return nil, ErrSessionExpired
	}
	now := m.now().UTC()
	if !now.Before(time.Unix(c.Exp, 0)) || !now.Before(time.Unix(c.Aexp, 0)) {
		return nil, ErrSessionExpired
	}

	row, err := m.sessions.Get(c.Sid)
	if err != nil {
		if errors.Is(err, store.ErrAdminSessionNotFound) {
			return nil, ErrSessionExpired
		}
		slog.Error("auth: session lookup failed; denying request", "err", err)
		return nil, fmt.Errorf("%w: backend error", ErrSessionExpired)
	}
	if row.RevokedAt != nil || row.UserID != c.Sub {
		return nil, ErrSessionRevoked
	}
	if !now.Before(row.ExpiresAt) || !now.Before(row.AbsoluteExpiresAt) {
		return nil, ErrSessionExpired
	}

	user, err := m.users.GetByID(c.Sub)
	if err != nil {
		if errors.Is(err, store.ErrAdminUserNotFound) {
			return nil, ErrSessionRevoked
		}
		slog.Error("auth: user lookup failed; denying request", "err", err)
		return nil, fmt.Errorf("%w: backend error", ErrSessionExpired)
	}
	if user.TokenVersion != c.Ver {
		return nil, ErrSessionRevoked
	}

	v := &Verified{User: user, SessionID: row.ID, ExpiresAt: row.ExpiresAt, AbsoluteExpiresAt: row.AbsoluteExpiresAt, TokenVersion: c.Ver}
	if !renew {
		return v, nil
	}

	// Sliding renewal: less than half of the idle window left -> push it forward.
	if row.ExpiresAt.Sub(now) < m.idle/2 {
		exp := minTime(now.Add(m.idle), row.AbsoluteExpiresAt)
		if exp.After(row.ExpiresAt) {
			if err := m.sessions.Renew(row.ID, exp, now); err != nil {
				// Not fatal: the current cookie is still valid; try again next request.
				slog.Warn("auth: session renewal failed", "err", err)
				return v, nil
			}
			iss, err := m.sign(user, row, now, exp)
			if err != nil {
				return nil, err
			}
			m.markTouched(row.ID, now)
			v.ExpiresAt, v.Renewed = exp, iss
			return v, nil
		}
	}
	m.touch(row, now)
	return v, nil
}

// Revoke marks the session row revoked (logout, FR-007).
func (m *Manager) Revoke(sessionID string) error {
	if err := m.sessions.Revoke(sessionID, m.now().UTC()); err != nil {
		return fmt.Errorf("auth: revoke session: %w", err)
	}
	m.touchMu.Lock()
	delete(m.touched, sessionID)
	m.touchMu.Unlock()
	m.notify()
	return nil
}

// Recheck re-validates a session that is already in use (an open WebSocket): the row
// must exist, not be revoked and not be past its idle/absolute expiry, and the user's
// token_version must still be the one the connection was authenticated with. Errors
// are ErrSessionExpired / ErrSessionRevoked; a backing-store failure fails closed.
func (m *Manager) Recheck(sessionID, userID string, tokenVersion int) error {
	now := m.now().UTC()
	row, err := m.sessions.Get(sessionID)
	if err != nil {
		if errors.Is(err, store.ErrAdminSessionNotFound) {
			return ErrSessionExpired
		}
		slog.Error("auth: session recheck failed; closing", "err", err)
		return fmt.Errorf("%w: backend error", ErrSessionExpired)
	}
	if row.RevokedAt != nil || row.UserID != userID {
		return ErrSessionRevoked
	}
	if !now.Before(row.ExpiresAt) || !now.Before(row.AbsoluteExpiresAt) {
		return ErrSessionExpired
	}
	user, err := m.users.GetByID(userID)
	if err != nil {
		if errors.Is(err, store.ErrAdminUserNotFound) {
			return ErrSessionRevoked
		}
		slog.Error("auth: user recheck failed; closing", "err", err)
		return fmt.Errorf("%w: backend error", ErrSessionExpired)
	}
	if user.TokenVersion != tokenVersion {
		return ErrSessionRevoked
	}
	return nil
}

// Subscribe returns a channel that receives a (coalesced) signal whenever sessions may
// have been invalidated in this process: logout/Revoke and Reissue (password or username
// change, "revoke all"). Holders then call Recheck. The returned func must be called
// when the holder is done.
func (m *Manager) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	m.subMu.Lock()
	m.subs[ch] = struct{}{}
	m.subMu.Unlock()
	return ch, func() {
		m.subMu.Lock()
		delete(m.subs, ch)
		m.subMu.Unlock()
	}
}

func (m *Manager) notify() {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for ch := range m.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// SetCookie writes the session cookie (HttpOnly, SameSite=Strict, Path=/, Secure by default).
func (m *Manager) SetCookie(c *app.RequestContext, iss *Issued) {
	maxAge := int(iss.ExpiresAt.Sub(m.now()).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	c.SetCookie(m.CookieName(), iss.Token, maxAge, "/", "", protocol.CookieSameSiteStrictMode, m.secure, true)
}

// ClearCookie expires the session cookie.
func (m *Manager) ClearCookie(c *app.RequestContext) {
	c.SetCookie(m.CookieName(), "", -1, "/", "", protocol.CookieSameSiteStrictMode, m.secure, true)
}

// touch records last_seen_at at most once per touchInterval per session.
func (m *Manager) touch(row *store.AdminSession, now time.Time) {
	m.touchMu.Lock()
	last, ok := m.touched[row.ID]
	if ok && now.Sub(last) < touchInterval {
		m.touchMu.Unlock()
		return
	}
	m.touched[row.ID] = now
	if len(m.touched) > 1024 { // bound memory: drop entries that no longer throttle anything
		for id, at := range m.touched {
			if now.Sub(at) >= touchInterval {
				delete(m.touched, id)
			}
		}
	}
	m.touchMu.Unlock()
	if !ok && now.Sub(row.LastSeenAt) < touchInterval {
		return // first sight in this process, but the row was refreshed recently
	}
	if err := m.sessions.Renew(row.ID, row.ExpiresAt, now); err != nil {
		slog.Warn("auth: last_seen update failed", "err", err)
	}
}

func (m *Manager) markTouched(id string, now time.Time) {
	m.touchMu.Lock()
	m.touched[id] = now
	m.touchMu.Unlock()
}

func (m *Manager) sign(user *store.AdminUser, row *store.AdminSession, now, exp time.Time) (*Issued, error) {
	payload, err := json.Marshal(claims{
		Sub:  user.ID,
		Sid:  row.ID,
		Ver:  user.TokenVersion,
		Iat:  now.Unix(),
		Exp:  exp.Unix(),
		Aexp: row.AbsoluteExpiresAt.Unix(),
	})
	if err != nil {
		return nil, fmt.Errorf("auth: encode claims: %w", err)
	}
	unsigned := jwtHeader + "." + base64.RawURLEncoding.EncodeToString(payload)
	return &Issued{
		Token:             unsigned + "." + m.mac(unsigned),
		SessionID:         row.ID,
		ExpiresAt:         exp,
		AbsoluteExpiresAt: row.AbsoluteExpiresAt,
	}, nil
}

func (m *Manager) mac(unsigned string) string {
	h := hmac.New(sha256.New, m.key)
	h.Write([]byte(unsigned))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// parse verifies header and signature and decodes the claims. It never trusts the
// token's own alg.
func (m *Manager) parse(token string) (*claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != jwtHeader {
		return nil, errors.New("malformed token")
	}
	if !hmac.Equal([]byte(parts[2]), []byte(m.mac(parts[0]+"."+parts[1]))) {
		return nil, errors.New("bad signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var c claims
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if c.Sub == "" || c.Sid == "" {
		return nil, errors.New("missing claims")
	}
	return &c, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: random: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

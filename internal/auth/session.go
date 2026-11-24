package auth

import (
	"errors"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/izzamoe/auto-deploy/internal/store"
)

// Session lifetimes (Q-2).
const (
	DefaultIdle     = 12 * time.Hour
	DefaultAbsolute = 7 * 24 * time.Hour
)

var (
	// ErrSessionExpired: token malformed, badly signed, past exp/aexp, or no row.
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
}

// Manager issues and verifies session cookies.
type Manager struct{}

// NewManager builds a Manager.
func NewManager(cfg SessionConfig, users UserSource, sessions SessionSource) (*Manager, error) {
	return &Manager{}, nil
}

// CookieName is __Host-admin_session when Secure, admin_session otherwise.
func (m *Manager) CookieName() string { return "" }

// Issue creates a session row and signs its cookie value.
func (m *Manager) Issue(user *store.AdminUser, ip, userAgent string) (*Issued, error) {
	return nil, errNotImplemented
}

// Reissue signs a new cookie value for an existing session (new token_version).
func (m *Manager) Reissue(sessionID string, user *store.AdminUser) (*Issued, error) {
	return nil, errNotImplemented
}

// Verify checks a cookie value against the key, the session row and the user.
func (m *Manager) Verify(token string) (*Verified, error) { return nil, errNotImplemented }

// Revoke marks the session row revoked.
func (m *Manager) Revoke(sessionID string) error { return errNotImplemented }

// SetCookie writes the session cookie.
func (m *Manager) SetCookie(c *app.RequestContext, iss *Issued) {}

// ClearCookie expires the session cookie.
func (m *Manager) ClearCookie(c *app.RequestContext) {}

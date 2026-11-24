package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/izzamoe/auto-deploy/internal/httpx"
	"github.com/izzamoe/auto-deploy/internal/store"
)

const (
	ctxKeyPrincipal = "auth.principal"

	// basicChallenge is sent ONLY to script clients that used Authorization: Basic
	// (FR-001). Browsers never get it: it would pop the native credentials dialog.
	basicChallenge = `Basic realm="auto-deploy api"`

	legacyCookieName = "admin_token" // pre-v1 session cookie, cleared on first 401
)

// Principal is the authenticated caller attached to the request context.
type Principal struct {
	Kind               string // "session" or "token"
	UserID             string
	Username           string
	MustChangePassword bool
	SessionID          string
	ExpiresAt          time.Time
	AbsoluteExpiresAt  time.Time
}

// Principal kinds.
const (
	KindSession = "session"
	KindToken   = "token"
)

// MiddlewareConfig wires the middleware.
type MiddlewareConfig struct {
	Sessions *Manager
	Tokens   TokenSource
	// TokenLimiter counts FAILED token authentications per client IP (Q-4).
	// Nil disables token rate limiting (only tests do that).
	TokenLimiter *Window
	ClientIP     *ClientIPResolver
	Now          func() time.Time
}

type middleware struct {
	cfg MiddlewareConfig
	now func() time.Time

	usedMu   sync.Mutex
	tokenUse map[string]time.Time
}

// Middleware authenticates /admin/api/v1 and /admin/events/v1/ws requests.
//
// Decision order (RV-7, RV2-5):
//  1. a valid session cookie wins; any Authorization header is ignored;
//  2. otherwise Authorization: Bearer <api token> or Basic (password = api token);
//     a Basic header that comes from a browser (it carries X-Admin-Request or
//     Sec-Fetch-Site) is stale cached credentials: ignored, answered 401
//     session_expired without a challenge and without counting against the limiter;
//  3. otherwise 401 session_expired/session_revoked, never with WWW-Authenticate.
//
// The admin password is never accepted as a Basic credential.
func Middleware(cfg MiddlewareConfig) app.HandlerFunc {
	m := &middleware{cfg: cfg, now: cfg.Now, tokenUse: map[string]time.Time{}}
	if m.now == nil {
		m.now = time.Now
	}
	if m.cfg.ClientIP == nil {
		m.cfg.ClientIP = NewClientIPResolver(nil)
	}
	return m.handle
}

func (m *middleware) handle(ctx context.Context, c *app.RequestContext) {
	path := string(c.Request.URI().Path())

	var cookieErr error
	if raw := c.Cookie(m.cfg.Sessions.CookieName()); len(raw) > 0 {
		v, err := m.cfg.Sessions.Verify(string(raw))
		if err == nil {
			m.serveSession(ctx, c, v, path)
			return
		}
		cookieErr = err
	}

	if authz := strings.TrimSpace(string(c.GetHeader("Authorization"))); authz != "" {
		m.serveAuthorization(ctx, c, authz, path, cookieErr)
		return
	}
	m.denySession(c, cookieErr)
}

func (m *middleware) serveSession(ctx context.Context, c *app.RequestContext, v *Verified, path string) {
	if isMutation(string(c.Method())) {
		switch CheckCSRF(c, m.trustedProto(c)) {
		case CSRFRejected:
			httpx.WriteError(c, 403, httpx.CodeCSRFRejected, "Cross-site request rejected")
			c.Abort()
			return
		case CSRFUnsupportedMedia:
			httpx.WriteError(c, 415, httpx.CodeUnsupportedMediaType, "Content-Type must be application/json")
			c.Abort()
			return
		}
	}
	// B3/FR-006: the gate is evaluated from the user row on every request.
	if v.User.MustChangePassword && !exemptFromPasswordGate(path) {
		httpx.WriteError(c, 403, httpx.CodePasswordChangeNeeded, "Password change required")
		c.Abort()
		return
	}
	c.Set(ctxKeyPrincipal, Principal{
		Kind:               KindSession,
		UserID:             v.User.ID,
		Username:           v.User.Username,
		MustChangePassword: v.User.MustChangePassword,
		SessionID:          v.SessionID,
		ExpiresAt:          v.ExpiresAt,
		AbsoluteExpiresAt:  v.AbsoluteExpiresAt,
	})
	if v.Renewed != nil {
		m.cfg.Sessions.SetCookie(c, v.Renewed)
	}
	c.Next(ctx)
}

func (m *middleware) serveAuthorization(ctx context.Context, c *app.RequestContext, authz, path string, cookieErr error) {
	scheme, value, _ := strings.Cut(authz, " ")
	value = strings.TrimSpace(value)
	basic := false
	var token string
	switch {
	case strings.EqualFold(scheme, "Bearer"):
		token = value
	case strings.EqualFold(scheme, "Basic"):
		if hasHeader(c, "X-Admin-Request") || hasHeader(c, "Sec-Fetch-Site") {
			// Credentials a browser cached from an old dialog: not a real attempt.
			m.denySession(c, cookieErr)
			return
		}
		basic = true
		token = basicPassword(value)
	default:
		m.denyUnauthorized(c, false, cookieErr)
		return
	}

	ip := m.cfg.ClientIP.FromContext(c)
	if m.cfg.TokenLimiter != nil {
		if blocked, retry := m.cfg.TokenLimiter.Blocked(ip); blocked {
			WriteRateLimited(c, retry)
			c.Abort()
			return
		}
	}

	principal, ok := m.authenticateToken(token)
	if !ok {
		if m.cfg.TokenLimiter != nil {
			m.cfg.TokenLimiter.Add(ip)
		}
		m.denyUnauthorized(c, basic, cookieErr)
		return
	}
	if tokenForbidden(path) {
		httpx.WriteError(c, 403, httpx.CodeForbidden, "API tokens cannot use this endpoint")
		c.Abort()
		return
	}
	if principal.MustChangePassword {
		httpx.WriteError(c, 403, httpx.CodePasswordChangeNeeded, "Password change required")
		c.Abort()
		return
	}
	c.Set(ctxKeyPrincipal, principal)
	c.Next(ctx)
}

// authenticateToken resolves an API token to its owner. Anything that does not look
// like a token (an admin password sent as Basic, for one) fails without touching the
// database. A lookup or user-load error fails closed.
func (m *middleware) authenticateToken(token string) (Principal, bool) {
	if !strings.HasPrefix(token, TokenPrefix) || m.cfg.Tokens == nil {
		return Principal{}, false
	}
	tok, err := m.cfg.Tokens.FindActiveByHash(HashToken(token))
	if err != nil {
		if !errors.Is(err, store.ErrAPITokenNotFound) {
			slog.Error("auth: api token lookup failed; denying request", "err", err)
		}
		return Principal{}, false
	}
	user, err := m.cfg.Sessions.Users().GetByID(tok.UserID)
	if err != nil {
		if !errors.Is(err, store.ErrAdminUserNotFound) {
			slog.Error("auth: token owner lookup failed; denying request", "err", err)
		}
		return Principal{}, false
	}
	m.markTokenUsed(tok.ID)
	return Principal{Kind: KindToken, UserID: user.ID, Username: user.Username, MustChangePassword: user.MustChangePassword}, true
}

// markTokenUsed records last_used_at at most once a minute per token.
func (m *middleware) markTokenUsed(id string) {
	now := m.now()
	m.usedMu.Lock()
	last, seen := m.tokenUse[id]
	if seen && now.Sub(last) < touchInterval {
		m.usedMu.Unlock()
		return
	}
	m.tokenUse[id] = now
	m.usedMu.Unlock()
	if err := m.cfg.Tokens.MarkUsed(id, now.UTC()); err != nil {
		slog.Warn("auth: token last_used update failed", "err", err)
	}
}

// denySession answers "not signed in": session_revoked when the presented cookie was
// a revoked session, session_expired otherwise. Never a WWW-Authenticate header.
func (m *middleware) denySession(c *app.RequestContext, cookieErr error) {
	code, msg := httpx.CodeSessionExpired, "Session expired or not signed in"
	if errors.Is(cookieErr, ErrSessionRevoked) {
		code, msg = httpx.CodeSessionRevoked, "Session was revoked; sign in again"
	}
	m.reject401(c, code, msg, false, cookieErr != nil)
}

// denyUnauthorized answers bad automation credentials. challenge is true only for
// the Basic branch (a script client that asked for Basic).
func (m *middleware) denyUnauthorized(c *app.RequestContext, challenge bool, cookieErr error) {
	m.reject401(c, httpx.CodeUnauthorized, "Invalid credentials", challenge, cookieErr != nil)
}

func (m *middleware) reject401(c *app.RequestContext, code, msg string, challenge, dropCookie bool) {
	if challenge {
		c.Header("WWW-Authenticate", basicChallenge)
	}
	if dropCookie {
		m.cfg.Sessions.ClearCookie(c) // a dead session cookie is useless: tell the browser to drop it
	}
	if len(c.Cookie(legacyCookieName)) > 0 {
		c.SetCookie(legacyCookieName, "", -1, "/", "", 0, false, true)
	}
	httpx.WriteError(c, 401, code, msg)
	c.Abort()
}

func (m *middleware) trustedProto(c *app.RequestContext) bool {
	return hasHeader(c, "X-Forwarded-Proto") && m.cfg.ClientIP.TrustsRemote(c.RemoteAddr())
}

// WriteRateLimited writes the 429 rate_limited error with Retry-After.
func WriteRateLimited(c *app.RequestContext, retry time.Duration) {
	secs := int(retry.Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	c.Header("Retry-After", strconv.Itoa(secs))
	httpx.WriteError(c, 429, httpx.CodeRateLimited, "Too many attempts; try again later")
}

// PrincipalFrom returns the principal set by Middleware.
func PrincipalFrom(c *app.RequestContext) (Principal, bool) {
	v, ok := c.Get(ctxKeyPrincipal)
	if !ok {
		return Principal{}, false
	}
	p, ok := v.(Principal)
	return p, ok
}

func hasHeader(c *app.RequestContext, name string) bool { return len(c.GetHeader(name)) > 0 }

// basicPassword extracts the password of an `Authorization: Basic` value; the
// username is ignored.
func basicPassword(b64 string) string {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return ""
	}
	_, pass, ok := strings.Cut(string(raw), ":")
	if !ok {
		return ""
	}
	return pass
}

// pathUnder reports whether path is prefix or below it on a segment boundary.
func pathUnder(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// accountPrefixes are the endpoints reachable under the must-change-password gate
// (account and session management), including the pre-v1 aliases (Q-7).
var accountPrefixes = []string{
	"/admin/api/v1/session", "/admin/api/v1/account",
	"/admin/api/session", "/admin/api/account",
}

func exemptFromPasswordGate(path string) bool {
	for _, p := range accountPrefixes {
		if pathUnder(path, p) {
			return true
		}
	}
	return false
}

// tokenForbidden: API tokens act as the admin for data endpoints but never reach
// the account, token and session endpoints (Q-6).
func tokenForbidden(path string) bool {
	if exemptFromPasswordGate(path) {
		return true
	}
	return pathUnder(path, "/admin/api/v1/tokens") || pathUnder(path, "/admin/api/tokens")
}

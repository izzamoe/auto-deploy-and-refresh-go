package auth

import (
	"log/slog"
	"strings"
	"sync"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/izzamoe/auto-deploy/internal/httpx"
)

// Audit event names (NFR-006).
const (
	EventLoginSucceeded   = "auth.login_succeeded"
	EventLoginFailed      = "auth.login_failed"
	EventRateLimited      = "auth.rate_limited"
	EventLogout           = "auth.logout"
	EventPasswordChanged  = "auth.password_changed"
	EventUsernameChanged  = "auth.username_changed"
	EventSessionsRevoked  = "auth.sessions_revoked"
	EventTokenCreated     = "auth.token_created"
	EventTokenRevoked     = "auth.token_revoked"
	EventAppCreated       = "app.created"
	EventAppUpdated       = "app.updated"
	EventAppDeleted       = "app.deleted"
	EventAppSecretRotated = "app.secret_rotated"
	EventDeployManual     = "deploy.manual"
	EventDeployCancel     = "deploy.cancel"
	EventDeployRollback   = "deploy.rollback"
)

var (
	auditMu       sync.RWMutex
	auditClientIP = NewClientIPResolver(nil)
)

// SetAuditClientIP installs the resolver that audit lines use for the "ip" field, so
// they honor TRUSTED_PROXY_CIDRS like the rate limiters do (Q-5).
func SetAuditClientIP(r *ClientIPResolver) {
	if r == nil {
		return
	}
	auditMu.Lock()
	auditClientIP = r
	auditMu.Unlock()
}

// AuditRequest logs one audit event for the request: a single structured line with
// msg "audit", the event name, requestId, userId and ip, plus attrs (key, value, ...).
// userID may be empty: the signed-in principal, if any, is used. Values of secret-looking
// keys (password, token, secret, authorization, ...) are replaced by "[redacted]" so a
// careless call site can never leak them.
func AuditRequest(c *app.RequestContext, event, userID string, attrs ...any) {
	if userID == "" {
		if p, ok := PrincipalFrom(c); ok {
			userID = p.UserID
		}
	}
	auditMu.RLock()
	resolver := auditClientIP
	auditMu.RUnlock()
	base := []any{"event", event, "requestId", httpx.FromContext(c), "userId", userID, "ip", resolver.FromContext(c)}
	slog.Info("audit", append(base, redactAttrs(attrs)...)...)
}

// AuditBackground logs an audit event that has no HTTP request (a deploy that was
// rolled back by the coordinator). requestId and ip are empty.
func AuditBackground(event string, attrs ...any) {
	base := []any{"event", event, "requestId", "", "userId", "", "ip", ""}
	slog.Info("audit", append(base, redactAttrs(attrs)...)...)
}

func secretKey(key string) bool {
	k := strings.ToLower(key)
	switch k {
	case "token", "secret", "password", "authorization", "cookie", "hash", "apphash", "bottoken", "webhooksecret", "key", "apikey":
		return true
	}
	return strings.HasSuffix(k, "password") || strings.HasSuffix(k, "secret") || strings.HasSuffix(k, "authorization")
}

func redactAttrs(attrs []any) []any {
	out := make([]any, 0, len(attrs))
	for i := 0; i+1 < len(attrs); i += 2 {
		key, _ := attrs[i].(string)
		if secretKey(key) {
			out = append(out, key, "[redacted]")
			continue
		}
		out = append(out, attrs[i], attrs[i+1])
	}
	return out
}

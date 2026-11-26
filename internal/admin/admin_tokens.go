package admin

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/izzamoe/auto-deploy/internal/auth"
	"github.com/izzamoe/auto-deploy/internal/events"
	"github.com/izzamoe/auto-deploy/internal/httpx"
	"github.com/izzamoe/auto-deploy/internal/store"
)

const maxTokenNameLen = 64

// TokensHandler serves the API-token endpoints (FR-014). They require a session
// cookie: the middleware refuses API tokens on /tokens (Q-6).
type TokensHandler struct {
	tokens *store.APITokenStore
	eventPublisher

	// subs receive a signal when a token is revoked: the event sockets opened with a token
	// re-check it at once and close 4401 (T089 CR-4).
	subMu sync.Mutex
	subs  map[chan struct{}]struct{}
}

// errTokenGone is the outcome of RecheckToken for a token that was revoked or removed.
var errTokenGone = errors.New("api token revoked or removed")

// RecheckToken re-checks the API token of an open event socket (T089 CR-4): the user's
// token must still exist and not be revoked. A store error fails closed.
func (h *TokensHandler) RecheckToken(userID, tokenID string) error {
	list, err := h.tokens.List(userID)
	if err != nil {
		slog.Error("events: api token recheck failed; closing", "err", err)
		return err
	}
	for _, t := range list {
		if t.ID == tokenID && t.RevokedAt == nil {
			return nil
		}
	}
	return errTokenGone
}

// SubscribeTokens returns a channel that receives a (coalesced) signal whenever a token
// was revoked in this process; the returned func must be called when the holder is done.
func (h *TokensHandler) SubscribeTokens() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.subMu.Lock()
	if h.subs == nil {
		h.subs = make(map[chan struct{}]struct{})
	}
	h.subs[ch] = struct{}{}
	h.subMu.Unlock()
	return ch, func() {
		h.subMu.Lock()
		delete(h.subs, ch)
		h.subMu.Unlock()
	}
}

func (h *TokensHandler) notifyRevoked() {
	h.subMu.Lock()
	defer h.subMu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// NewTokensHandler builds the handler.
func NewTokensHandler(tokens *store.APITokenStore) *TokensHandler {
	return &TokensHandler{tokens: tokens}
}

// apiTokenJSON is the contract schema ApiToken: no secret, ever.
type apiTokenJSON struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
}

// apiTokenCreatedJSON is the contract schema ApiTokenCreated: the only response that
// ever carries the full token value.
type apiTokenCreatedJSON struct {
	apiTokenJSON
	Token string `json:"token"`
}

func toTokenJSON(t store.APIToken) apiTokenJSON {
	return apiTokenJSON{ID: t.ID, Name: t.Name, Prefix: t.Prefix, CreatedAt: t.CreatedAt.UTC(), LastUsedAt: t.LastUsedAt, RevokedAt: t.RevokedAt}
}

// ListHertz handles GET /tokens.
func (h *TokensHandler) ListHertz(ctx context.Context, c *app.RequestContext) {
	p, ok := auth.PrincipalFrom(c)
	if !ok {
		httpx.WriteError(c, 401, httpx.CodeSessionExpired, "Session expired or not signed in")
		return
	}
	list, err := h.tokens.List(p.UserID)
	if err != nil {
		slog.Error("list api tokens failed", "err", err, "request_id", httpx.FromContext(c))
		httpx.WriteError(c, 500, httpx.CodeInternalError, "Failed to list tokens")
		return
	}
	out := make([]apiTokenJSON, 0, len(list))
	for _, t := range list {
		out = append(out, toTokenJSON(t))
	}
	c.JSON(200, map[string]any{"tokens": out})
}

// CreateHertz handles POST /tokens: the full value is returned once.
func (h *TokensHandler) CreateHertz(ctx context.Context, c *app.RequestContext) {
	p, ok := auth.PrincipalFrom(c)
	if !ok {
		httpx.WriteError(c, 401, httpx.CodeSessionExpired, "Session expired or not signed in")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !bindJSON(c, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || utf8.RuneCountInString(name) > maxTokenNameLen {
		httpx.WriteError(c, 400, httpx.CodeValidationFailed, "Validation failed",
			httpx.FieldError{Field: "name", Code: httpx.CodeValidationFailed, Message: "Name must be 1-64 characters"})
		return
	}

	full, hash, prefix, err := auth.GenerateToken()
	if err == nil {
		var id string
		if id, err = store.NewAPITokenID(); err == nil {
			row := store.APIToken{ID: id, UserID: p.UserID, Name: name, TokenHash: hash, Prefix: prefix, CreatedAt: time.Now().UTC()}
			if err = h.tokens.Create(&row); err == nil {
				auth.AuditRequest(c, auth.EventTokenCreated, p.UserID, "tokenId", id, "tokenName", name)
				// Only this user's tabs, and never the token value (FR-005, FR-017).
				h.publish(c, events.AccountChanged(p.UserID, events.SectionTokens))
				c.JSON(201, apiTokenCreatedJSON{apiTokenJSON: toTokenJSON(row), Token: full})
				return
			}
		}
	}
	slog.Error("create api token failed", "err", err, "request_id", httpx.FromContext(c))
	httpx.WriteError(c, 500, httpx.CodeInternalError, "Failed to create token")
}

// RevokeHertz handles DELETE /tokens/:id.
func (h *TokensHandler) RevokeHertz(ctx context.Context, c *app.RequestContext) {
	p, ok := auth.PrincipalFrom(c)
	if !ok {
		httpx.WriteError(c, 401, httpx.CodeSessionExpired, "Session expired or not signed in")
		return
	}
	id := c.Param("id")
	if err := h.tokens.Revoke(p.UserID, id, time.Now().UTC()); err != nil {
		if errors.Is(err, store.ErrAPITokenNotFound) {
			httpx.WriteError(c, 404, httpx.CodeNotFound, "Token not found")
			return
		}
		slog.Error("revoke api token failed", "err", err, "request_id", httpx.FromContext(c))
		httpx.WriteError(c, 500, httpx.CodeInternalError, "Failed to revoke token")
		return
	}
	auth.AuditRequest(c, auth.EventTokenRevoked, p.UserID, "tokenId", id)
	h.notifyRevoked() // the token's event sockets close 4401 now (T089 CR-4)
	h.publish(c, events.AccountChanged(p.UserID, events.SectionTokens))
	c.Status(204)
}

// RegisterTokenRoutesHertz mounts the token endpoints behind the auth middleware.
func RegisterTokenRoutesHertz(h *server.Hertz, handler *TokensHandler, auth app.HandlerFunc) {
	api := h.Group("/admin/api/v1", auth)
	api.GET("/tokens", handler.ListHertz)
	api.POST("/tokens", handler.CreateHertz)
	api.DELETE("/tokens/:id", handler.RevokeHertz)
}

// bindJSON decodes the request body into dst; on failure it writes the 400
// invalid_json error and returns false.
func bindJSON(c *app.RequestContext, dst any) bool {
	if err := c.BindJSON(dst); err != nil {
		httpx.WriteError(c, 400, httpx.CodeInvalidJSON, "Invalid JSON body")
		return false
	}
	return true
}

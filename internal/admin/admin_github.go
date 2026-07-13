package admin

import (
	"context"
	"strings"
	"sync"

	"github.com/izzamoe/auto-deploy/internal/store"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// githubTokenSetter is the minimal surface GitHubConfigHandler needs to push
// a new token onto the live GitHub client, kept as a small local interface
// (rather than a direct *github.Client dependency) so it is easy to fake in
// tests.
type githubTokenSetter interface {
	SetToken(token string)
}

// GitHubConfigHandler serves the admin API for reading/writing the GitHub
// personal access token used by the Releases API, persisting it in the
// database (so it no longer has to be baked into the service environment)
// and applying it to the live client on every change.
type GitHubConfigHandler struct {
	store    *store.GitHubConfigStore
	client   githubTokenSetter
	envToken string

	mu sync.Mutex
}

// NewGitHubConfigHandler creates a handler bound to cfgStore and client.
// envToken is the token from the process environment (GITHUB_TOKEN), used as
// a fallback when no token has been saved in the database — so existing
// env-based deployments keep working after upgrading.
func NewGitHubConfigHandler(cfgStore *store.GitHubConfigStore, client githubTokenSetter, envToken string) *GitHubConfigHandler {
	return &GitHubConfigHandler{store: cfgStore, client: client, envToken: envToken}
}

// Reload loads the stored token and applies it (or the env fallback) to the
// live client. Call once at startup, in addition to the automatic apply
// performed after every save.
func (h *GitHubConfigHandler) Reload() error {
	cfg, err := h.store.Get()
	if err != nil {
		return err
	}
	h.apply(cfg.Token)
	return nil
}

// apply pushes the effective token (stored token, or the env fallback when
// none is stored) onto the live client.
func (h *GitHubConfigHandler) apply(storedToken string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	effective := storedToken
	if effective == "" {
		effective = h.envToken
	}
	h.client.SetToken(effective)
}

// tokenSource reports where the effective token comes from, for display.
func tokenSource(storedToken, envToken string) string {
	switch {
	case storedToken != "":
		return "database"
	case envToken != "":
		return "environment"
	default:
		return "none"
	}
}

type githubConfigResponse struct {
	// Token is the masked effective token (all but the last 4 chars hidden),
	// never the raw secret.
	Token    string `json:"token"`
	HasToken bool   `json:"hasToken"`
	Source   string `json:"source"`
}

func (h *GitHubConfigHandler) responseFor(storedToken string) githubConfigResponse {
	effective := storedToken
	if effective == "" {
		effective = h.envToken
	}
	return githubConfigResponse{
		Token:    maskBotToken(effective),
		HasToken: effective != "",
		Source:   tokenSource(storedToken, h.envToken),
	}
}

type githubConfigRequest struct {
	Token string `json:"token"`
}

// GetGitHubConfigHertz returns the current GitHub token, masked.
func (h *GitHubConfigHandler) GetGitHubConfigHertz(ctx context.Context, c *app.RequestContext) {
	cfg, err := h.store.Get()
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to load GitHub config")
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"config": h.responseFor(cfg.Token)})
}

// SaveGitHubConfigHertz updates the stored GitHub token. Submitting the token
// blank or unchanged (still equal to the masked placeholder previously
// returned by GET) keeps the existing stored token — the same "leave blank to
// keep current secret" idiom used elsewhere in this codebase. On save the
// live client is updated so the change takes effect immediately.
func (h *GitHubConfigHandler) SaveGitHubConfigHertz(ctx context.Context, c *app.RequestContext) {
	if !requireAdminAPIJSONRequestHertz(c) {
		return
	}

	var body githubConfigRequest
	if !decodeAdminAPIJSONHertz(c, &body) {
		return
	}

	current, err := h.store.Get()
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to load GitHub config")
		return
	}

	token := strings.TrimSpace(body.Token)
	if token == "" || token == maskBotToken(current.Token) || token == maskBotToken(h.envToken) {
		token = current.Token
	}

	if err := h.store.Save(store.GitHubConfig{Token: token}); err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to save GitHub config")
		return
	}

	h.apply(token)

	c.JSON(consts.StatusOK, map[string]any{"status": "updated", "config": h.responseFor(token)})
}

// RegisterGitHubRoutesHertz registers the GitHub config admin API routes.
// Must be registered before RegisterAdminAPIRoutesHertz, whose
// /admin/api/*path catch-all would otherwise shadow these specific routes.
func RegisterGitHubRoutesHertz(h *server.Hertz, handler *GitHubConfigHandler, auth app.HandlerFunc) {
	api := h.Group("/admin/api", auth)
	api.GET("/github", handler.GetGitHubConfigHertz)
	api.POST("/github", handler.SaveGitHubConfigHertz)
}

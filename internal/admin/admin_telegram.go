package admin

import (
	"context"
	"strings"
	"sync"

	"github.com/izzamoe/auto-deploy/internal/store"
	"github.com/izzamoe/auto-deploy/internal/telegram"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// notifierSetter is the minimal surface TelegramConfigHandler needs from a
// Coordinator to (re)attach the live Telegram notifier, kept as a small
// local interface (rather than a direct *coordinator.Coordinator
// dependency) so it stays easy to fake in tests.
type notifierSetter interface {
	SetNotifier(n telegram.Notifier)
}

// telegramManagerLifecycle is the Start/Stop surface of *telegram.Manager,
// extracted so tests can substitute a fake instead of driving a real
// MTProto connection (which requires live credentials and network access).
type telegramManagerLifecycle interface {
	telegram.Notifier
	Start(ctx context.Context)
	Stop()
}

// TelegramConfigHandler serves the admin API for reading/writing Telegram
// deploy-notification settings and owns the lifecycle of the (optional)
// live telegram.Manager built from that config.
type TelegramConfigHandler struct {
	store       *store.TelegramConfigStore
	notifier    notifierSetter
	sessionPath string
	baseCtx     context.Context
	newManager  func(telegram.Config) telegramManagerLifecycle

	mu      sync.Mutex
	manager telegramManagerLifecycle
}

// NewTelegramConfigHandler creates a handler bound to cfgStore. baseCtx is
// the long-lived context passed to any telegram.Manager.Start call (it
// should live as long as the server process, e.g. the context cancelled on
// SIGINT/SIGTERM). sessionPath is the file path used for gotd/td's file
// session storage.
func NewTelegramConfigHandler(baseCtx context.Context, cfgStore *store.TelegramConfigStore, sessionPath string, notifier notifierSetter) *TelegramConfigHandler {
	return &TelegramConfigHandler{
		store:       cfgStore,
		notifier:    notifier,
		sessionPath: sessionPath,
		baseCtx:     baseCtx,
		newManager: func(cfg telegram.Config) telegramManagerLifecycle {
			return telegram.NewManager(cfg)
		},
	}
}

// Reload loads the current config from the store and (re)starts or stops the
// Telegram manager accordingly. Call once at startup, in addition to the
// automatic reload performed after every successful save.
func (h *TelegramConfigHandler) Reload() error {
	cfg, err := h.store.Get()
	if err != nil {
		return err
	}
	h.apply(*cfg)
	return nil
}

// apply stops any currently-running manager, then starts a fresh one for
// cfg if it is enabled and complete, or falls back to a NopNotifier
// otherwise.
func (h *TelegramConfigHandler) apply(cfg store.TelegramConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.manager != nil {
		h.manager.Stop()
		h.manager = nil
	}

	if !telegramConfigComplete(cfg) {
		h.notifier.SetNotifier(telegram.NopNotifier{})
		return
	}

	m := h.newManager(telegram.Config{
		AppID:        cfg.AppID,
		AppHash:      cfg.AppHash,
		BotToken:     cfg.BotToken,
		ChatUsername: cfg.ChatUsername,
		SessionPath:  h.sessionPath,
	})
	m.Start(h.baseCtx)
	h.manager = m
	h.notifier.SetNotifier(m)
}

func telegramConfigComplete(cfg store.TelegramConfig) bool {
	return cfg.Enabled && cfg.AppID != 0 && cfg.AppHash != "" && cfg.BotToken != "" && cfg.ChatUsername != ""
}

// maskBotToken hides all but the last 4 characters of a bot token so it is
// never fully re-exposed to the SPA after being saved (the same
// write-mostly-secret spirit as webhook secrets elsewhere in this
// codebase, but surfaced as a masked value here instead of omitted
// entirely, per the product requirement for this field).
func maskBotToken(token string) string {
	if token == "" {
		return ""
	}
	const visible = 4
	if len(token) <= visible {
		return strings.Repeat("*", len(token))
	}
	return strings.Repeat("*", len(token)-visible) + token[len(token)-visible:]
}

type telegramConfigResponse struct {
	AppID        int    `json:"appId"`
	AppHash      string `json:"appHash"`
	BotToken     string `json:"botToken"`
	ChatUsername string `json:"chatUsername"`
	Enabled      bool   `json:"enabled"`
}

func telegramConfigResponseFrom(cfg store.TelegramConfig) telegramConfigResponse {
	return telegramConfigResponse{
		AppID:        cfg.AppID,
		AppHash:      cfg.AppHash,
		BotToken:     maskBotToken(cfg.BotToken),
		ChatUsername: cfg.ChatUsername,
		Enabled:      cfg.Enabled,
	}
}

type telegramConfigRequest struct {
	AppID        int    `json:"appId"`
	AppHash      string `json:"appHash"`
	BotToken     string `json:"botToken"`
	ChatUsername string `json:"chatUsername"`
	Enabled      *bool  `json:"enabled"`
}

// GetTelegramConfigHertz returns the current Telegram config, with the bot
// token masked.
func (h *TelegramConfigHandler) GetTelegramConfigHertz(ctx context.Context, c *app.RequestContext) {
	cfg, err := h.store.Get()
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to load telegram config")
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"config": telegramConfigResponseFrom(*cfg)})
}

// SaveTelegramConfigHertz updates the Telegram config. If botToken is
// submitted blank or unchanged (still equal to the masked placeholder
// previously returned by GET), the existing stored token is kept — the
// same "leave blank to keep current secret" idiom used for webhook secrets
// elsewhere in this codebase. On save, the live Telegram manager is
// stopped and, if the new config is enabled and complete, restarted so
// the change takes effect immediately.
func (h *TelegramConfigHandler) SaveTelegramConfigHertz(ctx context.Context, c *app.RequestContext) {
	if !requireAdminAPIJSONRequestHertz(c) {
		return
	}

	var body telegramConfigRequest
	if !decodeAdminAPIJSONHertz(c, &body) {
		return
	}

	current, err := h.store.Get()
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to load telegram config")
		return
	}

	botToken := body.BotToken
	if botToken == "" || botToken == maskBotToken(current.BotToken) {
		botToken = current.BotToken
	}

	enabled := current.Enabled
	if body.Enabled != nil {
		enabled = *body.Enabled
	}

	newCfg := store.TelegramConfig{
		AppID:        body.AppID,
		AppHash:      strings.TrimSpace(body.AppHash),
		BotToken:     botToken,
		ChatUsername: strings.TrimSpace(body.ChatUsername),
		Enabled:      enabled,
	}

	if errs := validateTelegramConfig(newCfg); len(errs) > 0 {
		writeAdminAPIValidationErrorHertz(c, errs)
		return
	}

	if err := h.store.Save(newCfg); err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to save telegram config")
		return
	}

	h.apply(newCfg)

	c.JSON(consts.StatusOK, map[string]any{"status": "updated", "config": telegramConfigResponseFrom(newCfg)})
}

// validateTelegramConfig requires appId/appHash/botToken/chatUsername only
// when the config is being enabled — a disabled (or not-yet-configured)
// config may be saved with blank fields.
func validateTelegramConfig(cfg store.TelegramConfig) []string {
	if !cfg.Enabled {
		return nil
	}
	var errs []string
	if cfg.AppID == 0 {
		errs = append(errs, "App ID is required")
	}
	if cfg.AppHash == "" {
		errs = append(errs, "App Hash is required")
	}
	if cfg.BotToken == "" {
		errs = append(errs, "Bot Token is required")
	}
	if cfg.ChatUsername == "" {
		errs = append(errs, "Chat Username is required")
	}
	return errs
}

// RegisterTelegramRoutesHertz registers the Telegram config admin API
// routes. Must be registered before RegisterAdminAPIRoutesHertz, whose
// /admin/api/*path catch-all would otherwise shadow these specific routes.
func RegisterTelegramRoutesHertz(h *server.Hertz, handler *TelegramConfigHandler, auth app.HandlerFunc) {
	api := h.Group("/admin/api", auth)
	api.GET("/telegram", handler.GetTelegramConfigHertz)
	api.POST("/telegram", handler.SaveTelegramConfigHertz)
}

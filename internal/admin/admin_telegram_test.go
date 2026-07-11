package admin

import (
	"context"
	"net/http"
	"path/filepath"
	"sync"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/store"
	"github.com/izzamoe/auto-deploy/internal/telegram"

	"github.com/cloudwego/hertz/pkg/app/server"
)

// fakeNotifierSetter records SetNotifier calls instead of driving a real
// Coordinator.
type fakeNotifierSetter struct {
	mu  sync.Mutex
	set []telegram.Notifier
}

func (f *fakeNotifierSetter) SetNotifier(n telegram.Notifier) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set = append(f.set, n)
}

func (f *fakeNotifierSetter) last() telegram.Notifier {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.set) == 0 {
		return nil
	}
	return f.set[len(f.set)-1]
}

func (f *fakeNotifierSetter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.set)
}

// fakeTelegramManager stands in for *telegram.Manager in tests, avoiding any
// real MTProto/network activity. Manager's actual gotd/td wiring is
// exercised only in internal/telegram's own tests (against a fake sender)
// and cannot be exercised here or in CI without live credentials and
// network access to Telegram's servers.
type fakeTelegramManager struct {
	telegram.NopNotifier
	mu      sync.Mutex
	started bool
	stopped bool
}

func (f *fakeTelegramManager) Start(context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = true
}

func (f *fakeTelegramManager) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
}

func newTestTelegramConfigHandler(t *testing.T) (*TelegramConfigHandler, *store.TelegramConfigStore, *fakeNotifierSetter) {
	t.Helper()
	db := newTestDB(t)
	cfgStore, err := store.NewTelegramConfigStore(db)
	if err != nil {
		t.Fatalf("NewTelegramConfigStore: %v", err)
	}
	notifier := &fakeNotifierSetter{}
	sessionPath := filepath.Join(t.TempDir(), "telegram-session.json")
	handler := NewTelegramConfigHandler(t.Context(), cfgStore, sessionPath, notifier)
	handler.newManager = func(telegram.Config) telegramManagerLifecycle {
		return &fakeTelegramManager{}
	}
	return handler, cfgStore, notifier
}

func newTestTelegramServer(t *testing.T, handler *TelegramConfigHandler) (*server.Hertz, *JWTHandler) {
	t.Helper()
	jwt := testJWTHandler(t)
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	RegisterTelegramRoutesHertz(h, handler, HertzSessionAuthMiddleware(jwt, testAuthenticator()))
	return h, jwt
}

func TestTelegramConfigGetReturnsZeroValueWhenUnset(t *testing.T) {
	t.Parallel()
	handler, _, _ := newTestTelegramConfigHandler(t)
	h, jwt := newTestTelegramServer(t, handler)

	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, http.MethodGet, "/admin/api/telegram", ""))
	if rr.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Response.StatusCode(), rr.Response.Body())
	}
	got := decodeAdminAPIResponseHertz[struct {
		Config telegramConfigResponse `json:"config"`
	}](t, rr)
	if got.Config.Enabled || got.Config.AppID != 0 || got.Config.BotToken != "" {
		t.Fatalf("expected zero-value config, got %+v", got.Config)
	}
}

func TestTelegramConfigSaveMasksBotTokenOnGet(t *testing.T) {
	t.Parallel()
	handler, _, _ := newTestTelegramConfigHandler(t)
	h, jwt := newTestTelegramServer(t, handler)

	saveBody := `{"appId":12345,"appHash":"abcdef0123456789","botToken":"123456:ABC-DEF-TOKEN","chatUsername":"@mychannel","enabled":true}`
	saveRR := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, http.MethodPost, "/admin/api/telegram", saveBody))
	if saveRR.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200 save, got %d body=%s", saveRR.Response.StatusCode(), saveRR.Response.Body())
	}
	saved := decodeAdminAPIResponseHertz[struct {
		Config telegramConfigResponse `json:"config"`
	}](t, saveRR)
	if saved.Config.BotToken == "123456:ABC-DEF-TOKEN" {
		t.Fatal("bot token was returned unmasked after save")
	}
	if saved.Config.BotToken != "****************OKEN" {
		t.Fatalf("unexpected masked token: %q", saved.Config.BotToken)
	}

	getRR := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, http.MethodGet, "/admin/api/telegram", ""))
	got := decodeAdminAPIResponseHertz[struct {
		Config telegramConfigResponse `json:"config"`
	}](t, getRR)
	if got.Config.BotToken != saved.Config.BotToken {
		t.Fatalf("GET token = %q, want masked %q", got.Config.BotToken, saved.Config.BotToken)
	}
	if got.Config.AppID != 12345 || got.Config.ChatUsername != "@mychannel" || !got.Config.Enabled {
		t.Fatalf("unexpected config: %+v", got.Config)
	}
}

func TestTelegramConfigSaveKeepsExistingTokenWhenBlank(t *testing.T) {
	t.Parallel()
	handler, cfgStore, _ := newTestTelegramConfigHandler(t)
	h, jwt := newTestTelegramServer(t, handler)

	first := `{"appId":1,"appHash":"hash1","botToken":"secret-token-1","chatUsername":"@one","enabled":true}`
	if rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, http.MethodPost, "/admin/api/telegram", first)); rr.Response.StatusCode() != http.StatusOK {
		t.Fatalf("first save failed: %d body=%s", rr.Response.StatusCode(), rr.Response.Body())
	}

	// Second save omits botToken entirely (blank) but changes chatUsername.
	second := `{"appId":1,"appHash":"hash1","botToken":"","chatUsername":"@two","enabled":true}`
	if rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, http.MethodPost, "/admin/api/telegram", second)); rr.Response.StatusCode() != http.StatusOK {
		t.Fatalf("second save failed: %d body=%s", rr.Response.StatusCode(), rr.Response.Body())
	}

	stored, err := cfgStore.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.BotToken != "secret-token-1" {
		t.Fatalf("bot token changed unexpectedly: %q", stored.BotToken)
	}
	if stored.ChatUsername != "@two" {
		t.Fatalf("chat username = %q, want @two", stored.ChatUsername)
	}
}

func TestTelegramConfigSaveRejectsIncompleteEnabledConfig(t *testing.T) {
	t.Parallel()
	handler, _, _ := newTestTelegramConfigHandler(t)
	h, jwt := newTestTelegramServer(t, handler)

	body := `{"appId":0,"appHash":"","botToken":"","chatUsername":"","enabled":true}`
	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, http.MethodPost, "/admin/api/telegram", body))
	if rr.Response.StatusCode() != http.StatusBadRequest {
		t.Fatalf("expected 400 for incomplete enabled config, got %d body=%s", rr.Response.StatusCode(), rr.Response.Body())
	}
}

func TestTelegramConfigSaveAllowsIncompleteDisabledConfig(t *testing.T) {
	t.Parallel()
	handler, _, _ := newTestTelegramConfigHandler(t)
	h, jwt := newTestTelegramServer(t, handler)

	body := `{"appId":0,"appHash":"","botToken":"","chatUsername":"","enabled":false}`
	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, http.MethodPost, "/admin/api/telegram", body))
	if rr.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200 for incomplete disabled config, got %d body=%s", rr.Response.StatusCode(), rr.Response.Body())
	}
}

func TestTelegramConfigSaveStartsManagerWhenEnabledAndAttachesNotifier(t *testing.T) {
	t.Parallel()
	handler, _, notifier := newTestTelegramConfigHandler(t)
	h, jwt := newTestTelegramServer(t, handler)

	body := `{"appId":1,"appHash":"hash","botToken":"token","chatUsername":"@ops","enabled":true}`
	if rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, http.MethodPost, "/admin/api/telegram", body)); rr.Response.StatusCode() != http.StatusOK {
		t.Fatalf("save failed: %d body=%s", rr.Response.StatusCode(), rr.Response.Body())
	}

	if notifier.callCount() != 1 {
		t.Fatalf("expected SetNotifier to be called once, got %d", notifier.callCount())
	}
	fake, ok := notifier.last().(*fakeTelegramManager)
	if !ok {
		t.Fatalf("expected SetNotifier to receive the fake manager, got %T", notifier.last())
	}
	fake.mu.Lock()
	started := fake.started
	fake.mu.Unlock()
	if !started {
		t.Fatal("expected manager.Start to have been called")
	}
}

func TestTelegramConfigSaveStopsManagerAndFallsBackToNopWhenDisabled(t *testing.T) {
	t.Parallel()
	handler, _, notifier := newTestTelegramConfigHandler(t)
	h, jwt := newTestTelegramServer(t, handler)

	enableBody := `{"appId":1,"appHash":"hash","botToken":"token","chatUsername":"@ops","enabled":true}`
	if rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, http.MethodPost, "/admin/api/telegram", enableBody)); rr.Response.StatusCode() != http.StatusOK {
		t.Fatalf("enable save failed: %d body=%s", rr.Response.StatusCode(), rr.Response.Body())
	}
	fake, ok := notifier.last().(*fakeTelegramManager)
	if !ok {
		t.Fatalf("expected fake manager after enabling, got %T", notifier.last())
	}

	disableBody := `{"appId":1,"appHash":"hash","botToken":"","chatUsername":"@ops","enabled":false}`
	if rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, http.MethodPost, "/admin/api/telegram", disableBody)); rr.Response.StatusCode() != http.StatusOK {
		t.Fatalf("disable save failed: %d body=%s", rr.Response.StatusCode(), rr.Response.Body())
	}

	fake.mu.Lock()
	stopped := fake.stopped
	fake.mu.Unlock()
	if !stopped {
		t.Fatal("expected the previous manager.Stop to have been called on disable")
	}

	if _, ok := notifier.last().(telegram.NopNotifier); !ok {
		t.Fatalf("expected NopNotifier to be attached after disable, got %T", notifier.last())
	}
}

func TestTelegramConfigHandlerReloadAppliesStoredConfig(t *testing.T) {
	t.Parallel()
	handler, cfgStore, notifier := newTestTelegramConfigHandler(t)

	if err := cfgStore.Save(store.TelegramConfig{
		AppID: 1, AppHash: "hash", BotToken: "token", ChatUsername: "@ops", Enabled: true,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := handler.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if notifier.callCount() != 1 {
		t.Fatalf("expected SetNotifier called once by Reload, got %d", notifier.callCount())
	}
	if _, ok := notifier.last().(*fakeTelegramManager); !ok {
		t.Fatalf("expected fake manager attached by Reload, got %T", notifier.last())
	}
}

func TestMaskBotToken(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"abcd", "****"},
		{"abc", "***"},
		{"123456:ABC-DEF-TOKEN", "****************OKEN"},
	}
	for _, tc := range cases {
		if got := maskBotToken(tc.in); got != tc.want {
			t.Errorf("maskBotToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

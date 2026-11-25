package admin

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/izzamoe/auto-deploy/internal/store"
	"github.com/izzamoe/auto-deploy/internal/telegram"
)

// B27 / FR-029 / FR-052: stored Telegram/GitHub secrets are masked on read, can be
// cleared explicitly, a blank field keeps the stored value, and reloading the Telegram
// manager neither drops queued messages nor shares a session file between credentials.

const (
	tgSave = `{"appId":12345,"appHash":"0123456789abcdef0123456789abcdef","botToken":"123456:ABC-DEF-TOKEN","chatUsername":"@ops","enabled":true}`
	tgPath = "/admin/api/v1/telegram"
)

func tgConfigOf(t *testing.T, h *server.Hertz, jwt *testAuth) telegramConfigResponse {
	t.Helper()
	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, http.MethodGet, tgPath, ""))
	return decodeAdminAPIResponseHertz[struct {
		Config telegramConfigResponse `json:"config"`
	}](t, rr).Config
}

func TestTelegram_GetNeverReturnsTheFullAppHash(t *testing.T) {
	t.Parallel()
	handler, _, _ := newTestTelegramConfigHandler(t)
	h, jwt := newTestTelegramServer(t, handler)
	if rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, http.MethodPost, tgPath, tgSave)); rr.Response.StatusCode() != http.StatusOK {
		t.Fatalf("save: %d %s", rr.Response.StatusCode(), rr.Response.Body())
	}
	got := tgConfigOf(t, h, jwt)
	if got.AppHash == "0123456789abcdef0123456789abcdef" || strings.Contains(got.AppHash, "0123456789abcdef0123") {
		t.Fatalf("B27: GET /telegram returned the app hash: %q", got.AppHash)
	}
	if !strings.HasSuffix(got.AppHash, "cdef") || strings.Trim(got.AppHash[:len(got.AppHash)-4], "*") != "" {
		t.Errorf("app hash mask = %q, want asterisks + the last 4 characters", got.AppHash)
	}
}

// A form that is saved again with the masked placeholder (or blank) keeps the secret.
func TestTelegram_BlankOrMaskedSecretsKeepTheStoredOnes(t *testing.T) {
	t.Parallel()
	handler, cfgStore, _ := newTestTelegramConfigHandler(t)
	h, jwt := newTestTelegramServer(t, handler)
	_ = serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, http.MethodPost, tgPath, tgSave))
	masked := tgConfigOf(t, h, jwt)

	for name, body := range map[string]string{
		"blank":  `{"appId":12345,"appHash":"","botToken":"","chatUsername":"@new","enabled":true}`,
		"masked": `{"appId":12345,"appHash":"` + masked.AppHash + `","botToken":"` + masked.BotToken + `","chatUsername":"@new","enabled":true}`,
	} {
		if rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, http.MethodPost, tgPath, body)); rr.Response.StatusCode() != http.StatusOK {
			t.Fatalf("%s: %d %s", name, rr.Response.StatusCode(), rr.Response.Body())
		}
		stored, _ := cfgStore.Get()
		if stored.AppHash != "0123456789abcdef0123456789abcdef" || stored.BotToken != "123456:ABC-DEF-TOKEN" || stored.ChatUsername != "@new" {
			t.Fatalf("B27 (%s): stored config = %+v; blank/masked secrets must be kept and the chat updated", name, stored)
		}
	}
}

func TestTelegram_ClearFlagsRemoveTheStoredSecrets(t *testing.T) {
	t.Parallel()
	handler, cfgStore, _ := newTestTelegramConfigHandler(t)
	h, jwt := newTestTelegramServer(t, handler)
	_ = serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, http.MethodPost, tgPath, tgSave))

	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, http.MethodPost, tgPath,
		`{"appId":12345,"chatUsername":"@ops","enabled":false,"clearAppHash":true,"clearBotToken":true}`))
	if rr.Response.StatusCode() != http.StatusOK {
		t.Fatalf("clear: %d %s", rr.Response.StatusCode(), rr.Response.Body())
	}
	stored, _ := cfgStore.Get()
	if stored.AppHash != "" || stored.BotToken != "" {
		t.Fatalf("B27: the secrets cannot be removed: stored = %+v", stored)
	}
	// clearing a secret while keeping the integration enabled is a validation error
	_ = serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, http.MethodPost, tgPath, tgSave))
	rr = serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, http.MethodPost, tgPath, `{"appId":12345,"chatUsername":"@ops","enabled":true,"clearBotToken":true}`))
	if rr.Response.StatusCode() != http.StatusBadRequest {
		t.Errorf("enabled + clearBotToken = %d, want 400", rr.Response.StatusCode())
	}
}

func newTestGitHubHandler(t *testing.T, envToken string) (*GitHubConfigHandler, *store.GitHubConfigStore, *server.Hertz, *testAuth) {
	t.Helper()
	cfgStore, err := store.NewGitHubConfigStore(newTestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	handler := NewGitHubConfigHandler(cfgStore, &fakeTokenSetter{}, envToken)
	jwt := testJWTHandler(t)
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	RegisterGitHubRoutesHertz(h, handler, jwt.middleware())
	return handler, cfgStore, h, jwt
}

type fakeTokenSetter struct {
	mu    sync.Mutex
	token string
}

func (f *fakeTokenSetter) SetToken(tok string) { f.mu.Lock(); f.token = tok; f.mu.Unlock() }

func TestGitHub_ClearRemovesTheStoredToken(t *testing.T) {
	t.Parallel()
	for _, flag := range []string{"clear", "clearToken"} {
		_, cfgStore, h, jwt := newTestGitHubHandler(t, "")
		save := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, http.MethodPost, "/admin/api/v1/github", `{"token":"ghp_exampletoken1234"}`))
		if save.Response.StatusCode() != http.StatusOK {
			t.Fatalf("save: %d %s", save.Response.StatusCode(), save.Response.Body())
		}
		if stored, _ := cfgStore.Get(); stored.Token != "ghp_exampletoken1234" {
			t.Fatalf("token not stored: %+v", stored)
		}
		rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, http.MethodPost, "/admin/api/v1/github", `{"`+flag+`":true}`))
		if rr.Response.StatusCode() != http.StatusOK {
			t.Fatalf("%s: %d %s", flag, rr.Response.StatusCode(), rr.Response.Body())
		}
		if stored, _ := cfgStore.Get(); stored.Token != "" {
			t.Fatalf("B27: the GitHub token cannot be removed with %q: %+v", flag, stored)
		}
		if strings.Contains(string(rr.Response.Body()), "ghp_exampletoken1234") {
			t.Error("the token must never be echoed")
		}
	}
}

// ---- reload: no message loss, per-credential session files

type orderedManager struct {
	telegram.NopNotifier
	name string
	cfg  telegram.Config
	log  *orderLog
}

type orderLog struct {
	mu     sync.Mutex
	events []string
}

func (l *orderLog) add(e string) { l.mu.Lock(); l.events = append(l.events, e); l.mu.Unlock() }

func (m *orderedManager) Start(context.Context) { m.log.add("start:" + m.name) }
func (m *orderedManager) Stop()                 { m.log.add("stop:" + m.name) }

type orderNotifierSetter struct{ log *orderLog }

func (s orderNotifierSetter) SetNotifier(n telegram.Notifier) {
	if m, ok := n.(*orderedManager); ok {
		s.log.add("notify:" + m.name)
		return
	}
	s.log.add("notify:nop")
}

func TestTelegram_ReloadSwitchesNotifierBeforeStoppingTheOldManager(t *testing.T) {
	t.Parallel()
	cfgStore, err := store.NewTelegramConfigStore(newTestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	log := &orderLog{}
	handler := NewTelegramConfigHandler(t.Context(), cfgStore, filepath.Join(t.TempDir(), "telegram-session.json"), orderNotifierSetter{log})
	n := 0
	handler.newManager = func(cfg telegram.Config) telegramManagerLifecycle {
		n++
		return &orderedManager{name: string(rune('A' + n - 1)), cfg: cfg, log: log}
	}
	cfg := store.TelegramConfig{AppID: 1, AppHash: "h1", BotToken: "t1", ChatUsername: "@a", Enabled: true}
	handler.apply(cfg)
	cfg.BotToken = "t2"
	handler.apply(cfg)

	// The old manager (A) may only stop AFTER new messages are routed to B, and B may
	// only start after A drained and stopped (they would share one connection).
	want := []string{"notify:A", "start:A", "notify:B", "stop:A", "start:B"}
	log.mu.Lock()
	defer log.mu.Unlock()
	if strings.Join(log.events, ",") != strings.Join(want, ",") {
		t.Fatalf("B27: reload order = %v, want %v (messages queued during a reload are lost otherwise)", log.events, want)
	}
}

func TestTelegram_SessionFileIsPerCredentialSet(t *testing.T) {
	t.Parallel()
	cfgStore, err := store.NewTelegramConfigStore(newTestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	handler := NewTelegramConfigHandler(t.Context(), cfgStore, filepath.Join(t.TempDir(), "telegram-session.json"), &fakeNotifierSetter{})
	var paths []string
	handler.newManager = func(cfg telegram.Config) telegramManagerLifecycle {
		paths = append(paths, cfg.SessionPath)
		return &fakeTelegramManager{}
	}
	base := store.TelegramConfig{AppID: 1, AppHash: "hash-one", BotToken: "token-one", ChatUsername: "@a", Enabled: true}
	handler.apply(base)
	handler.apply(base) // same credentials, e.g. a chat change
	other := base
	other.BotToken = "token-two"
	handler.apply(other)

	if len(paths) != 3 || paths[0] != paths[1] || paths[0] == paths[2] {
		t.Fatalf("B27: session paths = %v; want one stable file per credential set", paths)
	}
	for _, p := range paths {
		if strings.Contains(p, "token-") || strings.Contains(p, "hash-one") {
			t.Errorf("the session file name must not contain credentials: %s", p)
		}
		if !strings.HasPrefix(filepath.Base(p), "telegram-session") || !strings.HasSuffix(p, ".json") {
			t.Errorf("unexpected session file name %s", p)
		}
	}
}

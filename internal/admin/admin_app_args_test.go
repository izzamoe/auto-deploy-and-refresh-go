package admin

import (
	"net/http"
	"strings"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/store"

	"github.com/cloudwego/hertz/pkg/app/server"
)

func newTestAppArgsHertz(t *testing.T) (*server.Hertz, *store.AppStore, *JWTHandler) {
	t.Helper()
	jwt := testJWTHandler(t)
	db := newTestDB(t)
	appStore, err := store.NewAppStore(db)
	if err != nil {
		t.Fatalf("NewAppStore: %v", err)
	}
	argsStore, err := store.NewAppArgsStore(db)
	if err != nil {
		t.Fatalf("NewAppArgsStore: %v", err)
	}
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	RegisterAppArgsRoutesHertz(h, NewAppArgsHandler(appStore, argsStore), HertzSessionAuthMiddleware(jwt, testAuthenticator()))
	return h, appStore, jwt
}

type appArgsResponse struct {
	Status string   `json:"status"`
	Args   []string `json:"args"`
	Errors []string `json:"errors"`
}

func createTestAppArgsApp(t *testing.T, appStore *store.AppStore) *store.App {
	t.Helper()
	a, err := appStore.Create("myapp", "secret", "/opt/myapp/myapp", "myapp.service", "owner/repo", "myapp-linux")
	if err != nil {
		t.Fatalf("Create app: %v", err)
	}
	return a
}

func TestGetAppArgsHertzEmptyByDefault(t *testing.T) {
	t.Parallel()
	h, appStore, jwt := newTestAppArgsHertz(t)
	a := createTestAppArgsApp(t, appStore)

	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "GET", "/admin/api/apps/"+a.ID+"/args", ""))
	if rr.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Response.StatusCode(), string(rr.Response.Body()))
	}
	// An app with no stored args must serialise as [] and never as null, which
	// the SPA would have to special-case.
	if body := string(rr.Response.Body()); !strings.Contains(body, `"args":[]`) {
		t.Fatalf("expected an empty args array, got %s", body)
	}
}

func TestSaveAndGetAppArgsHertzRoundTrip(t *testing.T) {
	t.Parallel()
	h, appStore, jwt := newTestAppArgsHertz(t)
	a := createTestAppArgsApp(t, appStore)

	payload := `{"args":["--port","8080","--message","hello world"]}`
	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "PUT", "/admin/api/apps/"+a.ID+"/args", payload))
	if rr.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Response.StatusCode(), string(rr.Response.Body()))
	}
	saved := decodeAdminAPIResponseHertz[appArgsResponse](t, rr)
	if saved.Status != "updated" {
		t.Fatalf("unexpected status: %+v", saved)
	}
	if len(saved.Args) != 4 || saved.Args[3] != "hello world" {
		t.Fatalf("unexpected saved args: %v", saved.Args)
	}

	rr = serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "GET", "/admin/api/apps/"+a.ID+"/args", ""))
	got := decodeAdminAPIResponseHertz[appArgsResponse](t, rr)
	if len(got.Args) != 4 || got.Args[0] != "--port" || got.Args[1] != "8080" || got.Args[3] != "hello world" {
		t.Fatalf("args did not round trip: %v", got.Args)
	}
}

func TestSaveAppArgsHertzClearsWithEmptyList(t *testing.T) {
	t.Parallel()
	h, appStore, jwt := newTestAppArgsHertz(t)
	a := createTestAppArgsApp(t, appStore)

	serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "PUT", "/admin/api/apps/"+a.ID+"/args", `{"args":["--port","8080"]}`))
	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "PUT", "/admin/api/apps/"+a.ID+"/args", `{"args":[]}`))
	if rr.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Response.StatusCode(), string(rr.Response.Body()))
	}

	rr = serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "GET", "/admin/api/apps/"+a.ID+"/args", ""))
	got := decodeAdminAPIResponseHertz[appArgsResponse](t, rr)
	if len(got.Args) != 0 {
		t.Fatalf("expected args cleared, got %v", got.Args)
	}
}

func TestSaveAppArgsHertzRejectsControlCharacters(t *testing.T) {
	t.Parallel()
	h, appStore, jwt := newTestAppArgsHertz(t)
	a := createTestAppArgsApp(t, appStore)

	payload := `{"args":["--flag","x\nExecStartPre=/bin/malicious"]}`
	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "PUT", "/admin/api/apps/"+a.ID+"/args", payload))
	if rr.Response.StatusCode() != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Response.StatusCode(), string(rr.Response.Body()))
	}

	rr = serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "GET", "/admin/api/apps/"+a.ID+"/args", ""))
	got := decodeAdminAPIResponseHertz[appArgsResponse](t, rr)
	if len(got.Args) != 0 {
		t.Fatalf("rejected args must not be persisted, got %v", got.Args)
	}
}

func TestAppArgsHertzUnknownApp404(t *testing.T) {
	t.Parallel()
	h, _, jwt := newTestAppArgsHertz(t)

	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "GET", "/admin/api/apps/missing/args", ""))
	if rr.Response.StatusCode() != http.StatusNotFound {
		t.Fatalf("expected 404 on GET, got %d", rr.Response.StatusCode())
	}

	rr = serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "PUT", "/admin/api/apps/missing/args", `{"args":[]}`))
	if rr.Response.StatusCode() != http.StatusNotFound {
		t.Fatalf("expected 404 on PUT, got %d", rr.Response.StatusCode())
	}
}

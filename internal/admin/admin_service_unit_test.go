package admin

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/deploy"
	"github.com/izzamoe/auto-deploy/internal/store"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

// stubServiceUnitEnv redirects deploy's package-level systemdUnitDir and
// runSystemctl vars (exported test hooks) so these tests never touch the
// real /etc/systemd/system or invoke the real systemctl binary.
func stubServiceUnitEnv(t *testing.T) (dir string, calls *[][]string) {
	t.Helper()
	dir = t.TempDir()
	restoreDir := deploy.SetSystemdUnitDirForTest(dir)
	var recorded [][]string
	restoreSystemctl := deploy.SetRunSystemctlForTest(func(name string, args ...string) ([]byte, error) {
		call := append([]string{name}, args...)
		recorded = append(recorded, call)
		return []byte("ok"), nil
	})
	t.Cleanup(func() {
		restoreDir()
		restoreSystemctl()
	})
	return dir, &recorded
}

func newTestServiceUnitAdminHertz(t *testing.T) (*server.Hertz, *store.AppStore, *JWTHandler) {
	h, appStore, _, jwt := newTestServiceUnitAdminHertzWithArgs(t)
	return h, appStore, jwt
}

// newTestServiceUnitAdminHertzWithArgs is the full variant that also hands back
// the args store, so a test can seed ExecStart flags before rendering a unit.
func newTestServiceUnitAdminHertzWithArgs(t *testing.T) (*server.Hertz, *store.AppStore, *store.AppArgsStore, *JWTHandler) {
	t.Helper()
	jwt := testJWTHandler(t)
	db := newTestDB(t)
	appStore, err := store.NewAppStore(db)
	if err != nil {
		t.Fatalf("NewAppStore: %v", err)
	}
	envStore, err := store.NewAppEnvStore(db)
	if err != nil {
		t.Fatalf("NewAppEnvStore: %v", err)
	}
	argsStore, err := store.NewAppArgsStore(db)
	if err != nil {
		t.Fatalf("NewAppArgsStore: %v", err)
	}
	handler := NewServiceUnitAdminHandler(appStore, envStore, argsStore)
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	RegisterServiceUnitRoutesHertz(h, handler, HertzSessionAuthMiddleware(jwt, testAuthenticator()))
	return h, appStore, argsStore, jwt
}

// TestPreviewServiceUnitHertzIncludesStoredArgs proves the stored command-line
// arguments actually reach the rendered unit — the wiring between
// AppArgsStore, loadRuntimeInto and RenderServiceUnit.
func TestPreviewServiceUnitHertzIncludesStoredArgs(t *testing.T) {
	// Not t.Parallel(): mutates shared package-level deploy vars via stubServiceUnitEnv.
	stubServiceUnitEnv(t)
	h, appStore, argsStore, jwt := newTestServiceUnitAdminHertzWithArgs(t)
	a := createTestServiceUnitApp(t, appStore, "myapp.service")
	if err := argsStore.Set(a.ID, []string{"--port", "8080", "--message", "hello world"}); err != nil {
		t.Fatalf("Set args: %v", err)
	}

	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "GET", "/admin/api/apps/"+a.ID+"/service-unit/preview", ""))
	if rr.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Response.StatusCode(), string(rr.Response.Body()))
	}
	body := decodeAdminAPIResponseHertz[struct {
		Unit string `json:"unit"`
	}](t, rr)

	wantExec := `ExecStart=/opt/myapp/myapp "--port" "8080" "--message" "hello world"` + "\n"
	if !strings.Contains(body.Unit, wantExec) {
		t.Fatalf("rendered unit missing args:\n%s\nwant it to contain\n%q", body.Unit, wantExec)
	}
}

// TestApplyServiceUnitHertzWritesArgs proves the applied (on-disk) unit file
// carries the arguments too, not just the preview.
func TestApplyServiceUnitHertzWritesArgs(t *testing.T) {
	// Not t.Parallel(): mutates shared package-level deploy vars via stubServiceUnitEnv.
	dir, _ := stubServiceUnitEnv(t)
	h, appStore, argsStore, jwt := newTestServiceUnitAdminHertzWithArgs(t)
	a := createTestServiceUnitApp(t, appStore, "myapp.service")
	if err := argsStore.Set(a.ID, []string{"--config=/etc/myapp.yaml"}); err != nil {
		t.Fatalf("Set args: %v", err)
	}

	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/apps/"+a.ID+"/service-unit/apply", ""))
	if rr.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Response.StatusCode(), string(rr.Response.Body()))
	}

	written, err := os.ReadFile(filepath.Join(dir, "myapp.service"))
	if err != nil {
		t.Fatalf("read written unit: %v", err)
	}
	if !strings.Contains(string(written), `ExecStart=/opt/myapp/myapp "--config=/etc/myapp.yaml"`+"\n") {
		t.Fatalf("written unit missing args:\n%s", written)
	}
}

func createTestServiceUnitApp(t *testing.T, appStore *store.AppStore, serviceName string) *store.App {
	t.Helper()
	a, err := appStore.Create("myapp", "secret", "/opt/myapp/myapp", serviceName, "owner/repo", "myapp-linux")
	if err != nil {
		t.Fatalf("Create app: %v", err)
	}
	return a
}

func TestPreviewServiceUnitHertzReturnsUnitText(t *testing.T) {
	// Not t.Parallel(): mutates shared package-level deploy vars via stubServiceUnitEnv.
	stubServiceUnitEnv(t)
	h, appStore, jwt := newTestServiceUnitAdminHertz(t)
	a := createTestServiceUnitApp(t, appStore, "myapp.service")

	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "GET", "/admin/api/apps/"+a.ID+"/service-unit/preview", ""))
	if rr.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Response.StatusCode(), string(rr.Response.Body()))
	}
	body := decodeAdminAPIResponseHertz[struct {
		Unit string `json:"unit"`
	}](t, rr)
	if body.Unit == "" {
		t.Fatal("expected non-empty unit text")
	}
	if body.Unit[:2] != "# " {
		t.Fatalf("expected unit text to start with marker comment, got %q", body.Unit)
	}
}

func TestPreviewServiceUnitHertzNotFound(t *testing.T) {
	// Not t.Parallel(): mutates shared package-level deploy vars via stubServiceUnitEnv.
	stubServiceUnitEnv(t)
	h, _, jwt := newTestServiceUnitAdminHertz(t)

	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "GET", "/admin/api/apps/missing/service-unit/preview", ""))
	if rr.Response.StatusCode() != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Response.StatusCode())
	}
}

func TestApplyServiceUnitHertzSuccess(t *testing.T) {
	// Not t.Parallel(): mutates shared package-level deploy vars via stubServiceUnitEnv.
	dir, calls := stubServiceUnitEnv(t)
	h, appStore, jwt := newTestServiceUnitAdminHertz(t)
	a := createTestServiceUnitApp(t, appStore, "myapp.service")

	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/apps/"+a.ID+"/service-unit/apply", ""))
	if rr.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Response.StatusCode(), string(rr.Response.Body()))
	}
	body := decodeAdminAPIResponseHertz[adminAPIStatusResponse](t, rr)
	if body.Status != "ok" {
		t.Fatalf("unexpected status response: %+v", body)
	}

	if _, err := os.Stat(filepath.Join(dir, "myapp.service")); err != nil {
		t.Fatalf("expected unit file written: %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("expected 2 systemctl calls, got %v", *calls)
	}
}

func TestApplyServiceUnitHertzRefusesHandWritten(t *testing.T) {
	// Not t.Parallel(): mutates shared package-level deploy vars via stubServiceUnitEnv.
	dir, calls := stubServiceUnitEnv(t)
	h, appStore, jwt := newTestServiceUnitAdminHertz(t)
	a := createTestServiceUnitApp(t, appStore, "myapp.service")

	handWritten := "# hand written\n[Unit]\nDescription=custom\n"
	if err := os.WriteFile(filepath.Join(dir, "myapp.service"), []byte(handWritten), 0644); err != nil {
		t.Fatalf("write hand-written unit file: %v", err)
	}

	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/apps/"+a.ID+"/service-unit/apply", ""))
	if rr.Response.StatusCode() != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", rr.Response.StatusCode(), string(rr.Response.Body()))
	}
	if len(*calls) != 0 {
		t.Fatalf("expected no systemctl calls on refusal, got %v", *calls)
	}

	after, err := os.ReadFile(filepath.Join(dir, "myapp.service"))
	if err != nil {
		t.Fatalf("read unit file: %v", err)
	}
	if string(after) != handWritten {
		t.Fatalf("hand-written unit file was modified")
	}
}

func TestApplyServiceUnitHertzNotFound(t *testing.T) {
	// Not t.Parallel(): mutates shared package-level deploy vars via stubServiceUnitEnv.
	stubServiceUnitEnv(t)
	h, _, jwt := newTestServiceUnitAdminHertz(t)

	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/apps/missing/service-unit/apply", ""))
	if rr.Response.StatusCode() != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Response.StatusCode())
	}
}

func TestServiceUnitRoutesRequireAuth(t *testing.T) {
	// Not t.Parallel(): mutates shared package-level deploy vars via stubServiceUnitEnv.
	stubServiceUnitEnv(t)
	h, appStore, _ := newTestServiceUnitAdminHertz(t)
	a := createTestServiceUnitApp(t, appStore, "myapp.service")

	c := app.NewContext(0)
	c.Request.SetRequestURI("/admin/api/apps/" + a.ID + "/service-unit/preview")
	c.Request.SetMethod("GET")

	h.ServeHTTP(context.Background(), c)
	if c.Response.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", c.Response.StatusCode())
	}
}

package admin

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/store"
)

// T050 (FR-028, FR-055, FR-076, B55): atomic create with env + args, the webhook secret
// shown once in the 201, duplicates are 409, and the secret can be rotated.

func appJSONWith(over map[string]any) string {
	m := map[string]any{
		"name": "n-app", "binaryPath": "/opt/n-app/n-app", "serviceName": "n-app.service",
		"githubRepo": "owner/n-app", "artifactName": "n-app-linux",
	}
	for k, v := range over {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func TestCreateApp_InvalidEnvCreatesNothing(t *testing.T) {
	t.Parallel()
	h, appStore, _, jwt := newTestAdminAPIHertz(t)
	cases := map[string]map[string]any{
		"invalid env name":       {"envVars": []map[string]string{{"name": "OK", "value": "1"}, {"name": "1BAD", "value": "x"}}},
		"env name with newline":  {"envVars": []map[string]string{{"name": "A\nB", "value": "x"}}},
		"newline in an argument": {"args": []string{"fine", "bad\narg"}},
		"too many arguments":     {"args": make([]string, store.MaxServiceArgs+1)},
	}
	for name, over := range cases {
		over["webhookSecret"] = strings.Repeat("z", 40)
		rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps", appJSONWith(over)))
		if rr.Response.StatusCode() != http.StatusBadRequest || !strings.Contains(string(rr.Response.Body()), "validation_failed") {
			t.Errorf("%s: %d %s, want 400 validation_failed", name, rr.Response.StatusCode(), rr.Response.Body())
		}
	}
	apps, _ := appStore.List()
	if len(apps) != 0 {
		t.Errorf("%d apps were created by invalid requests, want none", len(apps))
	}
	if found, _ := appStore.GetBySecretHash(store.HashSecret(strings.Repeat("z", 40))); found != nil {
		t.Error("an invalid request left a usable webhook secret behind")
	}
}

func TestCreateApp_StoresEnvAndArgsAtomically(t *testing.T) {
	t.Parallel()
	h, appStore, _, jwt := newTestAdminAPIHertz(t)
	envs, _ := store.NewAppEnvStore(appStore.DB())
	args, _ := store.NewAppArgsStore(appStore.DB())
	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps", appJSONWith(map[string]any{
		"envVars": []map[string]string{{"name": "PORT", "value": "8080"}},
		"args":    []string{"--port", "8080"},
	})))
	if rr.Response.StatusCode() != http.StatusCreated {
		t.Fatalf("create = %d %s", rr.Response.StatusCode(), rr.Response.Body())
	}
	var out struct {
		App struct {
			ID string `json:"id"`
		} `json:"app"`
	}
	_ = json.Unmarshal(rr.Response.Body(), &out)
	if got, _ := envs.Get(out.App.ID); len(got) != 1 || got[0].Name != "PORT" || got[0].Value != "8080" {
		t.Errorf("env = %v", got)
	}
	if got, _ := args.Get(out.App.ID); len(got) != 2 || got[0] != "--port" {
		t.Errorf("args = %v", got)
	}
}

func TestCreateApp_ReturnsWebhookSecretOnce(t *testing.T) {
	t.Parallel()
	h, appStore, _, jwt := newTestAdminAPIHertz(t)

	// no secret in the request: the server generates 32 random bytes
	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps", appJSONWith(nil)))
	if rr.Response.StatusCode() != http.StatusCreated {
		t.Fatalf("create = %d %s", rr.Response.StatusCode(), rr.Response.Body())
	}
	var created struct {
		Status        string `json:"status"`
		WebhookSecret string `json:"webhookSecret"`
		App           struct {
			ID string `json:"id"`
		} `json:"app"`
	}
	if err := json.Unmarshal(rr.Response.Body(), &created); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(created.WebhookSecret)
	if err != nil || len(raw) != 32 {
		t.Fatalf("webhookSecret %q is not 32 random bytes (base64url): %d bytes, %v", created.WebhookSecret, len(raw), err)
	}
	if found, _ := appStore.GetBySecretHash(store.HashSecret(created.WebhookSecret)); found == nil || found.ID != created.App.ID {
		t.Error("the returned secret must authenticate the new app")
	}

	// shown ONCE: no read endpoint ever returns it again
	for _, path := range []string{"/admin/api/v1/apps/" + created.App.ID, "/admin/api/v1/apps"} {
		g := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "GET", path, ""))
		if strings.Contains(string(g.Response.Body()), created.WebhookSecret) || strings.Contains(string(g.Response.Body()), "webhookSecret") {
			t.Errorf("GET %s exposes the webhook secret: %s", path, g.Response.Body())
		}
	}

	// a secret supplied by the client is echoed in the 201 (and never again)
	supplied := strings.Repeat("k", 40)
	rr2 := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps", appJSONWith(map[string]any{
		"name": "second", "binaryPath": "/opt/second/second", "serviceName": "second.service", "webhookSecret": supplied,
	})))
	var second struct {
		WebhookSecret string `json:"webhookSecret"`
	}
	_ = json.Unmarshal(rr2.Response.Body(), &second)
	if rr2.Response.StatusCode() != http.StatusCreated || second.WebhookSecret != supplied {
		t.Errorf("supplied secret: %d %s", rr2.Response.StatusCode(), rr2.Response.Body())
	}
}

func TestCreateApp_DuplicateIs409WithoutRevealingWhichField(t *testing.T) {
	t.Parallel()
	h, _, _, jwt := newTestAdminAPIHertz(t)
	body := appJSONWith(map[string]any{"webhookSecret": strings.Repeat("d", 40)})
	if rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps", body)); rr.Response.StatusCode() != http.StatusCreated {
		t.Fatalf("first create = %d %s", rr.Response.StatusCode(), rr.Response.Body())
	}
	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps", body))
	got := string(rr.Response.Body())
	if rr.Response.StatusCode() != http.StatusConflict || !strings.Contains(got, `"code":"duplicate"`) {
		t.Fatalf("duplicate create = %d %s, want 409 duplicate", rr.Response.StatusCode(), got)
	}
	if strings.Contains(got, strings.Repeat("d", 40)) {
		t.Errorf("the 409 must not echo the secret: %s", got)
	}
}

func TestRotateWebhookSecret_32Bytes(t *testing.T) {
	t.Parallel()
	h, appStore, _, jwt := newTestAdminAPIHertz(t)
	oldSecret := strings.Repeat("o", 40)
	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps", appJSONWith(map[string]any{"webhookSecret": oldSecret})))
	var created struct {
		App struct {
			ID string `json:"id"`
		} `json:"app"`
	}
	_ = json.Unmarshal(rr.Response.Body(), &created)

	rot := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps/"+created.App.ID+"/webhook-secret/rotate", ""))
	if rot.Response.StatusCode() != http.StatusOK {
		t.Fatalf("rotate = %d %s", rot.Response.StatusCode(), rot.Response.Body())
	}
	var out struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(rot.Response.Body(), &out)
	raw, err := base64.RawURLEncoding.DecodeString(out.Secret)
	if err != nil || len(raw) != 32 || len(out.Secret) < 43 {
		t.Fatalf("rotated secret %q is not 32 random bytes: %d bytes, %v", out.Secret, len(raw), err)
	}
	if found, _ := appStore.GetBySecretHash(store.HashSecret(oldSecret)); found != nil {
		t.Error("the old secret must stop working after rotation")
	}
	if found, _ := appStore.GetBySecretHash(store.HashSecret(out.Secret)); found == nil || found.ID != created.App.ID {
		t.Error("the new secret must authenticate the app")
	}

	two := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps/"+created.App.ID+"/webhook-secret/rotate", ""))
	var out2 struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(two.Response.Body(), &out2)
	if out2.Secret == out.Secret {
		t.Error("every rotation must produce a fresh secret")
	}

	missing := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps/nope/webhook-secret/rotate", ""))
	if missing.Response.StatusCode() != http.StatusNotFound {
		t.Errorf("rotate of an unknown app = %d, want 404", missing.Response.StatusCode())
	}
}

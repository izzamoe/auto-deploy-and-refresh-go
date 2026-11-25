package admin

import (
	"net/http"
	"strings"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/store"
)

// B31 / FR-055 (decision of the RUN 4 review): a webhook secret chosen by the client must
// be at least 32 characters; shorter ones are a 400 validation_failed with a field error.

func TestWebhookSecret_ShortClientSecretIsRejectedOnCreate(t *testing.T) {
	t.Parallel()
	h, appStore, _, jwt := newTestAdminAPIHertz(t)
	for _, short := range []string{"x", "short-secret", strings.Repeat("a", 31)} {
		rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps", appJSONWith(map[string]any{"webhookSecret": short})))
		body := string(rr.Response.Body())
		if rr.Response.StatusCode() != http.StatusBadRequest || !strings.Contains(body, `"code":"validation_failed"`) || !strings.Contains(body, `"field":"webhookSecret"`) {
			t.Errorf("B31: a %d-character secret: %d %s, want 400 validation_failed with field webhookSecret", len(short), rr.Response.StatusCode(), body)
		}
	}
	if apps, _ := appStore.List(); len(apps) != 0 {
		t.Errorf("%d apps were created with a weak secret", len(apps))
	}
	// 32 characters and an omitted secret (server-generated) are fine
	for _, ok := range []map[string]any{{"webhookSecret": strings.Repeat("a", 32)}, {"name": "gen", "binaryPath": "/opt/gen/gen", "serviceName": "gen.service"}} {
		if rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps", appJSONWith(ok))); rr.Response.StatusCode() != http.StatusCreated {
			t.Errorf("%v: %d %s, want 201", ok, rr.Response.StatusCode(), rr.Response.Body())
		}
	}
}

func TestWebhookSecret_ShortClientSecretIsRejectedOnUpdate(t *testing.T) {
	t.Parallel()
	h, appStore, _, jwt := newTestAdminAPIHertz(t)
	app, err := appStore.Create("old", "legacy-short", "/opt/old/old", "old.service", "o/r", "art")
	if err != nil {
		t.Fatal(err)
	}
	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "PUT", "/admin/api/v1/apps/"+app.ID, appJSONWith(map[string]any{"name": "old", "binaryPath": "/opt/old/old", "serviceName": "old.service", "webhookSecret": "tiny"})))
	if rr.Response.StatusCode() != http.StatusBadRequest || !strings.Contains(string(rr.Response.Body()), `"field":"webhookSecret"`) {
		t.Errorf("B31: updating to a 4-character secret: %d %s", rr.Response.StatusCode(), rr.Response.Body())
	}
	// an update that does not send a secret leaves a legacy short secret alone
	if rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "PUT", "/admin/api/v1/apps/"+app.ID, appJSONWith(map[string]any{"name": "renamed", "binaryPath": "/opt/old/old", "serviceName": "old.service"}))); rr.Response.StatusCode() != http.StatusOK {
		t.Errorf("update without a secret: %d %s", rr.Response.StatusCode(), rr.Response.Body())
	}
	if found, _ := appStore.GetBySecretHash(store.HashSecret("legacy-short")); found == nil {
		t.Error("the legacy secret must keep working (W5: old secrets stay accepted)")
	}
}

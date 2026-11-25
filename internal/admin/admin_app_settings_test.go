package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// T056 (FR-042): healthUrl (and the other per-app deploy settings) round-trip through
// create / get / update; an invalid healthUrl is a field error.

type appSettingsResp struct {
	App struct {
		ID            string `json:"id"`
		RunAsUser     string `json:"runAsUser"`
		Hardening     bool   `json:"hardening"`
		HealthURL     string `json:"healthUrl"`
		RequireDigest bool   `json:"requireDigest"`
	} `json:"app"`
}

func TestAppSettings_HealthURLRoundTrip(t *testing.T) {
	t.Parallel()
	h, _, _, jwt := newTestAdminAPIHertz(t)
	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps", appJSONWith(map[string]any{
		"healthUrl": "http://127.0.0.1:8080/healthz",
	})))
	if rr.Response.StatusCode() != http.StatusCreated {
		t.Fatalf("create = %d %s", rr.Response.StatusCode(), rr.Response.Body())
	}
	var created appSettingsResp
	_ = json.Unmarshal(rr.Response.Body(), &created)
	if created.App.HealthURL != "http://127.0.0.1:8080/healthz" {
		t.Errorf("created healthUrl = %q", created.App.HealthURL)
	}

	// Update without the field keeps it; with the field replaces it.
	upd := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "PUT", "/admin/api/v1/apps/"+created.App.ID, appJSONWith(map[string]any{"name": "renamed"})))
	if upd.Response.StatusCode() != http.StatusOK {
		t.Fatalf("update = %d %s", upd.Response.StatusCode(), upd.Response.Body())
	}
	var kept appSettingsResp
	_ = json.Unmarshal(upd.Response.Body(), &kept)
	if kept.App.HealthURL != "http://127.0.0.1:8080/healthz" {
		t.Errorf("an update without healthUrl must keep it, got %q", kept.App.HealthURL)
	}
	upd = serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "PUT", "/admin/api/v1/apps/"+created.App.ID, appJSONWith(map[string]any{"healthUrl": ""})))
	var cleared appSettingsResp
	_ = json.Unmarshal(upd.Response.Body(), &cleared)
	if upd.Response.StatusCode() != http.StatusOK || cleared.App.HealthURL != "" {
		t.Errorf("clearing healthUrl: %d %s", upd.Response.StatusCode(), upd.Response.Body())
	}

	get := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "GET", "/admin/api/v1/apps/"+created.App.ID, ""))
	if !strings.Contains(string(get.Response.Body()), `"healthUrl":""`) {
		t.Errorf("GET must expose healthUrl: %s", get.Response.Body())
	}
}

func TestAppSettings_InvalidHealthURLIs400WithField(t *testing.T) {
	t.Parallel()
	h, appStore, _, jwt := newTestAdminAPIHertz(t)
	for _, bad := range []string{"ftp://x/y", "javascript:alert(1)", "http://", "http://user:pw@host/", "/relative", "http://a b/"} {
		rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps", appJSONWith(map[string]any{"healthUrl": bad})))
		body := string(rr.Response.Body())
		if rr.Response.StatusCode() != http.StatusBadRequest || !strings.Contains(body, `"field":"healthUrl"`) {
			t.Errorf("healthUrl %q: %d %s, want 400 with field healthUrl", bad, rr.Response.StatusCode(), body)
		}
	}
	if apps, _ := appStore.List(); len(apps) != 0 {
		t.Errorf("%d apps created by invalid requests", len(apps))
	}
}

// Q-14: a NEW app requires a digest by default; the request can opt out.
func TestAppSettings_NewAppsRequireDigestByDefault(t *testing.T) {
	t.Parallel()
	h, _, _, jwt := newTestAdminAPIHertz(t)
	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps", appJSONWith(nil)))
	var created appSettingsResp
	_ = json.Unmarshal(rr.Response.Body(), &created)
	if rr.Response.StatusCode() != http.StatusCreated || !created.App.RequireDigest {
		t.Fatalf("default requireDigest = %v (%d %s), want true", created.App.RequireDigest, rr.Response.StatusCode(), rr.Response.Body())
	}
	rr = serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps", appJSONWith(map[string]any{
		"name": "second", "binaryPath": "/opt/second/second", "serviceName": "second.service", "requireDigest": false,
	})))
	var second appSettingsResp
	_ = json.Unmarshal(rr.Response.Body(), &second)
	if rr.Response.StatusCode() != http.StatusCreated || second.App.RequireDigest {
		t.Fatalf("explicit requireDigest=false = %v (%d %s)", second.App.RequireDigest, rr.Response.StatusCode(), rr.Response.Body())
	}
}

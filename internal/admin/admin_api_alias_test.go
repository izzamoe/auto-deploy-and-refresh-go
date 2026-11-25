package admin

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/izzamoe/auto-deploy/internal/store"
)

// aliasEngine mounts the apps API plus per-app args under v1 and then the legacy
// aliases, the way cmd/server does.
func aliasEngine(t *testing.T) (func(method, path string, hdr map[string]string, body string) *ut.ResponseRecorder, *store.AppStore) {
	t.Helper()
	h, appStore, _, _ := newTestAdminAPIHertz(t)
	argsStore, err := store.NewAppArgsStore(appStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	RegisterAppArgsRoutesHertz(h, NewAppArgsHandler(appStore, argsStore), testHertzSessionAuth(t))
	RegisterLegacyAPIAliases(h, testHertzSessionAuth(t))
	do := func(method, path string, hdr map[string]string, body string) *ut.ResponseRecorder {
		var hs []ut.Header
		for k, v := range hdr {
			hs = append(hs, ut.Header{Key: k, Value: v})
		}
		var b *ut.Body
		if body != "" {
			b = &ut.Body{Body: strings.NewReader(body), Len: len(body)}
		}
		return ut.PerformRequest(h.Engine, method, "http://app.test"+path, b, hs...)
	}
	return do, appStore
}

func browserHdr(t *testing.T) map[string]string {
	return map[string]string{
		"Cookie": testAdminSessionCookie(t), "X-Admin-Request": "true", "Sec-Fetch-Site": "same-origin", "Content-Type": "application/json",
	}
}

// Q-7: the pre-v1 paths keep working for one release, flagged as deprecated.
func TestLegacyAPIAliasesMirrorV1AndAreDeprecated(t *testing.T) {
	do, appStore := aliasEngine(t)
	app, err := appStore.Create("legacy", "secret-legacy", "/opt/legacy", "legacy.service", "o/r", "legacy")
	if err != nil {
		t.Fatal(err)
	}
	hdr := browserHdr(t)

	// Canonical path: no deprecation signal.
	w := do("GET", "/admin/api/v1/apps", hdr, "")
	if w.Code != http.StatusOK || w.Header().Get("Deprecation") != "" {
		t.Fatalf("v1 list: %d Deprecation=%q, want 200 and none", w.Code, w.Header().Get("Deprecation"))
	}

	// Alias: same answer plus Deprecation and Link to the v1 path.
	w = do("GET", "/admin/api/apps", hdr, "")
	if w.Code != http.StatusOK {
		t.Fatalf("legacy list: %d %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Deprecation"); got != "true" {
		t.Errorf("Deprecation = %q, want true", got)
	}
	if got := w.Header().Get("Link"); got != `</admin/api/v1/apps>; rel="successor-version"` {
		t.Errorf("Link = %q", got)
	}
	if !strings.Contains(w.Body.String(), "legacy") {
		t.Errorf("alias must serve the v1 handler's data: %s", w.Body.String())
	}

	// A parameterised alias keeps the resolved id in the Link.
	w = do("GET", "/admin/api/apps/"+app.ID+"/args", hdr, "")
	if w.Code != http.StatusOK || w.Header().Get("Link") != `</admin/api/v1/apps/`+app.ID+`/args>; rel="successor-version"` {
		t.Errorf("legacy args: %d Link=%q", w.Code, w.Header().Get("Link"))
	}

	// Mutations through the alias are still CSRF-protected by the same middleware.
	body := `{"name":"made-via-alias","secret":"secret-alias-1","binaryPath":"/opt/alias","serviceName":"alias.service","githubRepo":"o/alias","artifactName":"alias"}`
	noCSRF := map[string]string{"Cookie": testAdminSessionCookie(t), "Content-Type": "application/json"}
	if w := do("POST", "/admin/api/apps", noCSRF, body); w.Code != http.StatusForbidden {
		t.Errorf("alias mutation without CSRF headers: %d, want 403", w.Code)
	}
	if w := do("POST", "/admin/api/apps", hdr, body); w.Code != http.StatusCreated || w.Header().Get("Deprecation") != "true" {
		t.Errorf("alias mutation: %d Deprecation=%q, want 201 and true", w.Code, w.Header().Get("Deprecation"))
	}
}

func TestLegacyAPIAliasesRequireAuthAndRefuseAdminPasswordViaBasic(t *testing.T) {
	do, _ := aliasEngine(t)

	w := do("GET", "/admin/api/apps", nil, "")
	if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != "" {
		t.Errorf("no credentials: %d WWW-Authenticate=%q, want 401 without challenge", w.Code, w.Header().Get("WWW-Authenticate"))
	}
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:secret"))
	w = do("GET", "/admin/api/apps", map[string]string{"Authorization": basic}, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("admin password via Basic on an alias: %d, want 401 (Q-7: Basic password stays rejected)", w.Code)
	}
}

// New in v1 means no alias: those paths fall to the JSON 404, never to other handlers.
func TestLegacyAPIAliasesSkipEndpointsNewInV1(t *testing.T) {
	do, _ := aliasEngine(t)
	for _, p := range []string{"/admin/api/stats", "/admin/api/tokens", "/admin/api/session"} {
		w := do("GET", p, browserHdr(t), "")
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"not_found"`) {
			t.Errorf("%s: %d %s, want the JSON 404", p, w.Code, w.Body.String())
		}
	}
}

// B15/FR-020: unknown API paths and wrong prefixes answer the JSON contract, with code not_found.
func TestAPICatchAllIsJSONNotFound(t *testing.T) {
	do, _ := aliasEngine(t)
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		for _, p := range []string{"/admin/api/v1", "/admin/api/v1/", "/admin/api/v1/does/not/exist"} {
			w := do(method, p, browserHdr(t), "")
			if w.Code != http.StatusNotFound {
				t.Errorf("%s %s: %d, want 404", method, p, w.Code)
				continue
			}
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("%s %s: Content-Type %q", method, p, ct)
			}
			if !strings.Contains(w.Body.String(), `"code":"not_found"`) || !strings.Contains(w.Body.String(), `"status":"error"`) {
				t.Errorf("%s %s: body %s lacks the not_found error contract", method, p, w.Body.String())
			}
		}
	}
	// Unauthenticated callers learn nothing about which paths exist.
	if w := do("GET", "/admin/api/v1/does-not-exist", nil, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated unknown path: %d, want 401", w.Code)
	}
}

// Every error answer of the apps API has the whole envelope: status, code, error, errors[].
func TestAPIErrorEnvelopeIsComplete(t *testing.T) {
	do, _ := aliasEngine(t)
	cases := []struct {
		name, method, path, body string
		hdr                      map[string]string
		status                   int
		code                     string
	}{
		{"validation", "POST", "/admin/api/v1/apps", `{"name":"x"}`, browserHdr(t), 400, "validation_failed"},
		{"bad json", "POST", "/admin/api/v1/apps", `{`, browserHdr(t), 400, "invalid_json"},
		{"unknown app", "GET", "/admin/api/v1/apps/nope", ``, browserHdr(t), 404, "not_found"},
		{"no session", "GET", "/admin/api/v1/apps", ``, nil, 401, "session_expired"},
	}
	for _, tc := range cases {
		w := do(tc.method, tc.path, tc.hdr, tc.body)
		if w.Code != tc.status {
			t.Errorf("%s: status %d, want %d (%s)", tc.name, w.Code, tc.status, w.Body.String())
			continue
		}
		b := w.Body.String()
		for _, want := range []string{`"status":"error"`, `"code":"` + tc.code + `"`, `"error":"`, `"errors":[`} {
			if !strings.Contains(b, want) {
				t.Errorf("%s: body %s lacks %s", tc.name, b, want)
			}
		}
	}
	// validation errors are objects with field-level detail, not bare strings
	w := do("POST", "/admin/api/v1/apps", browserHdr(t), `{"name":"x"}`)
	if !strings.Contains(w.Body.String(), `"errors":[{"field":"binaryPath","code":"validation_failed","message":`) {
		t.Errorf("validation entries must be {field,code,message} objects: %s", w.Body.String())
	}
}

package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// T049 (B15, FR-036): manual deploy of a malformed tag is 400 invalid_tag, never a
// 500, and the validation detail (the offending character) is not echoed back.
func TestManualDeploy_InvalidTagIs400InvalidTagWithoutLeak(t *testing.T) {
	t.Parallel()
	h, _, _, jwt := newTestAdminAPIHertz(t)
	created := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps", validAppJSON(map[string]string{"name": "tagapp"})))
	var app struct {
		App struct {
			ID string `json:"id"`
		} `json:"app"`
	}
	if err := json.Unmarshal(created.Response.Body(), &app); err != nil || app.App.ID == "" {
		t.Fatalf("create app: %d %s", created.Response.StatusCode(), created.Response.Body())
	}

	for _, tag := range []string{"v1 bad", "a/../b", "/lead", "trail/", strings.Repeat("a", 300), "v1;rm"} {
		body, _ := json.Marshal(map[string]string{"tag": tag})
		rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps/"+app.App.ID+"/deploy", string(body)))
		got := string(rr.Response.Body())
		if rr.Response.StatusCode() != http.StatusBadRequest || !strings.Contains(got, `"code":"invalid_tag"`) {
			t.Errorf("tag %q: %d %s, want 400 invalid_tag", tag, rr.Response.StatusCode(), got)
		}
		for _, leak := range []string{"illegal character", "contains", "leading or trailing", "too long", "ErrInvalidTag", "invalid tag:"} {
			if strings.Contains(got, leak) {
				t.Errorf("tag %q: response leaks validation internals (%q): %s", tag, leak, got)
			}
		}
	}

	// a valid tag still queues
	ok := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps/"+app.App.ID+"/deploy", `{"tag":"v1.2.3"}`))
	if ok.Response.StatusCode() >= 400 {
		t.Errorf("a valid tag must still queue: %d %s", ok.Response.StatusCode(), ok.Response.Body())
	}
}

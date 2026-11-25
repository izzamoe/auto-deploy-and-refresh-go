package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/deploy"
)

// T047 (B7, B22, FR-040, Q-11): the apps API refuses unsafe binaryPath, serviceName,
// githubRepo and artifactName with 400 validation_failed and errors[].field.

type validationBody struct {
	Code   string `json:"code"`
	Errors []struct {
		Field   string `json:"field"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

func validAppJSON(over map[string]string) string {
	m := map[string]string{
		"name": "v-app", "secret": "secret-v", "binaryPath": "/opt/v-app/v-app", "serviceName": "v-app.service",
		"githubRepo": "owner/v-app", "artifactName": "v-app-linux",
	}
	for k, v := range over {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func TestAppAPI_RejectsUnsafeFieldsWithFieldErrors(t *testing.T) {
	t.Parallel()
	h, _, _, jwt := newTestAdminAPIHertz(t)
	cases := []struct {
		name  string
		over  map[string]string
		field string
	}{
		{"newline injection in binaryPath (B7)", map[string]string{"binaryPath": "/opt/x\nExecStartPre=/bin/sh -c 'id > /tmp/pwned'\n/bin"}, "binaryPath"},
		{"relative binaryPath", map[string]string{"binaryPath": "app"}, "binaryPath"},
		{"binaryPath with ..", map[string]string{"binaryPath": "/opt/a/../../etc/x"}, "binaryPath"},
		{"binaryPath with percent", map[string]string{"binaryPath": "/opt/100%h/a"}, "binaryPath"},
		{"binaryPath in /etc (Q-11 denylist)", map[string]string{"binaryPath": "/etc/app"}, "binaryPath"},
		{"binaryPath in /usr/bin (Q-11 denylist)", map[string]string{"binaryPath": "/usr/bin/app"}, "binaryPath"},
		{"serviceName with leading dash (B22)", map[string]string{"serviceName": "-now.service"}, "serviceName"},
		{"serviceName with slash", map[string]string{"serviceName": "a/b.service"}, "serviceName"},
		{"githubRepo not owner/repo", map[string]string{"githubRepo": "just-a-name"}, "githubRepo"},
		{"githubRepo with traversal", map[string]string{"githubRepo": "../repo"}, "githubRepo"},
		{"artifactName with slash", map[string]string{"artifactName": "a/b"}, "artifactName"},
		{"artifactName with ..", map[string]string{"artifactName": "..x"}, "artifactName"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps", validAppJSON(tc.over)))
			if rr.Response.StatusCode() != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rr.Response.StatusCode(), rr.Response.Body())
			}
			var body validationBody
			if err := json.Unmarshal(rr.Response.Body(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != "validation_failed" {
				t.Errorf("code = %q, want validation_failed", body.Code)
			}
			found := false
			for _, e := range body.Errors {
				if e.Field == tc.field && e.Message != "" {
					found = true
				}
			}
			if !found {
				t.Errorf("no errors[] entry with field %q: %s", tc.field, rr.Response.Body())
			}
		})
	}
}

func TestAppAPI_RejectsTheAutoDeployBinaryItself(t *testing.T) {
	t.Parallel()
	h, _, _, jwt := newTestAdminAPIHertz(t)
	self := deploy.OwnExecutable()
	if self == "" || strings.ContainsAny(self, " %$\\") {
		t.Skipf("own executable path %q is not usable for this check", self)
	}
	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps", validAppJSON(map[string]string{"binaryPath": self})))
	if rr.Response.StatusCode() != http.StatusBadRequest || !strings.Contains(string(rr.Response.Body()), "auto-deploy binary itself") {
		t.Fatalf("pointing an app at the manager binary = %d %s, want 400 naming the reason", rr.Response.StatusCode(), rr.Response.Body())
	}
}

func TestAppAPI_ReportsEveryInvalidFieldAtOnceAndUpdateIsValidatedToo(t *testing.T) {
	t.Parallel()
	h, _, _, jwt := newTestAdminAPIHertz(t)
	bad := validAppJSON(map[string]string{"binaryPath": "/etc/x", "serviceName": "-x", "githubRepo": "nope", "artifactName": "a/b"})
	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps", bad))
	var body validationBody
	_ = json.Unmarshal(rr.Response.Body(), &body)
	fields := map[string]bool{}
	for _, e := range body.Errors {
		fields[e.Field] = true
	}
	for _, f := range []string{"binaryPath", "serviceName", "githubRepo", "artifactName"} {
		if !fields[f] {
			t.Errorf("field %q missing from %s", f, rr.Response.Body())
		}
	}

	// a valid app can be created, and then an unsafe update of it is refused
	ok := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps", validAppJSON(nil)))
	if ok.Response.StatusCode() != http.StatusCreated {
		t.Fatalf("valid create = %d %s", ok.Response.StatusCode(), ok.Response.Body())
	}
	var created struct {
		App struct {
			ID string `json:"id"`
		} `json:"app"`
	}
	_ = json.Unmarshal(ok.Response.Body(), &created)
	up := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "PUT", "/admin/api/v1/apps/"+created.App.ID, validAppJSON(map[string]string{"binaryPath": "/bin/evil"})))
	if up.Response.StatusCode() != http.StatusBadRequest {
		t.Fatalf("update to /bin/evil = %d %s, want 400", up.Response.StatusCode(), up.Response.Body())
	}
}

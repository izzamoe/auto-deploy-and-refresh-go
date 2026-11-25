package webhook

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/izzamoe/auto-deploy/internal/admission"
	"github.com/izzamoe/auto-deploy/internal/httpx"
	"github.com/izzamoe/auto-deploy/internal/store"
	_ "modernc.org/sqlite"
)

// T049 (B15, FR-022, contracts/webhook.md W1, W2, W6): the webhook never puts an
// internal error string into a response, every error carries a machine `code`, the
// status codes of the existing contract are unchanged, and X-Request-ID is returned.

type fakeAdmitter struct{ result admission.AdmissionResult }

func (f fakeAdmitter) Admit(string, string) admission.AdmissionResult { return f.result }

func serve(t *testing.T, adm Admitter, authHeader, body string) *app.RequestContext {
	t.Helper()
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.Use(httpx.RequestID())
	h.POST("/webhook", Handler(adm))
	c := app.NewContext(0)
	c.Request.SetRequestURI("/webhook")
	c.Request.SetMethod(http.MethodPost)
	c.Request.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		c.Request.Header.Set("Authorization", authHeader)
	}
	c.Request.SetBodyString(body)
	h.ServeHTTP(context.Background(), c)
	return c
}

type wireBody struct {
	Status string `json:"status"`
	Tag    string `json:"tag"`
	Error  string `json:"error"`
	Code   string `json:"code"`
}

func decode(t *testing.T, c *app.RequestContext) wireBody {
	t.Helper()
	var b wireBody
	if err := json.Unmarshal(c.Response.Body(), &b); err != nil {
		t.Fatalf("response is not JSON: %s", c.Response.Body())
	}
	return b
}

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func realAdmitter(t *testing.T, maxPending int) (*admission.AdmissionService, *store.AppStore, *store.DeployQueue, string) {
	t.Helper()
	db := newDB(t)
	apps, err := store.NewAppStore(db)
	if err != nil {
		t.Fatal(err)
	}
	q, err := store.NewDeployQueue(db, maxPending)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err := apps.Create("a", "the-secret", "/opt/a/a", "a.service", "o/a", "a"); err != nil {
		t.Fatal(err)
	}
	return admission.NewAdmissionService(apps, q), apps, q, "Bearer the-secret"
}

func TestWebhook_SuccessShapesAreUnchanged(t *testing.T) {
	adm, _, _, auth := realAdmitter(t, 10)

	c := serve(t, adm, auth, `{"tag":"v1.2.3"}`)
	if c.Response.StatusCode() != http.StatusAccepted || string(c.Response.Body()) != `{"status":"queued","tag":"v1.2.3"}` {
		t.Fatalf("queued: %d %s", c.Response.StatusCode(), c.Response.Body())
	}
	c = serve(t, adm, auth, `{"tag":"v1.2.3"}`)
	if c.Response.StatusCode() != http.StatusOK || string(c.Response.Body()) != `{"status":"duplicate","tag":"v1.2.3"}` {
		t.Fatalf("duplicate: %d %s", c.Response.StatusCode(), c.Response.Body())
	}
}

func TestWebhook_ErrorsCarryCodeAndKeepTheirStatus(t *testing.T) {
	adm, _, _, auth := realAdmitter(t, 10)
	cases := []struct {
		name, auth, body string
		status           int
		errText, code    string
	}{
		{"no bearer", "", `{"tag":"v1"}`, 401, "unauthorized", "unauthorized"},
		{"unknown secret", "Bearer nope", `{"tag":"v1"}`, 401, "unauthorized", "unauthorized"},
		{"empty tag", auth, `{"tag":""}`, 400, "missing or empty tag", "validation_failed"},
		{"no tag field", auth, `{}`, 400, "missing or empty tag", "validation_failed"},
		{"not json", auth, `{`, 400, "missing or empty tag", "validation_failed"},
		{"tag with space", auth, `{"tag":"v1 bad"}`, 400, "invalid tag", "invalid_tag"},
		{"tag with dotdot", auth, `{"tag":"a/../b"}`, 400, "invalid tag", "invalid_tag"},
		{"tag with leading slash", auth, `{"tag":"/v1"}`, 400, "invalid tag", "invalid_tag"},
		{"tag too long", auth, `{"tag":"` + strings.Repeat("a", 300) + `"}`, 400, "invalid tag", "invalid_tag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := serve(t, adm, tc.auth, tc.body)
			b := decode(t, c)
			if c.Response.StatusCode() != tc.status || b.Status != "error" || b.Error != tc.errText || b.Code != tc.code {
				t.Errorf("got %d %s, want %d error=%q code=%q", c.Response.StatusCode(), c.Response.Body(), tc.status, tc.errText, tc.code)
			}
			if got := string(c.Response.Header.Peek(httpx.HeaderRequestID)); got == "" {
				t.Error("X-Request-ID must be returned on every response (W6)")
			}
		})
	}
}

func TestWebhook_QueueFullIs503WithCode(t *testing.T) {
	adm, _, _, auth := realAdmitter(t, 1)
	if c := serve(t, adm, auth, `{"tag":"v1"}`); c.Response.StatusCode() != http.StatusAccepted {
		t.Fatalf("first: %d %s", c.Response.StatusCode(), c.Response.Body())
	}
	c := serve(t, adm, auth, `{"tag":"v2"}`)
	b := decode(t, c)
	if c.Response.StatusCode() != http.StatusServiceUnavailable || b.Error != "queue full" || b.Code != "queue_full" {
		t.Fatalf("second: %d %s, want 503 error=queue full code=queue_full", c.Response.StatusCode(), c.Response.Body())
	}
}

// W1: an internal failure is a generic 500; the raw error (SQL text, paths, ...) goes to
// the server log with the request ID only.
func TestWebhook_InternalErrorLeaksNothing(t *testing.T) {
	secret := `open /var/lib/auto-deploy/queue.db: disk I/O error (password=hunter2)`
	adm := fakeAdmitter{admission.AdmissionResult{Outcome: admission.OutcomeError, Error: errors.New(secret)}}
	c := serve(t, adm, "Bearer x", `{"tag":"v1"}`)
	b := decode(t, c)
	if c.Response.StatusCode() != http.StatusInternalServerError || b.Error != "internal error" || b.Code != "internal_error" {
		t.Fatalf("got %d %s, want 500 error=internal error code=internal_error", c.Response.StatusCode(), c.Response.Body())
	}
	for _, leak := range []string{"disk", "queue.db", "hunter2", "/var/lib"} {
		if strings.Contains(string(c.Response.Body()), leak) {
			t.Errorf("response leaks %q: %s", leak, c.Response.Body())
		}
	}
}

func TestWebhook_RealInternalFailureLeaksNothing(t *testing.T) {
	adm, _, q, auth := realAdmitter(t, 10)
	_ = q.DB().Close() // every query now fails with a raw database error
	c := serve(t, adm, auth, `{"tag":"v1"}`)
	if c.Response.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", c.Response.StatusCode(), c.Response.Body())
	}
	if body := strings.ToLower(string(c.Response.Body())); strings.Contains(body, "sql") || strings.Contains(body, "database") || strings.Contains(body, "closed") {
		t.Errorf("response leaks the database error: %s", c.Response.Body())
	}
}

func TestWebhook_UnexpectedOutcomeIsGeneric500(t *testing.T) {
	c := serve(t, fakeAdmitter{admission.AdmissionResult{Outcome: admission.AdmissionOutcome("something-new")}}, "Bearer x", `{"tag":"v1"}`)
	b := decode(t, c)
	if c.Response.StatusCode() != 500 || b.Code != "internal_error" {
		t.Fatalf("got %d %s", c.Response.StatusCode(), c.Response.Body())
	}
}

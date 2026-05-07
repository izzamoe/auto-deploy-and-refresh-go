package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

func newTestAdminAPIHertz(t *testing.T) (*server.Hertz, *AppStore, *DeployQueue) {
	t.Helper()
	db := newTestDB(t)
	store, err := NewAppStore(db)
	if err != nil {
		t.Fatalf("NewAppStore: %v", err)
	}
	queue, err := NewDeployQueue(db, 10)
	if err != nil {
		t.Fatalf("NewDeployQueue: %v", err)
	}
	handler := NewAdminAPIHandler(store, queue, NewProgressTracker(), NewCancelService(queue))
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	RegisterAdminAPIRoutesHertz(h, handler, HertzBasicAuthMiddleware("admin", "secret"))
	return h, store, queue
}

func adminAPIRequestHertz(method, path, body string) *app.RequestContext {
	c := app.NewContext(0)
	c.Request.SetRequestURI(path)
	c.Request.SetMethod(method)
	c.Request.SetBodyString(body)
	if method != http.MethodGet || body != "" {
		c.Request.Header.Set("Content-Type", "application/json")
	}
	c.Request.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:secret")))
	return c
}

func serveAdminAPIHertz(t *testing.T, h *server.Hertz, c *app.RequestContext) *app.RequestContext {
	t.Helper()
	h.ServeHTTP(context.Background(), c)
	if contentType := string(c.Response.Header.Peek("Content-Type")); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("expected JSON Content-Type, got %q with body %s", contentType, string(c.Response.Body()))
	}
	return c
}

func decodeAdminAPIResponseHertz[T any](t *testing.T, c *app.RequestContext) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(c.Response.Body(), &out); err != nil {
		t.Fatalf("decode JSON response: %v; body=%s", err, string(c.Response.Body()))
	}
	return out
}

func TestAdminAPIUnauthorizedReturnsJSON(t *testing.T) {
	h, _, _ := newTestAdminAPIHertz(t)

	c := app.NewContext(0)
	c.Request.SetRequestURI("/admin/api/apps")
	c.Request.SetMethod("GET")

	h.ServeHTTP(context.Background(), c)

	if c.Response.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", c.Response.StatusCode())
	}
	if got := string(c.Response.Header.Peek("WWW-Authenticate")); got != `Basic realm="auto-deploy admin"` {
		t.Fatalf("expected WWW-Authenticate header, got %q", got)
	}
	if contentType := string(c.Response.Header.Peek("Content-Type")); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("expected JSON Content-Type, got %q", contentType)
	}
	body := decodeAdminAPIResponseHertz[response](t, c)
	if body.Status != "error" || body.Error != "unauthorized" {
		t.Fatalf("unexpected auth error body: %+v", body)
	}
}

func TestAdminAPIAppsCRUD(t *testing.T) {
	h, store, _ := newTestAdminAPIHertz(t)

	createBody := `{"name":"api-app","secret":"secret-1","binaryPath":"/opt/api-app","serviceName":"api-app.service","githubRepo":"owner/api-app","artifactName":"api-app-linux"}`
	createRR := serveAdminAPIHertz(t, h, adminAPIRequestHertz("POST", "/admin/api/apps", createBody))
	if createRR.Response.StatusCode() != http.StatusCreated {
		t.Fatalf("expected 201 create, got %d body=%s", createRR.Response.StatusCode(), string(createRR.Response.Body()))
	}
	created := decodeAdminAPIResponseHertz[struct {
		Status string              `json:"status"`
		App    adminAPIAppResponse `json:"app"`
	}](t, createRR)
	if created.Status != "created" || created.App.ID == "" || created.App.Name != "api-app" {
		t.Fatalf("unexpected create response: %+v", created)
	}

	listRR := serveAdminAPIHertz(t, h, adminAPIRequestHertz("GET", "/admin/api/apps", ""))
	if listRR.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200 list, got %d", listRR.Response.StatusCode())
	}
	listed := decodeAdminAPIResponseHertz[struct {
		Apps []adminAPIAppResponse `json:"apps"`
	}](t, listRR)
	if len(listed.Apps) != 1 || listed.Apps[0].ID != created.App.ID {
		t.Fatalf("unexpected app list: %+v", listed)
	}

	getRR := serveAdminAPIHertz(t, h, adminAPIRequestHertz("GET", "/admin/api/apps/"+created.App.ID, ""))
	if getRR.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200 get, got %d", getRR.Response.StatusCode())
	}
	got := decodeAdminAPIResponseHertz[struct {
		App adminAPIAppResponse `json:"app"`
	}](t, getRR)
	if got.App.BinaryPath != "/opt/api-app" {
		t.Fatalf("unexpected get response: %+v", got)
	}

	updateBody := `{"name":"api-app-updated","binaryPath":"/opt/api-app-v2","serviceName":"api-app-v2.service","githubRepo":"owner/api-app-v2","artifactName":"api-app-v2-linux","enabled":false}`
	updateRR := serveAdminAPIHertz(t, h, adminAPIRequestHertz("PUT", "/admin/api/apps/"+created.App.ID, updateBody))
	if updateRR.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200 update, got %d body=%s", updateRR.Response.StatusCode(), string(updateRR.Response.Body()))
	}
	updated := decodeAdminAPIResponseHertz[struct {
		Status string              `json:"status"`
		App    adminAPIAppResponse `json:"app"`
	}](t, updateRR)
	if updated.App.Name != "api-app-updated" || updated.App.Enabled {
		t.Fatalf("unexpected update response: %+v", updated)
	}

	toggleRR := serveAdminAPIHertz(t, h, adminAPIRequestHertz("POST", "/admin/api/apps/"+created.App.ID+"/toggle", ""))
	if toggleRR.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200 toggle, got %d", toggleRR.Response.StatusCode())
	}
	toggled := decodeAdminAPIResponseHertz[struct {
		App adminAPIAppResponse `json:"app"`
	}](t, toggleRR)
	if !toggled.App.Enabled {
		t.Fatalf("expected toggle to re-enable app, got %+v", toggled)
	}

	deleteRR := serveAdminAPIHertz(t, h, adminAPIRequestHertz("DELETE", "/admin/api/apps/"+created.App.ID, ""))
	if deleteRR.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200 delete, got %d body=%s", deleteRR.Response.StatusCode(), string(deleteRR.Response.Body()))
	}
	if _, err := store.Get(created.App.ID); err == nil {
		t.Fatal("expected app to be deleted")
	}

	missingDeleteRR := serveAdminAPIHertz(t, h, adminAPIRequestHertz("DELETE", "/admin/api/apps/"+created.App.ID, ""))
	if missingDeleteRR.Response.StatusCode() != http.StatusNotFound {
		t.Fatalf("expected 404 delete missing app, got %d body=%s", missingDeleteRR.Response.StatusCode(), string(missingDeleteRR.Response.Body()))
	}
}

func TestAdminAPIUpdateDuplicateSecretDoesNotPartiallyUpdate(t *testing.T) {
	h, store, _ := newTestAdminAPIHertz(t)
	appA, err := store.Create("app-a", "secret-a", "/bin/a", "a.service", "owner/a", "artifact-a")
	if err != nil {
		t.Fatalf("create app A: %v", err)
	}
	_, err = store.Create("app-b", "secret-b", "/bin/b", "b.service", "owner/b", "artifact-b")
	if err != nil {
		t.Fatalf("create app B: %v", err)
	}

	updateBody := `{"name":"app-a-mutated","secret":"secret-b","binaryPath":"/bin/a-mutated","serviceName":"a-mutated.service","githubRepo":"owner/a-mutated","artifactName":"artifact-a-mutated"}`
	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz("PUT", "/admin/api/apps/"+appA.ID, updateBody))
	if rr.Response.StatusCode() != http.StatusBadRequest {
		t.Fatalf("expected 400 duplicate secret update, got %d body=%s", rr.Response.StatusCode(), string(rr.Response.Body()))
	}

	reloaded, err := store.Get(appA.ID)
	if err != nil {
		t.Fatalf("reload app A: %v", err)
	}
	if reloaded.Name != appA.Name || reloaded.BinaryPath != appA.BinaryPath || reloaded.ServiceName != appA.ServiceName || reloaded.GithubRepo != appA.GithubRepo || reloaded.ArtifactName != appA.ArtifactName {
		t.Fatalf("expected duplicate secret failure to leave metadata unchanged, got %+v want %+v", reloaded, appA)
	}
}

func TestAdminAPIManualDeployQueuesJSON(t *testing.T) {
	h, store, queue := newTestAdminAPIHertz(t)
	app, err := store.Create("deploy-app", "secret", "/bin/deploy", "deploy.service", "owner/deploy", "artifact")
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz("POST", "/admin/api/apps/"+app.ID+"/deploy", `{"tag":"v1.2.3"}`))
	if rr.Response.StatusCode() != http.StatusAccepted {
		t.Fatalf("expected 202 deploy, got %d body=%s", rr.Response.StatusCode(), string(rr.Response.Body()))
	}
	queued := decodeAdminAPIResponseHertz[struct {
		Status string `json:"status"`
		Tag    string `json:"tag"`
	}](t, rr)
	if queued.Status != "queued" || queued.Tag != "v1.2.3" {
		t.Fatalf("unexpected deploy response: %+v", queued)
	}
	jobs, err := queue.ListHistory(app.ID, 10)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Trigger != "manual_deploy" || jobs[0].Status != JobStatusPending {
		t.Fatalf("expected one pending manual_deploy job, got %+v", jobs)
	}

	dupRR := serveAdminAPIHertz(t, h, adminAPIRequestHertz("POST", "/admin/api/apps/"+app.ID+"/deploy", `{"tag":"v1.2.3"}`))
	if dupRR.Response.StatusCode() != http.StatusConflict {
		t.Fatalf("expected 409 duplicate deploy, got %d body=%s", dupRR.Response.StatusCode(), string(dupRR.Response.Body()))
	}
	errBody := decodeAdminAPIResponseHertz[adminAPIErrorResponse](t, dupRR)
	if errBody.Error != "Deploy already queued for this tag" {
		t.Fatalf("unexpected duplicate error body: %+v", errBody)
	}
}

func TestAdminAPIHistoryListAndRetry(t *testing.T) {
	h, store, queue := newTestAdminAPIHertz(t)
	app, err := store.Create("history-app", "secret", "/bin/history", "history.service", "owner/history", "artifact")
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	if err := queue.Enqueue(app.ID, "v1.0.0"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	jobID, _, err := queue.DequeueNext(app.ID)
	if err != nil {
		t.Fatalf("DequeueNext: %v", err)
	}
	if err := queue.MarkDone(jobID, false, "failed", nil); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}

	listRR := serveAdminAPIHertz(t, h, adminAPIRequestHertz("GET", "/admin/api/history?appId="+app.ID, ""))
	if listRR.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200 history, got %d", listRR.Response.StatusCode())
	}
	listed := decodeAdminAPIResponseHertz[struct {
		App     adminAPIAppResponse   `json:"app"`
		History []adminAPIJobResponse `json:"history"`
	}](t, listRR)
	if listed.App.ID != app.ID || len(listed.History) != 1 || listed.History[0].ID != jobID {
		t.Fatalf("unexpected history response: %+v", listed)
	}

	retryRR := serveAdminAPIHertz(t, h, adminAPIRequestHertz("POST", "/admin/api/history/"+jobID+"/retry", ""))
	if retryRR.Response.StatusCode() != http.StatusAccepted {
		t.Fatalf("expected 202 retry, got %d body=%s", retryRR.Response.StatusCode(), string(retryRR.Response.Body()))
	}
	retry := decodeAdminAPIResponseHertz[struct {
		Status string `json:"status"`
		JobID  string `json:"jobId"`
	}](t, retryRR)
	if retry.Status != "queued" || retry.JobID == "" {
		t.Fatalf("unexpected retry response: %+v", retry)
	}
	count, err := queue.PendingCount(app.ID)
	if err != nil {
		t.Fatalf("PendingCount: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one pending retry job, got %d", count)
	}
}

func TestAdminAPICancelEndpoints(t *testing.T) {
	h, store, queue := newTestAdminAPIHertz(t)
	app, err := store.Create("cancel-app", "secret", "/bin/cancel", "cancel.service", "owner/cancel", "artifact")
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	if err := queue.Enqueue(app.ID, "v-pending"); err != nil {
		t.Fatalf("Enqueue pending: %v", err)
	}
	pendingJobs, err := queue.ListHistory(app.ID, 10)
	if err != nil || len(pendingJobs) != 1 {
		t.Fatalf("ListHistory pending: jobs=%+v err=%v", pendingJobs, err)
	}

	jobCancelRR := serveAdminAPIHertz(t, h, adminAPIRequestHertz("POST", "/admin/api/jobs/"+pendingJobs[0].ID+"/cancel", ""))
	if jobCancelRR.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200 job cancel, got %d body=%s", jobCancelRR.Response.StatusCode(), string(jobCancelRR.Response.Body()))
	}
	jobCancel := decodeAdminAPIResponseHertz[struct {
		Status string                    `json:"status"`
		Result adminAPICancelJobResponse `json:"result"`
	}](t, jobCancelRR)
	if jobCancel.Result.Status != JobStatusCanceled || jobCancel.Result.Outcome != CancelOutcomePendingCanceled {
		t.Fatalf("unexpected job cancel response: %+v", jobCancel)
	}
	var rawJobCancel map[string]any
	if err := json.Unmarshal(jobCancelRR.Response.Body(), &rawJobCancel); err != nil {
		t.Fatalf("decode raw job cancel: %v", err)
	}
	rawJobResult := rawJobCancel["result"].(map[string]any)
	if _, ok := rawJobResult["jobId"]; !ok {
		t.Fatalf("expected camelCase jobId in cancel response, got %+v", rawJobResult)
	}
	if _, ok := rawJobResult["JobID"]; ok {
		t.Fatalf("expected no Go-style JobID key in cancel response, got %+v", rawJobResult)
	}

	if err := queue.Enqueue(app.ID, "v-active"); err != nil {
		t.Fatalf("Enqueue active: %v", err)
	}
	activeID, _, err := queue.DequeueNext(app.ID)
	if err != nil {
		t.Fatalf("DequeueNext: %v", err)
	}
	appCancelRR := serveAdminAPIHertz(t, h, adminAPIRequestHertz("POST", "/admin/api/apps/"+app.ID+"/cancel", ""))
	if appCancelRR.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200 app cancel, got %d body=%s", appCancelRR.Response.StatusCode(), string(appCancelRR.Response.Body()))
	}
	appCancel := decodeAdminAPIResponseHertz[struct {
		Status string                    `json:"status"`
		Result adminAPICancelAppResponse `json:"result"`
	}](t, appCancelRR)
	if appCancel.Result.AppID != app.ID || appCancel.Result.Active != 1 {
		t.Fatalf("unexpected app cancel response: %+v", appCancel)
	}
	var status string
	if err := queue.db.QueryRow(`SELECT status FROM deploy_jobs WHERE id = ?`, activeID).Scan(&status); err != nil {
		t.Fatalf("query active status: %v", err)
	}
	if status != JobStatusCancelRequested {
		t.Fatalf("expected active job cancel_requested, got %q", status)
	}
}

func TestAdminAPIAppCancelReturnsCounts(t *testing.T) {
	h, store, queue := newTestAdminAPIHertz(t)
	app, err := store.Create("cancel-counts-app", "secret", "/bin/cancel-counts", "cancel-counts.service", "owner/cancel-counts", "artifact")
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	activeID := dequeueJob(t, queue, app.ID, "v-active")
	terminalID := dequeueJob(t, queue, app.ID, "v-terminal")
	if err := queue.MarkDone(terminalID, true, "", nil); err != nil {
		t.Fatalf("MarkDone terminal: %v", err)
	}
	pendingID := enqueueJob(t, queue, app.ID, "v-pending")
	otherAppID := enqueueJob(t, queue, "other-app", "v-other")

	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz("POST", "/admin/api/apps/"+app.ID+"/cancel", ""))
	if rr.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200 app cancel, got %d body=%s", rr.Response.StatusCode(), string(rr.Response.Body()))
	}
	decoded := decodeAdminAPIResponseHertz[struct {
		Status string                    `json:"status"`
		Result adminAPICancelAppResponse `json:"result"`
	}](t, rr)
	if decoded.Status != "ok" {
		t.Fatalf("status = %q, want ok", decoded.Status)
	}
	result := decoded.Result
	if result.AppID != app.ID || result.Total != 3 || result.PendingCanceled != 1 || result.ActiveSignaled != 1 || result.AlreadyTerminal != 1 {
		t.Fatalf("unexpected cancel counts: %+v", result)
	}
	if result.Pending != 1 || result.Active != 1 || result.Terminal != 1 || result.Unknown != 0 {
		t.Fatalf("unexpected legacy aggregate counts: %+v", result)
	}

	var raw map[string]any
	if err := json.Unmarshal(rr.Response.Body(), &raw); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}
	rawResult := raw["result"].(map[string]any)
	for key, want := range map[string]float64{"pendingCanceled": 1, "activeSignaled": 1, "alreadyTerminal": 1} {
		got, ok := rawResult[key].(float64)
		if !ok || got != want {
			t.Fatalf("raw result[%q] = %v (ok=%v), want %v in %s", key, rawResult[key], ok, want, string(rr.Response.Body()))
		}
	}

	assertJobStatus(t, queue, pendingID, JobStatusCanceled)
	assertJobStatus(t, queue, activeID, JobStatusCancelRequested)
	assertJobStatus(t, queue, terminalID, JobStatusSucceeded)
	assertJobStatus(t, queue, otherAppID, JobStatusPending)
}

func TestAdminAPIErrorBodiesAreJSON(t *testing.T) {
	h, _, _ := newTestAdminAPIHertz(t)

	invalidCreate := serveAdminAPIHertz(t, h, adminAPIRequestHertz("POST", "/admin/api/apps", `{"name":"missing-fields"}`))
	if invalidCreate.Response.StatusCode() != http.StatusBadRequest {
		t.Fatalf("expected 400 validation, got %d", invalidCreate.Response.StatusCode())
	}
	validation := decodeAdminAPIResponseHertz[adminAPIErrorResponse](t, invalidCreate)
	if validation.Status != "error" || validation.Error != "Validation failed" || len(validation.Errors) == 0 {
		t.Fatalf("unexpected validation body: %+v", validation)
	}

	badJSON := serveAdminAPIHertz(t, h, adminAPIRequestHertz("POST", "/admin/api/apps", `{`))
	if badJSON.Response.StatusCode() != http.StatusBadRequest {
		t.Fatalf("expected 400 bad JSON, got %d", badJSON.Response.StatusCode())
	}
	badBody := decodeAdminAPIResponseHertz[adminAPIErrorResponse](t, badJSON)
	if badBody.Error != "Invalid JSON body" {
		t.Fatalf("unexpected bad JSON body: %+v", badBody)
	}

	notFound := serveAdminAPIHertz(t, h, adminAPIRequestHertz("GET", "/admin/api/does-not-exist", ""))
	if notFound.Response.StatusCode() != http.StatusNotFound {
		t.Fatalf("expected 404 JSON not found, got %d", notFound.Response.StatusCode())
	}
	notFoundBody := decodeAdminAPIResponseHertz[adminAPIErrorResponse](t, notFound)
	if notFoundBody.Error != "Not found" {
		t.Fatalf("unexpected not found body: %+v", notFoundBody)
	}

	missingContentType := app.NewContext(0)
	missingContentType.Request.SetRequestURI("/admin/api/apps")
	missingContentType.Request.SetMethod("POST")
	missingContentType.Request.SetBodyString(`{"name":"x"}`)
	missingContentType.Request.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:secret")))
	missingContentType = serveAdminAPIHertz(t, h, missingContentType)
	if missingContentType.Response.StatusCode() != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415 for missing JSON Content-Type, got %d body=%s", missingContentType.Response.StatusCode(), string(missingContentType.Response.Body()))
	}
	missingContentTypeBody := decodeAdminAPIResponseHertz[adminAPIErrorResponse](t, missingContentType)
	if missingContentTypeBody.Error != "Content-Type must be application/json" {
		t.Fatalf("unexpected missing Content-Type body: %+v", missingContentTypeBody)
	}
}

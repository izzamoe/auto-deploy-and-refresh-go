package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func setupTestWebhook(t *testing.T) (http.HandlerFunc, string) {
	t.Helper()
	db := newTestDB(t)
	q, err := NewDeployQueue(db, 10)
	if err != nil {
		t.Fatalf("new deploy queue: %v", err)
	}
	store, err := NewAppStore(db)
	if err != nil {
		t.Fatalf("new app store: %v", err)
	}
	secret := "test-secret-1234"
	_, err = store.Create("testapp", secret, "/bin/test", "test.service", "owner/repo", "artifact")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	admission := NewAdmissionService(store, q)
	return multiAppWebhookHandler(admission), secret
}

func setupTestWebhookWithQueueMax(t *testing.T, maxPending int) (http.HandlerFunc, string) {
	t.Helper()
	db := newTestDB(t)
	q, err := NewDeployQueue(db, maxPending)
	if err != nil {
		t.Fatalf("new deploy queue: %v", err)
	}
	store, err := NewAppStore(db)
	if err != nil {
		t.Fatalf("new app store: %v", err)
	}
	secret := "test-secret-1234"
	_, err = store.Create("testapp", secret, "/bin/test", "test.service", "owner/repo", "artifact")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	admission := NewAdmissionService(store, q)
	return multiAppWebhookHandler(admission), secret
}

func decodeResponse(t *testing.T, rec *httptest.ResponseRecorder) response {
	t.Helper()
	var resp response
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	return resp
}

func newAuthRequestWithSecret(t *testing.T, tag, secret string) *http.Request {
	t.Helper()
	body := bytes.NewBufferString(`{"tag":"` + tag + `"}`)
	r := httptest.NewRequest(http.MethodPost, "/webhook", body)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+secret)
	return r
}

func TestWebhookValidRequestReturns202Queued(t *testing.T) {
	handler, secret := setupTestWebhook(t)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newAuthRequestWithSecret(t, "v1.2.0", secret))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	resp := decodeResponse(t, rec)
	if resp.Status != "queued" {
		t.Errorf("expected status=queued, got %q", resp.Status)
	}
	if resp.Tag != "v1.2.0" {
		t.Errorf("expected tag=v1.2.0, got %q", resp.Tag)
	}
}

func TestWebhookSecondRequestAlsoReturns202(t *testing.T) {
	handler, secret := setupTestWebhook(t)

	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, newAuthRequestWithSecret(t, "v1.2.0", secret))
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first request: expected 202, got %d", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, newAuthRequestWithSecret(t, "v1.3.0", secret))
	if rec2.Code != http.StatusAccepted {
		t.Fatalf("second request: expected 202, got %d", rec2.Code)
	}
	resp := decodeResponse(t, rec2)
	if resp.Status != "queued" {
		t.Errorf("expected status=queued, got %q", resp.Status)
	}
}

func TestWebhookDuplicateTagReturnsDuplicateStatus(t *testing.T) {
	handler, secret := setupTestWebhook(t)

	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, newAuthRequestWithSecret(t, "v1.2.0", secret))
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first request: expected 202, got %d", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, newAuthRequestWithSecret(t, "v1.2.0", secret))
	if rec2.Code != http.StatusOK {
		t.Fatalf("duplicate request: expected 200, got %d", rec2.Code)
	}
	resp := decodeResponse(t, rec2)
	if resp.Status != "duplicate" {
		t.Errorf("expected status=duplicate, got %q", resp.Status)
	}
	if resp.Tag != "v1.2.0" {
		t.Errorf("expected tag=v1.2.0, got %q", resp.Tag)
	}
}

func TestWebhookQueueFullReturns503(t *testing.T) {
	handler, secret := setupTestWebhookWithQueueMax(t, 1)

	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, newAuthRequestWithSecret(t, "v1.0.0", secret))
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first request: expected 202, got %d", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, newAuthRequestWithSecret(t, "v2.0.0", secret))
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec2.Code)
	}
	resp := decodeResponse(t, rec2)
	if resp.Status != "error" {
		t.Errorf("expected status=error, got %q", resp.Status)
	}
	if resp.Error != "queue full" {
		t.Errorf("expected error=queue full, got %q", resp.Error)
	}
}

func TestWebhookUnauthorizedReturns401(t *testing.T) {
	handler, _ := setupTestWebhook(t)

	cases := []struct {
		name   string
		header string
	}{
		{"no auth header", ""},
		{"wrong bearer token", "Bearer wrong-secret"},
		{"missing bearer prefix", "some-secret"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := bytes.NewBufferString(`{"tag":"v1.0.0"}`)
			r := httptest.NewRequest(http.MethodPost, "/webhook", body)
			r.Header.Set("Content-Type", "application/json")
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rec.Code)
			}
			resp := decodeResponse(t, rec)
			if resp.Status != "error" {
				t.Errorf("expected status=error, got %q", resp.Status)
			}
			if resp.Error != "unauthorized" {
				t.Errorf("expected error=unauthorized, got %q", resp.Error)
			}
		})
	}
}

func TestWebhookInvalidPayloadReturns400(t *testing.T) {
	handler, secret := setupTestWebhook(t)

	cases := []struct {
		name string
		body string
	}{
		{"empty body", ``},
		{"malformed json", `{not-json}`},
		{"empty tag field", `{"tag":""}`},
		{"missing tag field", `{"other":"value"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+secret)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", rec.Code)
			}
			resp := decodeResponse(t, rec)
			if resp.Status != "error" {
				t.Errorf("expected status=error, got %q", resp.Status)
			}
			if resp.Error != "missing or empty tag" {
				t.Errorf("expected error=missing or empty tag, got %q", resp.Error)
			}
		})
	}
}

func TestLoadConfigQueueSettings(t *testing.T) {
	n, path, err := parseQueueConfig("10", "deploy-queue.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 10 {
		t.Errorf("expected QueueMax=10, got %d", n)
	}
	if path != "deploy-queue.db" {
		t.Errorf("expected QueueDBPath=deploy-queue.db, got %s", path)
	}

	n2, path2, err2 := parseQueueConfig("5", "/tmp/custom.db")
	if err2 != nil {
		t.Fatalf("unexpected error: %v", err2)
	}
	if n2 != 5 {
		t.Errorf("expected QueueMax=5, got %d", n2)
	}
	if path2 != "/tmp/custom.db" {
		t.Errorf("expected QueueDBPath=/tmp/custom.db, got %s", path2)
	}
}

func TestLoadConfigRejectsInvalidQueueMax(t *testing.T) {
	cases := []string{"abc", "0", "-1", ""}
	for _, tc := range cases {
		_, _, err := parseQueueConfig(tc, "deploy-queue.db")
		if err == nil {
			t.Errorf("expected error for DEPLOY_QUEUE_MAX=%q, got nil", tc)
		}
	}
}

func TestWebhookMethodNotAllowedReturns405(t *testing.T) {
	handler, secret := setupTestWebhook(t)

	methods := []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			r := httptest.NewRequest(method, "/webhook", nil)
			r.Header.Set("Authorization", "Bearer "+secret)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected 405, got %d", rec.Code)
			}
			resp := decodeResponse(t, rec)
			if resp.Status != "error" {
				t.Errorf("expected status=error, got %q", resp.Status)
			}
			if resp.Error != "method not allowed" {
				t.Errorf("expected error=method not allowed, got %q", resp.Error)
			}
		})
	}
}

package download

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/client"
)

// TestDownloadSendsHeaders verifies caller-supplied headers reach the origin.
func TestDownloadSendsHeaders(t *testing.T) {
	t.Parallel()
	var gotAuth, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("binary-data"))
	}))
	defer srv.Close()

	c, _ := client.NewClient(client.WithResponseBodyStream(true))
	headers := map[string]string{"Authorization": "Bearer tok", "Accept": "application/octet-stream"}
	resp, err := DownloadWithRetryContext(context.Background(), c, srv.URL, headers, noSleep)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want Bearer tok", gotAuth)
	}
	if gotAccept != "application/octet-stream" {
		t.Errorf("Accept = %q, want application/octet-stream", gotAccept)
	}
}

// TestDownloadStripsAuthOnCrossHostRedirect verifies the Authorization header
// is NOT forwarded when the download is redirected to a different host (as
// GitHub does when redirecting asset downloads to signed storage URLs), while
// non-sensitive headers still flow.
func TestDownloadStripsAuthOnCrossHostRedirect(t *testing.T) {
	t.Parallel()

	var storageAuth, storageAccept string
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		storageAuth = r.Header.Get("Authorization")
		storageAccept = r.Header.Get("Accept")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("binary-data"))
	}))
	defer storage.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("api Authorization = %q, want Bearer tok", r.Header.Get("Authorization"))
		}
		http.Redirect(w, r, storage.URL+"/signed", http.StatusFound)
	}))
	defer api.Close()

	c, _ := client.NewClient(client.WithResponseBodyStream(true))
	headers := map[string]string{"Authorization": "Bearer tok", "Accept": "application/octet-stream"}
	resp, err := DownloadWithRetryContext(context.Background(), c, api.URL+"/asset", headers, noSleep)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if storageAuth != "" {
		t.Errorf("storage host received Authorization = %q, want it stripped", storageAuth)
	}
	if storageAccept != "application/octet-stream" {
		t.Errorf("storage Accept = %q, want application/octet-stream (non-sensitive headers should flow)", storageAccept)
	}
}

package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// withTestServer points apiBaseURL at server for the duration of the test
// and restores it afterwards (apiBaseURL is a package-level var precisely
// so tests can do this).
func withTestServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	original := apiBaseURL
	apiBaseURL = server.URL
	t.Cleanup(func() { apiBaseURL = original })
}

// Note: these tests deliberately do not call t.Parallel() — they swap the
// shared package-level apiBaseURL var (see withTestServer), which would race
// if run concurrently. This mirrors the runSystemctl-stubbing convention in
// internal/deploy/main_test.go.

func TestListReleasesParsesSuccessfulResponse(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/releases" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept header = %q, want application/vnd.github+json", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("X-GitHub-Api-Version header = %q, want 2022-11-28", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"tag_name": "v1.0.0",
				"assets": []map[string]string{
					{"name": "app-linux-amd64"},
					{"name": "checksums.txt"},
				},
			},
			{
				"tag_name": "v0.9.0",
				"assets": []map[string]string{
					{"name": "other-artifact"},
				},
			},
		})
	})

	c := NewClient("")
	releases, err := c.ListReleases(context.Background(), "owner", "repo")
	if err != nil {
		t.Fatalf("ListReleases: %v", err)
	}
	if len(releases) != 2 {
		t.Fatalf("expected 2 releases, got %d: %+v", len(releases), releases)
	}
	if releases[0].TagName != "v1.0.0" {
		t.Errorf("releases[0].TagName = %q, want v1.0.0", releases[0].TagName)
	}
	if len(releases[0].Assets) != 2 || releases[0].Assets[0] != "app-linux-amd64" {
		t.Errorf("releases[0].Assets = %v, want [app-linux-amd64 checksums.txt]", releases[0].Assets)
	}
	if releases[1].TagName != "v0.9.0" {
		t.Errorf("releases[1].TagName = %q, want v0.9.0", releases[1].TagName)
	}
}

func TestListReleasesSetsAuthHeaderOnlyWhenTokenPresent(t *testing.T) {
	var gotAuthHeader string
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	})

	c := NewClient("")
	if _, err := c.ListReleases(context.Background(), "owner", "repo"); err != nil {
		t.Fatalf("ListReleases (no token): %v", err)
	}
	if gotAuthHeader != "" {
		t.Errorf("expected no Authorization header when token is empty, got %q", gotAuthHeader)
	}

	cWithToken := NewClient("s3cr3t-token")
	if _, err := cWithToken.ListReleases(context.Background(), "owner", "repo"); err != nil {
		t.Fatalf("ListReleases (with token): %v", err)
	}
	if gotAuthHeader != "Bearer s3cr3t-token" {
		t.Errorf("Authorization header = %q, want %q", gotAuthHeader, "Bearer s3cr3t-token")
	}
}

func TestListReleasesNonSuccessStatusReturnsError(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	})

	c := NewClient("super-secret-token")
	_, err := c.ListReleases(context.Background(), "owner", "repo")
	if err == nil {
		t.Fatal("expected an error for a non-2xx status, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("expected error to mention status code 403, got: %v", err)
	}
	if strings.Contains(err.Error(), "super-secret-token") {
		t.Errorf("error must not leak the token, got: %v", err)
	}
}

func TestFilterReleasesWithAsset(t *testing.T) {
	t.Parallel()

	releases := []Release{
		{TagName: "v1.2.0", Assets: []string{"myapp-linux-amd64", "checksums.txt"}},
		{TagName: "v1.1.0", Assets: []string{"unrelated-file"}},
		{TagName: "v1.0.0", Assets: []string{"myapp-linux-amd64"}},
	}

	filtered := FilterReleasesWithAsset(releases, "myapp-linux-amd64")
	if len(filtered) != 2 {
		t.Fatalf("expected 2 matching releases, got %d: %+v", len(filtered), filtered)
	}
	if filtered[0].TagName != "v1.2.0" || filtered[1].TagName != "v1.0.0" {
		t.Errorf("unexpected filtered tags: %+v", filtered)
	}

	if none := FilterReleasesWithAsset(releases, "does-not-exist"); len(none) != 0 {
		t.Errorf("expected no matches for a nonexistent asset, got %+v", none)
	}
}

// TestNewClientSetsRequestTimeout guards the fix for the Cloudflare 502: the
// HTTP client MUST have a non-zero timeout so a host that cannot reach GitHub
// fails fast with the handler's own error instead of hanging until the reverse
// proxy returns its own 502.
func TestNewClientSetsRequestTimeout(t *testing.T) {
	t.Parallel()

	if got := NewClient("").httpClient.Timeout; got != requestTimeout {
		t.Fatalf("client timeout = %v, want %v", got, requestTimeout)
	}
}

// TestListReleasesTimesOutOnSlowServer verifies ListReleases returns an error
// (rather than blocking forever) when the upstream never responds within the
// client timeout — the exact condition that produced the hung origin.
func TestListReleasesTimesOutOnSlowServer(t *testing.T) {
	// close(block) via defer runs when this function returns, i.e. BEFORE the
	// t.Cleanup(server.Close) that withTestServer registers. Closing it in a
	// t.Cleanup would run after server.Close (cleanups are LIFO), so Close
	// would deadlock waiting on the still-blocked handler goroutine.
	block := make(chan struct{})
	defer close(block)
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-block // never respond within the client timeout
	})

	// Use a tiny timeout so the test is fast; NewClient's real value is
	// asserted separately in TestNewClientSetsRequestTimeout.
	c := &Client{httpClient: &http.Client{Timeout: 50 * time.Millisecond}}

	if _, err := c.ListReleases(context.Background(), "owner", "repo"); err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
}

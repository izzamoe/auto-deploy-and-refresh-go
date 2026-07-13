package github

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// Note: like the other tests in this package, these do not call t.Parallel()
// because withTestServer swaps the shared package-level apiBaseURL.

func TestArtifactDownloadNoTokenUsesPublicURL(t *testing.T) {
	c := NewClient("") // no token
	url, headers, err := c.ArtifactDownload(context.Background(), "owner/repo", "v1.2.3", "app-linux-amd64")
	if err != nil {
		t.Fatalf("ArtifactDownload: %v", err)
	}
	want := "https://github.com/owner/repo/releases/download/v1.2.3/app-linux-amd64"
	if url != want {
		t.Errorf("url = %q, want %q", url, want)
	}
	if headers != nil {
		t.Errorf("headers = %v, want nil for unauthenticated download", headers)
	}
}

func TestArtifactDownloadWithTokenResolvesAuthenticatedAssetURL(t *testing.T) {
	const assetURL = "https://api.github.com/repos/owner/repo/releases/assets/42"
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/releases/tags/v1.2.3" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("Authorization = %q, want Bearer secret-token", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v1.2.3",
			"assets": []map[string]any{
				{"name": "other-artifact", "id": 41, "url": "https://api.github.com/x/41"},
				{"name": "app-linux-amd64", "id": 42, "url": assetURL},
			},
		})
	})

	c := NewClient("secret-token")
	url, headers, err := c.ArtifactDownload(context.Background(), "owner/repo", "v1.2.3", "app-linux-amd64")
	if err != nil {
		t.Fatalf("ArtifactDownload: %v", err)
	}
	if url != assetURL {
		t.Errorf("url = %q, want %q", url, assetURL)
	}
	if headers["Authorization"] != "Bearer secret-token" {
		t.Errorf("Authorization header = %q, want Bearer secret-token", headers["Authorization"])
	}
	if headers["Accept"] != "application/octet-stream" {
		t.Errorf("Accept header = %q, want application/octet-stream", headers["Accept"])
	}
}

func TestArtifactDownloadWithTokenMissingAssetErrors(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v1.2.3",
			"assets": []map[string]any{
				{"name": "different-artifact", "id": 7, "url": "https://api.github.com/x/7"},
			},
		})
	})

	c := NewClient("secret-token")
	_, _, err := c.ArtifactDownload(context.Background(), "owner/repo", "v1.2.3", "app-linux-amd64")
	if err == nil {
		t.Fatal("expected error when the named asset is absent, got nil")
	}
}

func TestArtifactDownloadInvalidRepoErrors(t *testing.T) {
	c := NewClient("secret-token")
	_, _, err := c.ArtifactDownload(context.Background(), "no-slash", "v1.2.3", "app")
	if err == nil {
		t.Fatal("expected error for repo without owner/name, got nil")
	}
}

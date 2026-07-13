// Package github provides a minimal client for the GitHub Releases REST API,
// used to let operators pick a real release tag instead of typing one by hand.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"
)

// requestTimeout bounds a single GitHub API call end to end (connect, TLS,
// response, body read). Without it a host that cannot reach api.github.com —
// blocked egress, DNS blackhole — makes http.Client.Do hang indefinitely, so
// the releases request never returns and the reverse proxy in front (e.g.
// Cloudflare) answers with its own 502 instead of the handler's graceful
// error. Fail fast so the app returns its JSON error and the UI can fall back.
const requestTimeout = 10 * time.Second

// apiBaseURL is the GitHub REST API base URL. It is an unexported
// package-level var so tests can point it at an httptest.Server.
var apiBaseURL = "https://api.github.com"

// Release is a reduced view of a GitHub release: only the fields callers need.
type Release struct {
	TagName string
	// Assets holds the asset file names attached to the release (no other
	// asset metadata is needed by callers).
	Assets []string
}

// Client is a minimal GitHub REST API client for listing releases. Its token
// is guarded by a mutex so it can be updated at runtime (e.g. from the admin
// UI) while concurrent requests are in flight.
type Client struct {
	httpClient *http.Client

	mu    sync.RWMutex
	token string
}

// NewClient creates a Client. token may be empty, meaning requests are made
// unauthenticated (subject to GitHub's lower rate limit for anonymous
// callers) — that is fine and expected for public repos.
func NewClient(token string) *Client {
	return &Client{httpClient: &http.Client{Timeout: requestTimeout}, token: token}
}

// SetToken replaces the token used to authenticate subsequent requests. An
// empty token switches back to unauthenticated calls. Safe for concurrent use.
func (c *Client) SetToken(token string) {
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
}

// currentToken returns the token under a read lock.
func (c *Client) currentToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

type releaseAssetJSON struct {
	Name string `json:"name"`
}

type releaseJSON struct {
	TagName string             `json:"tag_name"`
	Assets  []releaseAssetJSON `json:"assets"`
}

// ListReleases fetches releases for owner/repo from the GitHub Releases API.
//
// Limitation: pagination is not implemented (v1). GitHub returns the most
// recent releases first by default, which is sufficient for tag selection.
func (c *Client) ListReleases(ctx context.Context, owner, repo string) ([]Release, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases", apiBaseURL, owner, repo)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("github: build releases request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token := c.currentToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: releases request for %s/%s failed: %w", owner, repo, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("github: releases request for %s/%s returned status %d", owner, repo, resp.StatusCode)
	}

	var raw []releaseJSON
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("github: decode releases response for %s/%s: %w", owner, repo, err)
	}

	releases := make([]Release, 0, len(raw))
	for _, r := range raw {
		assets := make([]string, 0, len(r.Assets))
		for _, a := range r.Assets {
			assets = append(assets, a.Name)
		}
		releases = append(releases, Release{TagName: r.TagName, Assets: assets})
	}
	return releases, nil
}

// FilterReleasesWithAsset returns only the releases whose Assets contain
// assetName (exact match).
func FilterReleasesWithAsset(releases []Release, assetName string) []Release {
	filtered := make([]Release, 0, len(releases))
	for _, r := range releases {
		if slices.Contains(r.Assets, assetName) {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

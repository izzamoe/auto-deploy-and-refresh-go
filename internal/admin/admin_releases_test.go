package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/github"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/route"
)

type fakeReleaseLister struct {
	releases []github.Release
	err      error
}

func (f *fakeReleaseLister) ListReleases(ctx context.Context, owner, repo string) ([]github.Release, error) {
	return f.releases, f.err
}

func newTestReleasesEngine(t *testing.T, handler *ReleasesHandler) *route.Engine {
	t.Helper()
	h := server.New(server.WithHostPorts(":0"), server.WithDisablePrintRoute(true))
	RegisterReleasesRoutesHertz(h, handler, func(ctx context.Context, c *app.RequestContext) { c.Next(ctx) })
	return h.Engine
}

func decodeReleasesResponse(t *testing.T, body []byte) []string {
	t.Helper()
	var parsed struct {
		Releases []string `json:"releases"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal releases response: %v (body=%s)", err, body)
	}
	return parsed.Releases
}

func TestListAppReleasesHertzFiltersToMatchingArtifact(t *testing.T) {
	t.Parallel()
	appStore := newTestAppStore(t)
	createdApp, err := appStore.Create("Test", "sec", "/bin", "svc", "owner/repo", "app-linux-amd64")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	lister := &fakeReleaseLister{releases: []github.Release{
		{TagName: "v1.0.0", Assets: []string{"app-linux-amd64", "checksums.txt"}},
		{TagName: "v0.9.0", Assets: []string{"other-artifact"}},
		{TagName: "v0.8.0", Assets: []string{"app-linux-amd64"}},
	}}
	handler := NewReleasesHandler(appStore, lister)
	engine := newTestReleasesEngine(t, handler)

	w := ut.PerformRequest(engine, "GET", "/admin/api/apps/"+createdApp.ID+"/releases", nil)
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200, got %d. body=%s", resp.StatusCode(), resp.Body())
	}

	releases := decodeReleasesResponse(t, resp.Body())
	if len(releases) != 2 || releases[0] != "v1.0.0" || releases[1] != "v0.8.0" {
		t.Fatalf("expected [v1.0.0 v0.8.0], got %v", releases)
	}
}

func TestListAppReleasesHertzAppNotFound(t *testing.T) {
	t.Parallel()
	appStore := newTestAppStore(t)
	handler := NewReleasesHandler(appStore, &fakeReleaseLister{})
	engine := newTestReleasesEngine(t, handler)

	w := ut.PerformRequest(engine, "GET", "/admin/api/apps/does-not-exist/releases", nil)
	resp := w.Result()

	if resp.StatusCode() != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode())
	}
}

func TestListAppReleasesHertzGitHubErrorReturns502WithoutLeakingDetails(t *testing.T) {
	t.Parallel()
	appStore := newTestAppStore(t)
	createdApp, err := appStore.Create("Test", "sec", "/bin", "svc", "owner/repo", "art")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	lister := &fakeReleaseLister{err: errors.New("github: releases request failed: token ghp_super_secret_value rejected")}
	handler := NewReleasesHandler(appStore, lister)
	engine := newTestReleasesEngine(t, handler)

	w := ut.PerformRequest(engine, "GET", "/admin/api/apps/"+createdApp.ID+"/releases", nil)
	resp := w.Result()

	if resp.StatusCode() != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.StatusCode())
	}
	body := string(resp.Body())
	if strings.Contains(body, "ghp_super_secret_value") {
		t.Fatalf("expected error body to not leak internal error details/token, got %s", body)
	}
}

func TestListAppReleasesHertzSurfacesGitHubStatusHint(t *testing.T) {
	t.Parallel()
	appStore := newTestAppStore(t)
	createdApp, err := appStore.Create("Test", "sec", "/bin", "svc", "owner/private-repo", "art")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A private repo the token cannot see returns 404 from GitHub; the handler
	// must surface that so the operator knows to widen the token's scope.
	lister := &fakeReleaseLister{err: &github.StatusError{StatusCode: 404, Op: "list releases for owner/private-repo"}}
	handler := NewReleasesHandler(appStore, lister)
	engine := newTestReleasesEngine(t, handler)

	w := ut.PerformRequest(engine, "GET", "/admin/api/apps/"+createdApp.ID+"/releases", nil)
	resp := w.Result()

	if resp.StatusCode() != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.StatusCode())
	}
	body := string(resp.Body())
	if !strings.Contains(body, "404") || !strings.Contains(body, "scope") {
		t.Fatalf("expected body to surface the 404 status and a scope hint, got %s", body)
	}
}

func TestListAppReleasesHertzNoMatchingReleasesReturnsEmptyArray(t *testing.T) {
	t.Parallel()
	appStore := newTestAppStore(t)
	createdApp, err := appStore.Create("Test", "sec", "/bin", "svc", "owner/repo", "does-not-exist-asset")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	lister := &fakeReleaseLister{releases: []github.Release{
		{TagName: "v1.0.0", Assets: []string{"other-asset"}},
	}}
	handler := NewReleasesHandler(appStore, lister)
	engine := newTestReleasesEngine(t, handler)

	w := ut.PerformRequest(engine, "GET", "/admin/api/apps/"+createdApp.ID+"/releases", nil)
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode())
	}
	releases := decodeReleasesResponse(t, resp.Body())
	if len(releases) != 0 {
		t.Fatalf("expected no matching releases, got %v", releases)
	}
}

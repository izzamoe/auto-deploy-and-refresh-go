package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/izzamoe/auto-deploy/internal/github"
	"github.com/izzamoe/auto-deploy/internal/store"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// releasesErrorMessage turns a ListReleases failure into a safe, actionable
// message for the admin UI. It surfaces GitHub's HTTP status code (which never
// contains the token) so a 401/403/404 — typically a token that lacks access
// to a private repo — is distinguishable from other failures. It never echoes
// err.Error() for non-status errors, which could carry request details.
func releasesErrorMessage(err error) string {
	var statusErr *github.StatusError
	if !errors.As(err, &statusErr) {
		return "Failed to fetch releases from GitHub"
	}
	switch statusErr.StatusCode {
	case 401:
		return "GitHub returned 401 (unauthorized): the configured token is missing or invalid."
	case 403:
		return "GitHub returned 403 (forbidden): the token is rate-limited or lacks the required scope."
	case 404:
		return "GitHub returned 404: the repo was not found, or it is private and the token cannot access it. " +
			"Give the token repo access — classic PAT needs the 'repo' scope, fine-grained PAT needs Contents: Read on this repo."
	default:
		return fmt.Sprintf("GitHub returned status %d while fetching releases.", statusErr.StatusCode)
	}
}

// releaseLister is the minimal surface ReleasesHandler needs from a GitHub
// releases client. Defined here (rather than depending on *github.Client
// directly) so tests can inject a fake without hitting the network.
type releaseLister interface {
	ListReleases(ctx context.Context, owner, repo string) ([]github.Release, error)
}

// ReleasesHandler serves the list of GitHub release tags available for an
// app's configured repo, filtered to releases that actually contain the
// app's expected artifact.
type ReleasesHandler struct {
	store  *store.AppStore
	lister releaseLister
}

func NewReleasesHandler(store *store.AppStore, lister releaseLister) *ReleasesHandler {
	return &ReleasesHandler{store: store, lister: lister}
}

// ListAppReleasesHertz handles GET /admin/api/apps/:id/releases.
func (h *ReleasesHandler) ListAppReleasesHertz(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	appRecord, err := h.store.Get(id)
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusNotFound, "App not found")
		return
	}

	owner, repo, ok := strings.Cut(appRecord.GithubRepo, "/")
	if !ok {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "App has an invalid GitHub repo configuration")
		return
	}

	releases, err := h.lister.ListReleases(ctx, owner, repo)
	if err != nil {
		// Deliberately do not include err.Error() in the response: it may
		// echo request details, and must never leak the GitHub token. We do
		// surface GitHub's HTTP status code (safe — it never contains the
		// token) plus an actionable hint, so operators can tell a token /
		// scope problem apart from a plain "no matching releases" result.
		writeAdminAPIErrorHertz(c, consts.StatusBadGateway, releasesErrorMessage(err))
		return
	}

	matching := github.FilterReleasesWithAsset(releases, appRecord.ArtifactName)
	tags := make([]string, 0, len(matching))
	for _, r := range matching {
		tags = append(tags, r.TagName)
	}

	c.JSON(consts.StatusOK, map[string]any{"releases": tags})
}

// RegisterReleasesRoutesHertz registers GET /admin/api/apps/:id/releases
// under the given auth middleware.
func RegisterReleasesRoutesHertz(h *server.Hertz, handler *ReleasesHandler, auth app.HandlerFunc) {
	h.GET("/admin/api/apps/:id/releases", auth, handler.ListAppReleasesHertz)
}

package admin

import (
	"context"
	"strings"

	"github.com/izzamoe/auto-deploy/internal/github"
	"github.com/izzamoe/auto-deploy/internal/store"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

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
		// echo request details, and must never leak the GitHub token.
		writeAdminAPIErrorHertz(c, consts.StatusBadGateway, "Failed to fetch releases from GitHub")
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

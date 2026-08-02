package admin

import (
	"context"

	"github.com/izzamoe/auto-deploy/internal/store"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// AppArgsHandler serves the admin API for reading/writing an app's
// command-line arguments. They are appended to ExecStart after the binary path
// in the systemd unit generated for the app, so a deployed binary can be
// configured with flags (--port 8080) and not only through the environment.
type AppArgsHandler struct {
	apps *store.AppStore
	args *store.AppArgsStore
}

func NewAppArgsHandler(apps *store.AppStore, args *store.AppArgsStore) *AppArgsHandler {
	return &AppArgsHandler{apps: apps, args: args}
}

type appArgsRequest struct {
	Args []string `json:"args"`
}

// argsToJSON normalises a nil slice to an empty array so the SPA always sees
// "args": [] rather than "args": null.
func argsToJSON(args []string) []string {
	if args == nil {
		return []string{}
	}
	return args
}

// GetAppArgsHertz returns the app's command-line arguments.
func (h *AppArgsHandler) GetAppArgsHertz(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	if _, err := h.apps.Get(id); err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusNotFound, "App not found")
		return
	}
	args, err := h.args.Get(id)
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to load command-line arguments")
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"args": argsToJSON(args)})
}

// SaveAppArgsHertz replaces the app's command-line arguments. Each argument is
// validated; an empty list clears them.
func (h *AppArgsHandler) SaveAppArgsHertz(ctx context.Context, c *app.RequestContext) {
	if !requireAdminAPIJSONRequestHertz(c) {
		return
	}
	id := c.Param("id")
	if _, err := h.apps.Get(id); err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusNotFound, "App not found")
		return
	}

	var body appArgsRequest
	if !decodeAdminAPIJSONHertz(c, &body) {
		return
	}

	// Validate every argument up front so the operator sees all the problems in
	// one response instead of fixing them one save at a time.
	var errs []string
	for _, a := range body.Args {
		if err := store.ValidateServiceArg(a); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		writeAdminAPIValidationErrorHertz(c, errs)
		return
	}

	args := argsToJSON(body.Args)
	if err := h.args.Set(id, args); err != nil {
		writeAdminAPIValidationErrorHertz(c, []string{err.Error()})
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"status": "updated", "args": args})
}

// RegisterAppArgsRoutesHertz registers the per-app argument routes. Like the
// env routes it must be registered before RegisterAdminAPIRoutesHertz, whose
// /admin/api/*path catch-all would otherwise shadow them.
func RegisterAppArgsRoutesHertz(h *server.Hertz, handler *AppArgsHandler, auth app.HandlerFunc) {
	api := h.Group("/admin/api", auth)
	api.GET("/apps/:id/args", handler.GetAppArgsHertz)
	api.PUT("/apps/:id/args", handler.SaveAppArgsHertz)
}

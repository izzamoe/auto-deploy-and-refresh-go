package admin

import (
	"context"

	"github.com/izzamoe/auto-deploy/internal/store"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// AppEnvHandler serves the admin API for reading/writing an app's per-service
// environment variables. These are injected into the systemd unit generated
// for the app (as Environment= lines), so a deployed binary can read secrets
// like BOT_TOKEN from its environment.
type AppEnvHandler struct {
	apps *store.AppStore
	env  *store.AppEnvStore
}

func NewAppEnvHandler(apps *store.AppStore, env *store.AppEnvStore) *AppEnvHandler {
	return &AppEnvHandler{apps: apps, env: env}
}

type envVarJSON struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func envVarsToJSON(vars []store.EnvVar) []envVarJSON {
	out := make([]envVarJSON, 0, len(vars))
	for _, v := range vars {
		out = append(out, envVarJSON{Name: v.Name, Value: v.Value})
	}
	return out
}

type appEnvRequest struct {
	EnvVars []envVarJSON `json:"envVars"`
}

// GetAppEnvHertz returns the app's environment variables.
func (h *AppEnvHandler) GetAppEnvHertz(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	if _, err := h.apps.Get(id); err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusNotFound, "App not found")
		return
	}
	vars, err := h.env.Get(id)
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to load environment variables")
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"envVars": envVarsToJSON(vars)})
}

// SaveAppEnvHertz replaces the app's environment variables. Names are
// validated; an empty list clears them.
func (h *AppEnvHandler) SaveAppEnvHertz(ctx context.Context, c *app.RequestContext) {
	if !requireAdminAPIJSONRequestHertz(c) {
		return
	}
	id := c.Param("id")
	if _, err := h.apps.Get(id); err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusNotFound, "App not found")
		return
	}

	var body appEnvRequest
	if !decodeAdminAPIJSONHertz(c, &body) {
		return
	}

	vars := make([]store.EnvVar, 0, len(body.EnvVars))
	var errs []string
	seen := make(map[string]bool)
	for _, v := range body.EnvVars {
		if v.Name == "" {
			continue // skip blank rows the UI may submit
		}
		if err := store.ValidateEnvVarName(v.Name); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if seen[v.Name] {
			errs = append(errs, "duplicate env var name: "+v.Name)
			continue
		}
		seen[v.Name] = true
		vars = append(vars, store.EnvVar{Name: v.Name, Value: v.Value})
	}
	if len(errs) > 0 {
		writeAdminAPIValidationErrorHertz(c, errs)
		return
	}

	if err := h.env.Set(id, vars); err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to save environment variables")
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"status": "updated", "envVars": envVarsToJSON(vars)})
}

// RegisterAppEnvRoutesHertz registers the per-app env var routes. Must be
// registered before RegisterAdminAPIRoutesHertz, whose /admin/api/*path
// catch-all would otherwise shadow these specific routes.
func RegisterAppEnvRoutesHertz(h *server.Hertz, handler *AppEnvHandler, auth app.HandlerFunc) {
	api := h.Group("/admin/api", auth)
	api.GET("/apps/:id/env", handler.GetAppEnvHertz)
	api.PUT("/apps/:id/env", handler.SaveAppEnvHertz)
}

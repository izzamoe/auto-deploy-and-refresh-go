package admin

import (
	"context"
	"embed"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

//go:embed all:web/admin/dist
var adminSPA embed.FS

func getAdminSPA() http.FileSystem {
	f, err := fs.Sub(adminSPA, "web/admin/dist")
	if err != nil {
		slog.Error("failed to load admin SPA assets", "err", err)
		return nil
	}
	return http.FS(f)
}

type adminSPAHandler struct {
	fs http.FileSystem
}

func RegisterAdminSPARoutesHertz(h *server.Hertz, auth app.HandlerFunc) {
	handler := &adminSPAHandler{fs: getAdminSPA()}
	h.GET("/admin/assets/*filepath", auth, handler.serveAssetHertz)
	h.GET("/admin", auth, handler.serveIndexHertz)
	h.GET("/admin/*path", auth, handler.serveIndexHertz)
}

func (h *adminSPAHandler) serveAssetHertz(ctx context.Context, c *app.RequestContext) {
	if h.fs == nil {
		c.String(http.StatusInternalServerError, "admin SPA unavailable")
		return
	}

	assetPath := strings.TrimPrefix(string(c.Request.URI().Path()), "/admin/")
	if assetPath == string(c.Request.URI().Path()) || assetPath == "assets" || !strings.HasPrefix(assetPath, "assets/") {
		c.SetStatusCode(http.StatusNotFound)
		return
	}

	file, err := h.fs.Open(assetPath)
	if err != nil {
		c.SetStatusCode(http.StatusNotFound)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || info.IsDir() {
		c.SetStatusCode(http.StatusNotFound)
		return
	}

	_, ok := file.(io.ReadSeeker)
	if !ok {
		c.String(http.StatusInternalServerError, "admin asset unavailable")
		return
	}

	mimeType := mime.TypeByExtension(filepath.Ext(assetPath))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("Content-Type", mimeType)
	c.SetStatusCode(http.StatusOK)
	io.Copy(c.Response.BodyWriter(), file)
}

func (h *adminSPAHandler) serveIndexHertz(ctx context.Context, c *app.RequestContext) {
	if h.fs == nil {
		c.String(http.StatusInternalServerError, "admin SPA unavailable")
		return
	}

	file, err := h.fs.Open("index.html")
	if err != nil {
		c.String(http.StatusInternalServerError, "admin SPA unavailable")
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || info.IsDir() {
		c.String(http.StatusInternalServerError, "admin SPA unavailable")
		return
	}

	content, err := io.ReadAll(file)
	if err != nil {
		c.String(http.StatusInternalServerError, "admin SPA unavailable")
		return
	}

	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/html; charset=utf-8", content)
}

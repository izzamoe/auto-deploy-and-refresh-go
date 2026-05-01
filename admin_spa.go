package main

import (
	"embed"
	"io"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"strings"
)

//go:embed all:web/admin/dist
var adminSPA embed.FS

func getAdminSPA() http.FileSystem {
	f, err := fs.Sub(adminSPA, "web/admin/dist")
	if err != nil {
		log.Printf("Failed to load admin SPA assets: %v", err)
		return nil
	}
	return http.FS(f)
}

type adminSPAHandler struct {
	fs http.FileSystem
}

func RegisterAdminSPARoutes(mux *http.ServeMux, middleware func(http.Handler) http.Handler) {
	handler := &adminSPAHandler{fs: getAdminSPA()}

	mux.Handle("GET /admin/assets", middleware(http.HandlerFunc(handler.serveAsset)))
	mux.Handle("GET /admin/assets/", middleware(http.HandlerFunc(handler.serveAsset)))
	mux.Handle("GET /admin", middleware(http.HandlerFunc(handler.serveIndex)))
	mux.Handle("GET /admin/", middleware(http.HandlerFunc(handler.serveIndex)))
}

func (h *adminSPAHandler) serveAsset(w http.ResponseWriter, r *http.Request) {
	if h.fs == nil {
		http.Error(w, "admin SPA unavailable", http.StatusInternalServerError)
		return
	}

	assetPath := strings.TrimPrefix(r.URL.Path, "/admin/")
	if assetPath == r.URL.Path || assetPath == "assets" || !strings.HasPrefix(assetPath, "assets/") {
		http.NotFound(w, r)
		return
	}

	file, err := h.fs.Open(assetPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}

	seeker, ok := file.(io.ReadSeeker)
	if !ok {
		http.Error(w, "admin asset unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, filepath.Base(assetPath), info.ModTime(), seeker)
}

func (h *adminSPAHandler) serveIndex(w http.ResponseWriter, r *http.Request) {
	if h.fs == nil {
		http.Error(w, "admin SPA unavailable", http.StatusInternalServerError)
		return
	}

	file, err := h.fs.Open("index.html")
	if err != nil {
		http.Error(w, "admin SPA unavailable", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || info.IsDir() {
		http.Error(w, "admin SPA unavailable", http.StatusInternalServerError)
		return
	}

	seeker, ok := file.(io.ReadSeeker)
	if !ok {
		http.Error(w, "admin SPA unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", info.ModTime(), seeker)
}

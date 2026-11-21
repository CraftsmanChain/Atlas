package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func newWebHandler(staticDir string) http.HandlerFunc {
	staticDir = strings.TrimSpace(staticDir)

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}

		if staticDir == "" {
			serveDefaultRoot(w, r)
			return
		}

		indexPath := filepath.Join(staticDir, "index.html")
		if _, err := os.Stat(indexPath); err != nil {
			serveDefaultRoot(w, r)
			return
		}

		requestPath := filepath.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
		if requestPath == "/" || requestPath == "/index.html" {
			serveIndex(w, r, indexPath)
			return
		}

		targetPath := filepath.Join(staticDir, strings.TrimPrefix(requestPath, "/"))
		if info, err := os.Stat(targetPath); err == nil && !info.IsDir() {
			if strings.HasPrefix(requestPath, "/assets/") {
				// Vite asset names carry a content hash.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeFile(w, r, targetPath)
			return
		}

		if filepath.Ext(requestPath) == "" {
			serveIndex(w, r, indexPath)
			return
		}

		http.NotFound(w, r)
	}
}

// index.html must be revalidated so a deploy's new chunk names are picked up.
func serveIndex(w http.ResponseWriter, r *http.Request, indexPath string) {
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, indexPath)
}

func serveDefaultRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Atlas Server is running\n"))
}

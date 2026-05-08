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
		if requestPath == "/" {
			http.ServeFile(w, r, indexPath)
			return
		}

		targetPath := filepath.Join(staticDir, strings.TrimPrefix(requestPath, "/"))
		if info, err := os.Stat(targetPath); err == nil && !info.IsDir() {
			http.ServeFile(w, r, targetPath)
			return
		}

		if filepath.Ext(requestPath) == "" {
			http.ServeFile(w, r, indexPath)
			return
		}

		http.NotFound(w, r)
	}
}

func serveDefaultRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Atlas Server is running\n"))
}

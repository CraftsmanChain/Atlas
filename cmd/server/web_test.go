package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNewWebHandlerServesIndexAndAssets(t *testing.T) {
	staticDir := t.TempDir()
	indexPath := filepath.Join(staticDir, "index.html")
	assetDir := filepath.Join(staticDir, "assets")
	assetPath := filepath.Join(assetDir, "app.js")

	if err := os.WriteFile(indexPath, []byte("<html>atlas</html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(assetPath, []byte("console.log('atlas')"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	handler := newWebHandler(staticDir)

	rootReq := httptest.NewRequest(http.MethodGet, "/", nil)
	rootRec := httptest.NewRecorder()
	handler(rootRec, rootReq)
	if rootRec.Code != http.StatusOK {
		t.Fatalf("root status = %d, want %d", rootRec.Code, http.StatusOK)
	}
	if body := rootRec.Body.String(); body != "<html>atlas</html>" {
		t.Fatalf("root body = %q", body)
	}

	assetReq := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	assetRec := httptest.NewRecorder()
	handler(assetRec, assetReq)
	if assetRec.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want %d", assetRec.Code, http.StatusOK)
	}
	if body := assetRec.Body.String(); body != "console.log('atlas')" {
		t.Fatalf("asset body = %q", body)
	}
}

func TestNewWebHandlerFallsBackToIndexForSpaRoutes(t *testing.T) {
	staticDir := t.TempDir()
	indexPath := filepath.Join(staticDir, "index.html")

	if err := os.WriteFile(indexPath, []byte("<html>spa</html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}

	handler := newWebHandler(staticDir)

	req := httptest.NewRequest(http.MethodGet, "/alerts/detail/42", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("spa status = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); body != "<html>spa</html>" {
		t.Fatalf("spa body = %q", body)
	}
}

func TestNewWebHandlerWithoutStaticDirKeepsDefaultRoot(t *testing.T) {
	handler := newWebHandler("")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("root status = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); body != "Atlas Server is running\n" {
		t.Fatalf("root body = %q", body)
	}

	missingReq := httptest.NewRequest(http.MethodGet, "/missing.js", nil)
	missingRec := httptest.NewRecorder()
	handler(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, want %d", missingRec.Code, http.StatusNotFound)
	}
}

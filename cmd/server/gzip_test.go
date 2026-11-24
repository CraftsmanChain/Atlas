package main

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWithGzipCompressesLargeJSON(t *testing.T) {
	body := `{"data":"` + strings.Repeat("x", 5000) + `"}`
	h := withGzip(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body[:100])
		_, _ = io.WriteString(w, body[100:])
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
	req.Header.Set("Accept-Encoding", "gzip, br")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected gzip, got headers %v", rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if string(got) != body {
		t.Fatalf("body mismatch: %d vs %d", len(got), len(body))
	}
}

func TestWithGzipPassThrough(t *testing.T) {
	cases := []struct {
		name, accept, ctype string
		status              int
		body                string
	}{
		{"small", "gzip", "application/json", 200, `{"ok":true}`},
		{"no-accept", "", "application/json", 200, strings.Repeat("a", 4000)},
		{"error-status", "gzip", "application/json", 500, strings.Repeat("a", 4000)},
		{"binary", "gzip", "image/png", 200, strings.Repeat("a", 4000)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := withGzip(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", c.ctype)
				w.WriteHeader(c.status)
				_, _ = io.WriteString(w, c.body)
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if c.accept != "" {
				req.Header.Set("Accept-Encoding", c.accept)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != c.body || rec.Code != c.status {
				t.Fatalf("unexpected: code=%d enc=%q len=%d", rec.Code, rec.Header().Get("Content-Encoding"), rec.Body.Len())
			}
		})
	}
}

func TestWithGzipEmptyBody(t *testing.T) {
	h := withGzip(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("code=%d enc=%q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
}

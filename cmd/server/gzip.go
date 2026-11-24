package main

import (
	"bufio"
	"compress/gzip"
	"net"
	"net/http"
	"strings"
	"sync"
)

// gzipMinBytes: responses smaller than this are sent as-is.
const gzipMinBytes = 1024

var gzipWriters = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed)
	return w
}}

func compressible(contentType string) bool {
	ct := strings.ToLower(contentType)
	for _, p := range []string{"application/json", "text/", "application/javascript", "image/svg+xml", "application/xml"} {
		if strings.HasPrefix(ct, p) {
			return true
		}
	}
	return false
}

// withGzip compresses text/JSON/JS responses when the client accepts gzip.
// Behaviour of handlers is unchanged otherwise (status, headers, body bytes).
func withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") || r.Header.Get("Range") != "" {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.finish()
		next.ServeHTTP(gw, r)
	})
}

type gzipResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	decided     bool
	compress    bool
	buf         []byte
	zw          *gzip.Writer
}

func (g *gzipResponseWriter) WriteHeader(status int) {
	if g.wroteHeader {
		return
	}
	g.wroteHeader = true
	g.status = status
}

func (g *gzipResponseWriter) decide(force bool) {
	if g.decided {
		return
	}
	h := g.Header()
	if g.status == 0 {
		g.status = http.StatusOK
	}
	if h.Get("Content-Type") == "" && len(g.buf) > 0 {
		h.Set("Content-Type", http.DetectContentType(g.buf))
	}
	eligible := g.status == http.StatusOK && h.Get("Content-Encoding") == "" && compressible(h.Get("Content-Type"))
	if eligible && len(g.buf) < gzipMinBytes && !force {
		return // wait for more bytes
	}
	g.decided = true
	h.Add("Vary", "Accept-Encoding")
	if eligible && len(g.buf) >= gzipMinBytes {
		g.compress = true
		h.Del("Content-Length")
		h.Set("Content-Encoding", "gzip")
		g.ResponseWriter.WriteHeader(g.status)
		g.zw = gzipWriters.Get().(*gzip.Writer)
		g.zw.Reset(g.ResponseWriter)
		_, _ = g.zw.Write(g.buf)
	} else {
		g.ResponseWriter.WriteHeader(g.status)
		_, _ = g.ResponseWriter.Write(g.buf)
	}
	g.buf = nil
}

func (g *gzipResponseWriter) Write(p []byte) (int, error) {
	if !g.wroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	if !g.decided {
		g.buf = append(g.buf, p...)
		g.decide(false)
		return len(p), nil
	}
	if g.compress {
		return g.zw.Write(p)
	}
	return g.ResponseWriter.Write(p)
}

func (g *gzipResponseWriter) finish() {
	if !g.wroteHeader && len(g.buf) == 0 {
		return // handler wrote nothing; net/http sends 200 empty
	}
	g.decide(true)
	if g.zw != nil {
		_ = g.zw.Close()
		gzipWriters.Put(g.zw)
		g.zw = nil
	}
}

func (g *gzipResponseWriter) Flush() {
	g.decide(true)
	if g.zw != nil {
		_ = g.zw.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := g.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

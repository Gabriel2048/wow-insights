package web

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

// gzipWriters are reused across requests: a gzip.Writer carries a ~256 KB
// window, and allocating one per response would be most of the cost of
// compressing at all.
var gzipWriters = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
	return w
}}

// compressible reports whether a response of this content type is worth
// compressing. Text is; images and already-compressed formats are not, and
// nothing here serves those anyway.
func compressible(contentType string) bool {
	ct, _, _ := strings.Cut(contentType, ";")
	ct = strings.TrimSpace(strings.ToLower(ct))
	switch {
	case strings.HasPrefix(ct, "text/"),
		ct == "application/json",
		ct == "application/javascript",
		ct == "image/svg+xml":
		return true
	}
	return false
}

// compress gzips responses the client accepts and the content type warrants.
// The fight page is one element per cast across four range blocks — machine-
// generated, highly repetitive markup — and shrinks roughly tenfold.
//
// The decision has to wait until the first write, because that is when the
// content type is known; so the header write is deferred too, since
// Content-Encoding must be set before it. Vary is set on every response that
// could have been compressed, whether or not it was, so a cache keys on the
// request's Accept-Encoding either way.
func (s *Server) compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.finish()
		next.ServeHTTP(gw, r)
	})
}

// gzipWriter decides at the first write whether to compress, and then either
// passes bytes straight through or through a pooled gzip.Writer.
type gzipWriter struct {
	http.ResponseWriter
	code    int          // the status the handler asked for, held until the first write
	gz      *gzip.Writer // nil when passing through
	started bool         // the header has been written
}

func (w *gzipWriter) WriteHeader(code int) {
	if w.started {
		return
	}
	w.code = code
}

func (w *gzipWriter) Write(p []byte) (int, error) {
	if !w.started {
		w.start(p)
	}
	if w.gz != nil {
		return w.gz.Write(p)
	}
	return w.ResponseWriter.Write(p)
}

// start commits the headers on the first write. A response with no explicit
// content type would be sniffed by net/http from these same bytes; with
// nosniff on every response that sniff has to happen here and be written out,
// so the client sees exactly the type it would have seen without compression.
func (w *gzipWriter) start(first []byte) {
	w.started = true
	h := w.Header()
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", http.DetectContentType(first))
	}
	code := w.code
	if code == 0 {
		code = http.StatusOK
	}
	if code != http.StatusNoContent && code != http.StatusNotModified && compressible(h.Get("Content-Type")) {
		h.Set("Content-Encoding", "gzip")
		h.Del("Content-Length") // it was for the uncompressed body
		w.gz = gzipWriters.Get().(*gzip.Writer)
		w.gz.Reset(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(code)
}

// finish flushes the gzip trailer once the handler is done, and writes the
// header for a response that had a status but no body.
func (w *gzipWriter) finish() {
	if !w.started {
		if w.code != 0 {
			w.ResponseWriter.WriteHeader(w.code)
		}
		return
	}
	if w.gz != nil {
		_ = w.gz.Close()
		gzipWriters.Put(w.gz)
		w.gz = nil
	}
}

// Flush lets a streaming handler push what it has; the gzip layer is flushed
// first so the bytes actually leave.
func (w *gzipWriter) Flush() {
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// wrote reports whether a response has begun, for the recovery middleware.
func (w *gzipWriter) wroteHeader() bool { return w.started }

package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// A middleware wraps a handler. func(http.Handler) http.Handler is the whole
// abstraction; the chain is built once in Handler and nothing else needs to
// know it exists. The hooks for security headers and compression slot in here.
type middleware func(http.Handler) http.Handler

// Handler is the routes behind the middleware chain, and what Run serves.
// Routes stays exposed so a test can ask the mux which pattern a path resolves
// to; everything that actually serves a request goes through here.
func (s *Server) Handler() http.Handler {
	var h http.Handler = s.Routes()
	// Listed innermost first, so that the request id exists before anything
	// logs, and recovery sits inside the access log so a panic still gets an
	// access line with its status.
	for _, wrap := range []middleware{
		s.recoverPanic,
		s.accessLog,
		s.requestID,
	} {
		h = wrap(h)
	}
	return h
}

// ctxKey keys the things a request carries through its context.
type ctxKey int

const (
	loggerKey ctxKey = iota
	callsKey
)

// requestID gives every request an id, returns it in a response header so a
// user's report can be matched to a log line, and attaches a logger carrying
// it to the context, so every line written while handling this request can be
// grouped. A Cloud Trace header, when present, is carried too — as the raw
// trace id; the shipped binary's log handler is what knows how to spell it
// for Cloud Logging.
func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		w.Header().Set("X-Request-Id", id)
		logger := s.log.With("request_id", id)
		if trace, _, _ := strings.Cut(r.Header.Get("X-Cloud-Trace-Context"), "/"); trace != "" {
			logger = logger.With("trace_id", trace)
		}
		ctx := context.WithValue(r.Context(), loggerKey, logger)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is a broken machine; an id of zeros still
		// groups the lines, which is what an id is for.
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}

// logger returns the request's logger, or the server's when called outside
// the chain (a test driving a handler directly).
func (s *Server) logger(r *http.Request) *slog.Logger {
	if l, ok := r.Context().Value(loggerKey).(*slog.Logger); ok {
		return l
	}
	return s.log
}

// recoverPanic turns a panic in a handler into a clean 500 and one ERROR line
// carrying the request id and the stack, instead of net/http's default: the
// connection severed mid-response and an unstructured stack on stderr with no
// way to tell which request it belonged to.
//
// http.ErrAbortHandler is not a panic in that sense — it is net/http's own
// signal for "abort this response quietly" — so it is passed straight through.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			p := recover()
			if p == nil {
				return
			}
			if err, ok := p.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(p)
			}
			s.logger(r).Error("panic in handler", "panic", p, "stack", string(debug.Stack()))
			if rw, ok := w.(*responseWriter); !ok || !rw.wrote {
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// accessLog writes one line per request once it is done: the route pattern
// rather than the raw path (report codes would give every request its own
// key), the status, how long it took, how much was written, and how many
// upstream calls it cost. RED metrics fall out of those fields with no more
// code than a log-based metric.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w}
		calls := new(int)
		r = r.WithContext(context.WithValue(r.Context(), callsKey, calls))

		next.ServeHTTP(rw, r)

		// r.Pattern is set by the mux when it matches, on this same request,
		// which is why it can be read here after the fact.
		pattern := r.Pattern
		if pattern == "" {
			pattern = "(no route)"
		}
		s.logger(r).Info("request",
			"method", r.Method,
			"pattern", pattern,
			"status", rw.status(),
			"duration_ms", time.Since(start).Milliseconds(),
			"bytes", rw.bytes,
			"upstream_calls", *calls,
		)
	})
}

// responseWriter remembers what was written, for the access line and for the
// recovery middleware's "has a response started" check.
type responseWriter struct {
	http.ResponseWriter
	code  int
	bytes int
	wrote bool
}

func (w *responseWriter) WriteHeader(code int) {
	if !w.wrote {
		w.code = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.code = http.StatusOK
		w.wrote = true
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += n
	return n, err
}

func (w *responseWriter) status() int {
	if !w.wrote {
		return http.StatusOK
	}
	return w.code
}

// countCall records one upstream call against the request, if the request is
// being counted.
func countCall(ctx context.Context) {
	if n, ok := ctx.Value(callsKey).(*int); ok {
		*n++
	}
}

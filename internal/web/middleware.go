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

	"wowinsight/internal/warcraftlogs"
)

// A middleware wraps a handler. func(http.Handler) http.Handler is the whole
// abstraction; the chain is built once in Handler and nothing else needs to
// know it exists. The hooks for security headers and compression slot in here.
type middleware func(http.Handler) http.Handler

// Handler is the routes behind the middleware chain, and what Run serves.
// Routes stays exposed so a test can ask the mux which pattern a path resolves
// to; everything that actually serves a request goes through here.
func (s *Server) Handler() http.Handler {
	mux := s.Routes()
	var h http.Handler = mux
	// Listed innermost first. The request id exists before anything logs;
	// the deadline is inside recovery so a panic from a cancelled context is
	// still caught; the headers go on everything, including a 500 from
	// recovery. Compression, if it ever comes, goes between recovery and the
	// access log so the bytes counted are the bytes on the wire — see #7.
	for _, wrap := range []middleware{
		s.deadline,
		s.recoverPanic,
		s.accessLog(mux),
		securityHeaders,
		s.requestID,
	} {
		h = wrap(h)
	}
	return h
}

// handlerDeadline bounds the work a request may do. It is the first
// end-to-end bound this app has had: the client's own 30s timeout is per
// call, and the cast paging loop multiplies it. A minute covers the slowest
// fight page seen (a 20-minute kill is ~3s) many times over, and is what the
// long renders #2 plans will have to fit inside or be split up.
const handlerDeadline = time.Minute

// deadline puts handlerDeadline on the request context. Upstream calls see
// it as context.DeadlineExceeded, which upstreamFailed logs as a warning
// rather than an upstream failure.
func (s *Server) deadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), s.handlerDeadline)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// securityHeaders is the conservative set that is right for any page here.
// The Content-Security-Policy is deliberately absent: its content depends on
// the tooltips script and the ingress, which #7 decides — this is the hook.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// ctxKey keys the things a request carries through its context.
type ctxKey int

const loggerKey ctxKey = iota

// budgeted is what the real client offers beyond logsClient: where the
// hourly budget stood after its last call. Asked for by type, so the
// interface the handlers consume stays three methods and the test fake need
// not know about budgets.
type budgeted interface {
	Budget() (warcraftlogs.RateLimit, bool)
}

// requestID gives every request an id, returns it in a response header so a
// user's report can be matched to a log line, and attaches a logger carrying
// it to the context, so every line written while handling this request can be
// grouped. A trace id from the proxy in front, when there is one, is carried
// too — raw; the shipped binary's log handler is what knows how to spell it
// for its logging backend.
func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		w.Header().Set("X-Request-Id", id)
		logger := s.log.With("request_id", id)
		if trace := traceID(r.Header); trace != "" {
			logger = logger.With("trace_id", trace)
		}
		ctx := context.WithValue(r.Context(), loggerKey, logger)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// traceID reads the trace id a proxy stamped on the request. The W3C
// traceparent header is tried first — Envoy, Istio, Cloudflare and Cloud Run
// all send it — with Google's own header as the fallback, so the app is not
// tied to one platform by its logs.
func traceID(h http.Header) string {
	// traceparent: version-traceid-spanid-flags, e.g. 00-<32 hex>-<16 hex>-01
	if parts := strings.Split(h.Get("traceparent"), "-"); len(parts) == 4 && len(parts[1]) == 32 {
		return parts[1]
	}
	trace, _, _ := strings.Cut(h.Get("X-Cloud-Trace-Context"), "/")
	return trace
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
			// accessLog's writer is what sits under this handler, and it
			// knows whether anything was written. Anything else here is a
			// chain change; write the 500 only when nothing has gone out.
			rw, ok := w.(*responseWriter)
			if !ok || !rw.wroteHeader() {
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// accessLog writes one line per request once it is done: the route pattern
// rather than the raw path (report codes would give every request its own
// key), the status, how long it took, how much was written, and where the
// hourly points budget stood afterwards. RED metrics fall out of those
// fields with no more code than a log-based metric.
func (s *Server) accessLog(mux *http.ServeMux) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &responseWriter{ResponseWriter: w}

			next.ServeHTTP(rw, r)

			// The mux is asked which pattern the path resolves to, rather
			// than reading r.Pattern: the mux sets that on the request it is
			// handed, and the middleware between here and there hand it a
			// clone.
			_, pattern := mux.Handler(r)
			if pattern == "" {
				pattern = "(no route)"
			}
			attrs := []any{
				"method", r.Method,
				"pattern", pattern,
				"status", rw.status(),
				"duration_ms", time.Since(start).Milliseconds(),
				"bytes", rw.bytes,
			}
			// The budget as the client last saw it — a gauge, not this
			// request's bill: other requests' spend is in the number too.
			if b, ok := s.wcl.(budgeted); ok {
				if snapshot, known := b.Budget(); known {
					attrs = append(attrs,
						"points_spent", snapshot.PointsSpentThisHour,
						"points_limit", snapshot.LimitPerHour,
						"points_reset_in_s", snapshot.PointsResetIn,
					)
				}
			}
			s.logger(r).Info("request", attrs...)
		})
	}
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

func (w *responseWriter) wroteHeader() bool { return w.wrote }

// Unwrap exposes the underlying writer, so http.ResponseController reaches
// through this one for Flush and the deadlines.
func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseWriter) status() int {
	if !w.wrote {
		return http.StatusOK
	}
	return w.code
}

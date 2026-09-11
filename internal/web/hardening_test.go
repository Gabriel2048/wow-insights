package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wowinsight/internal/warcraftlogs"
)

func fightPageClient() fakeWCL {
	return fakeWCL{
		fightDetail: func(context.Context, string, int) (*warcraftlogs.FightDetail, error) { return fightDetail(), nil },
		timeline: func(context.Context, string, warcraftlogs.Fight, int) (*warcraftlogs.Timeline, error) {
			return fullTimeline(), nil
		},
	}
}

func getWith(t *testing.T, wcl logsClient, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	newTestServer(t, wcl).Handler().ServeHTTP(rec, req)
	return rec
}

// A client that accepts gzip gets the page gzipped, smaller, with Vary set so
// a cache keys on the request; a client that does not gets the same bytes it
// always did. Both see the same content type, explicitly — with nosniff on
// every response, a missing Content-Type would be fatal.
func TestFightPageIsGzippedOnlyWhenAccepted(t *testing.T) {
	const target = "/report/ExampleReport123/fight/12?player=7"
	plain := getWith(t, fightPageClient(), target, nil)
	zipped := getWith(t, fightPageClient(), target, map[string]string{"Accept-Encoding": "gzip"})

	if plain.Header().Get("Content-Encoding") != "" {
		t.Error("a client that did not ask for gzip got it")
	}
	if got := zipped.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{"plain": plain, "gzip": zipped} {
		if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
			t.Errorf("%s: Vary = %q, want Accept-Encoding", name, got)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
			t.Errorf("%s: Content-Type = %q, want text/html", name, got)
		}
	}

	zr, err := gzip.NewReader(bytes.NewReader(zipped.Body.Bytes()))
	if err != nil {
		t.Fatalf("the gzipped body does not open: %v", err)
	}
	inflated, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("the gzipped body does not inflate: %v", err)
	}
	if !bytes.Equal(inflated, plain.Body.Bytes()) {
		t.Error("the inflated page is not byte-identical to the uncompressed one")
	}
	if ratio := float64(plain.Body.Len()) / float64(zipped.Body.Len()); ratio < 3 {
		t.Errorf("compression ratio %.1fx, want at least 3x for this markup", ratio)
	}
}

// JSON is compressed too; and a 404 from the mux, which sets text/plain, is
// still a correct response through the compressing writer.
func TestCompressionHandlesJSONAndErrors(t *testing.T) {
	gz := map[string]string{"Accept-Encoding": "gzip"}
	if rec := getWith(t, fakeWCL{}, "/healthz", gz); rec.Header().Get("Content-Encoding") != "gzip" {
		t.Error("/healthz was not gzipped")
	}
	rec := getWith(t, fakeWCL{}, "/no/such/route", gz)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("404 body does not open as gzip: %v", err)
	}
	if body, _ := io.ReadAll(zr); !strings.Contains(string(body), "not found") {
		t.Errorf("404 body = %q", body)
	}
}

// A panic inside a compressed response is still a clean 500: recovery has to
// see through the compressing writer to know nothing was written yet.
func TestPanicInsideACompressedResponseIsStillA500(t *testing.T) {
	wcl := fakeWCL{report: func(context.Context, string) (*warcraftlogs.Report, error) { panic("boom") }}
	rec := getWith(t, wcl, "/?url=https://www.warcraftlogs.com/reports/ExampleReport123", map[string]string{"Accept-Encoding": "gzip"})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestEveryResponseCarriesTheSecurityHeaders(t *testing.T) {
	for _, target := range []string{"/healthz", "/no/such/route", "/report/ExampleReport123/fight/12?player=7"} {
		rec := get(t, fightPageClient(), target)
		for header, want := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "DENY",
			"Referrer-Policy":        "strict-origin-when-cross-origin",
		} {
			if got := rec.Header().Get(header); got != want {
				t.Errorf("%s: %s = %q, want %q", target, header, got, want)
			}
		}
	}
}

// The first end-to-end bound this app has had. A fake that waits on the
// context stands in for a Warcraft Logs that never answers: the page comes
// back (the index shows the error), and the log says the deadline fired, as a
// warning with the request id, not as an upstream failure.
func TestHandlerDeadlineBoundsTheRequest(t *testing.T) {
	s, buf := loggedServer(t, fakeWCL{report: func(ctx context.Context, _ string) (*warcraftlogs.Report, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	s.handlerDeadline = 50 * time.Millisecond

	rec := httptest.NewRecorder()
	start := time.Now()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/?url=https://www.warcraftlogs.com/reports/ExampleReport123", nil))
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the request took %v; the deadline did not bound it", took)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (the index page renders the error)", rec.Code)
	}
	all := lines(t, buf)
	if n := len(withLevel(all, "ERROR")); n != 0 {
		t.Errorf("%d ERROR lines for a deadline, want none:\n%s", n, buf.String())
	}
	warns := withLevel(all, "WARN")
	if len(warns) != 1 || !strings.Contains(warns[0]["msg"].(string), "deadline exceeded") {
		t.Fatalf("WARN lines = %v, want one saying the deadline was exceeded", warns)
	}
	if warns[0]["request_id"] != rec.Header().Get("X-Request-Id") {
		t.Error("the deadline warning does not carry the request id")
	}
}

// The portable header first, the Google one as a fallback.
func TestTraceIDPrefersTraceparent(t *testing.T) {
	both := http.Header{
		"Traceparent":           {"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		"X-Cloud-Trace-Context": {"abc123/1;o=1"},
	}
	if got := traceID(both); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("traceID = %q, want the traceparent trace id", got)
	}
	if got := traceID(http.Header{"X-Cloud-Trace-Context": {"abc123/1;o=1"}}); got != "abc123" {
		t.Errorf("traceID = %q, want abc123 from the Google header", got)
	}
	if got := traceID(http.Header{"Traceparent": {"garbage"}}); got != "" {
		t.Errorf("traceID = %q for a malformed traceparent, want empty", got)
	}
}

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wowinsight/internal/knowledge"
	"wowinsight/internal/warcraftlogs"
)

func fightPageClient() fakeWCL {
	return fakeWCL{
		fightDetail: func(context.Context, string, int) (*warcraftlogs.FightDetail, error) { return fightDetail(), nil },
		timeline: func(context.Context, string, warcraftlogs.Fight, int, knowledge.Knowledge) (*warcraftlogs.Timeline, error) {
			return fullTimeline(), nil
		},
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

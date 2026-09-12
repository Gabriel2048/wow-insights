package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"wowinsight/internal/knowledge"
	"wowinsight/internal/warcraftlogs"
)

// loggedServer is a server whose log lines land in a buffer as JSON, one per
// line, so a test can say exactly what was logged.
func loggedServer(t *testing.T, wcl logsClient) (*Server, *bytes.Buffer) {
	t.Helper()
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	return New(wcl, tpl, slog.New(slog.NewJSONHandler(&buf, nil))), &buf
}

// lines decodes every log line in the buffer.
func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %v\n%s", err, line)
		}
		out = append(out, m)
	}
	return out
}

func withLevel(all []map[string]any, level string) []map[string]any {
	var out []map[string]any
	for _, m := range all {
		if m["level"] == level {
			out = append(out, m)
		}
	}
	return out
}

// The acceptance criterion: a panic in a handler is a clean 500 carrying the
// request id, the connection survives, and exactly one ERROR line is written
// with that same id. The fake's Report panics, which puts the panic inside a
// real handler on a real route rather than in a stub.
func TestPanicInAHandlerIsA500WithOneErrorLine(t *testing.T) {
	s, buf := loggedServer(t, fakeWCL{report: func(context.Context, string) (*warcraftlogs.Report, error) {
		panic("index arithmetic went wrong")
	}})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/?url=https://www.warcraftlogs.com/reports/ExampleReport123", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	id := rec.Header().Get("X-Request-Id")
	if id == "" {
		t.Fatal("no X-Request-Id header on the response")
	}
	errs := withLevel(lines(t, buf), "ERROR")
	if len(errs) != 1 {
		t.Fatalf("got %d ERROR lines, want exactly 1:\n%s", len(errs), buf.String())
	}
	if errs[0]["request_id"] != id {
		t.Errorf("the ERROR line carries request_id %v, want %s (the id on the response)", errs[0]["request_id"], id)
	}
	if stack, _ := errs[0]["stack"].(string); !strings.Contains(stack, "index arithmetic") && !strings.Contains(stack, "panic") {
		t.Error("the ERROR line carries no stack")
	}
	// And the access line still describes the request, with the 500.
	access := withLevel(lines(t, buf), "INFO")
	if len(access) != 1 || access[0]["status"] != float64(500) {
		t.Errorf("access line = %v, want one line with status 500", access)
	}
}

// http.ErrAbortHandler is net/http's own way for a handler to abort a
// response quietly; recovering it would turn a deliberate abort into a logged
// error. It must pass through.
func TestRecoveryLetsErrAbortHandlerThrough(t *testing.T) {
	s, buf := loggedServer(t, fakeWCL{report: func(context.Context, string) (*warcraftlogs.Report, error) {
		panic(http.ErrAbortHandler)
	}})
	defer func() {
		if p := recover(); !errors.Is(p.(error), http.ErrAbortHandler) {
			t.Errorf("recovered %v, want http.ErrAbortHandler to propagate", p)
		}
		if n := len(withLevel(lines(t, buf), "ERROR")); n != 0 {
			t.Errorf("%d ERROR lines were written for an abort, want none", n)
		}
	}()
	s.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/?url=https://www.warcraftlogs.com/reports/ExampleReport123", nil))
}

// One line per request, keyed by the route pattern rather than the path — a
// report code in the key would give every request its own metric.
func TestAccessLineCarriesThePatternAndTheCost(t *testing.T) {
	s, buf := loggedServer(t, fakeWCL{
		fightDetail: func(context.Context, string, int) (*warcraftlogs.FightDetail, error) { return fightDetail(), nil },
		timeline: func(context.Context, string, warcraftlogs.Fight, int, knowledge.Knowledge) (*warcraftlogs.Timeline, error) {
			return fullTimeline(), nil
		},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/report/ExampleReport123/fight/12?player=7", nil)
	req.Header.Set("X-Cloud-Trace-Context", "105445aa7843bc8bf206b12000100000/1;o=1")
	s.Handler().ServeHTTP(rec, req)

	all := lines(t, buf)
	if len(all) != 1 {
		t.Fatalf("got %d log lines, want exactly the access line:\n%s", len(all), buf.String())
	}
	line := all[0]
	for key, want := range map[string]any{
		"msg":      "request",
		"method":   "GET",
		"pattern":  "GET /report/{code}/fight/{id}",
		"status":   float64(200),
		"trace_id": "105445aa7843bc8bf206b12000100000",
	} {
		if line[key] != want {
			t.Errorf("%s = %v, want %v", key, line[key], want)
		}
	}
	if strings.Contains(buf.String(), "ExampleReport123") && line["pattern"] == "GET /report/ExampleReport123/fight/12" {
		t.Error("the access line carries the raw path")
	}
	if line["request_id"] != rec.Header().Get("X-Request-Id") {
		t.Error("the access line's request_id is not the one on the response")
	}
	if b, _ := line["bytes"].(float64); b == 0 {
		t.Error("bytes = 0 for a rendered page")
	}
	if _, ok := line["duration_ms"]; !ok {
		t.Error("no duration_ms on the access line")
	}
}

// A user who navigates away cancels the request; the client reports that as
// an error like any other. Logging it as one would drown the failures that
// matter, so it is an INFO line, and the fight page's timeline branch — which
// renders the page anyway — is a WARNING when it fails for real.
func TestSeverityTellsAClientGoingAwayFromAFailure(t *testing.T) {
	s, buf := loggedServer(t, fakeWCL{report: func(ctx context.Context, _ string) (*warcraftlogs.Report, error) {
		return nil, context.Canceled
	}})
	s.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/?url=https://www.warcraftlogs.com/reports/ExampleReport123", nil))
	if n := len(withLevel(lines(t, buf), "ERROR")); n != 0 {
		t.Errorf("%d ERROR lines for a cancelled request, want none:\n%s", n, buf.String())
	}

	s, buf = loggedServer(t, fakeWCL{
		fightDetail: func(context.Context, string, int) (*warcraftlogs.FightDetail, error) { return fightDetail(), nil },
		timeline: func(context.Context, string, warcraftlogs.Fight, int, knowledge.Knowledge) (*warcraftlogs.Timeline, error) {
			return nil, errors.New("upstream exploded")
		},
	})
	s.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/report/ExampleReport123/fight/12?player=7", nil))
	all := lines(t, buf)
	if n := len(withLevel(all, "ERROR")); n != 0 {
		t.Errorf("%d ERROR lines for a failed timeline on a page that rendered, want none", n)
	}
	warns := withLevel(all, "WARN")
	if len(warns) != 1 || warns[0]["msg"] != "fetch timeline" {
		t.Errorf("WARN lines = %v, want one for the timeline", warns)
	}
}

// Every response carries the id, including ones no handler produced.
func TestEveryResponseCarriesARequestID(t *testing.T) {
	for _, target := range []string{"/healthz", "/no/such/route"} {
		rec := get(t, fakeWCL{}, target)
		if rec.Header().Get("X-Request-Id") == "" {
			t.Errorf("%s: no X-Request-Id", target)
		}
	}
}

// The access line says where the budget stands, when the client knows. The
// fake does not, and the real one is asked by type — so a client that offers
// Budget() is what this exercises.
func TestAccessLineCarriesTheBudgetWhenKnown(t *testing.T) {
	s, buf := loggedServer(t, budgetedFake{fakeWCL: fightPageClient()})
	s.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/report/ExampleReport123/fight/12?player=7", nil))
	all := lines(t, buf)
	if len(all) != 1 {
		t.Fatalf("got %d lines", len(all))
	}
	if all[0]["points_spent"] != float64(118) || all[0]["points_limit"] != float64(3600) || all[0]["points_reset_in_s"] != float64(900) {
		t.Errorf("access line = %v, want the budget snapshot on it", all[0])
	}

	// Without a budgeted client the fields are absent, not zero.
	s, buf = loggedServer(t, fightPageClient())
	s.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/report/ExampleReport123/fight/12?player=7", nil))
	if _, present := lines(t, buf)[0]["points_spent"]; present {
		t.Error("a client with no budget put budget fields on the line")
	}
}

type budgetedFake struct{ fakeWCL }

func (budgetedFake) Budget() (warcraftlogs.RateLimit, bool) {
	return warcraftlogs.RateLimit{LimitPerHour: 3600, PointsSpentThisHour: 118, PointsResetIn: 900}, true
}

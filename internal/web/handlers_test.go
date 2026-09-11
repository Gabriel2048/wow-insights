package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"wowinsight/internal/warcraftlogs"
)

// get drives one request through the real route table and the whole
// middleware chain, and returns the recorder. Going through Handler() rather
// than calling the handler directly is what makes the registered patterns,
// and the middleware, part of what is being tested.
func get(t *testing.T, wcl logsClient, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	newTestServer(t, wcl).Handler().ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
	return rec
}

// A code that is not sixteen alphanumerics never reaches the API. The fake has
// no methods set, so any upstream call would surface as errNotStubbed rather
// than passing silently.
func TestFightRejectsAnInvalidReportCode(t *testing.T) {
	rec := get(t, fakeWCL{}, "/report/tooshort/fight/12")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (a malformed code must not cost an API call)", rec.Code, http.StatusBadRequest)
	}
}

func TestFightRejectsANonNumericFightID(t *testing.T) {
	rec := get(t, fakeWCL{}, "/report/ExampleReport123/fight/notanumber")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// A player id that is not a number, and one that is not in this fight, both
// render the fight page with no player selected rather than failing. That is
// current behaviour; pinning it here is what makes changing it in #12 a visible
// decision rather than an accident.
func TestFightIgnoresAPlayerItCannotResolve(t *testing.T) {
	wcl := fakeWCL{
		fightDetail: func(context.Context, string, int) (*warcraftlogs.FightDetail, error) {
			return fightDetail(), nil
		},
	}
	for _, target := range []string{
		"/report/ExampleReport123/fight/12?player=abc",
		"/report/ExampleReport123/fight/12?player=999",
	} {
		rec := get(t, wcl, target)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want %d", target, rec.Code, http.StatusOK)
		}
		if !strings.Contains(rec.Body.String(), "Pick a player above") {
			t.Errorf("%s: the page should fall back to the no-player state", target)
		}
	}
}

// The timeline is the expensive half and the half most likely to fail. When it
// does, the stats above it are still worth showing — so the page renders 200
// with no timeline. It is also indistinguishable from a player who cast
// nothing, which is why #12 adds a notice.
func TestFightStillRendersTheStatsWhenTheTimelineFails(t *testing.T) {
	wcl := fakeWCL{
		fightDetail: func(context.Context, string, int) (*warcraftlogs.FightDetail, error) {
			return fightDetail(), nil
		},
		timeline: func(context.Context, string, warcraftlogs.Fight, int) (*warcraftlogs.Timeline, error) {
			return nil, errors.New("upstream exploded")
		},
	}
	rec := get(t, wcl, "/report/ExampleReport123/fight/12?player=7")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Testmage") {
		t.Error("the player's stats should still render without a timeline")
	}
	if strings.Contains(body, "upstream exploded") {
		t.Error("the upstream error text reached the page")
	}
	if strings.Contains(body, "Cast timeline") {
		t.Error("the timeline heading rendered even though there is no timeline")
	}
}

func TestFightRendersTheWholePageWithATimeline(t *testing.T) {
	wcl := fakeWCL{
		fightDetail: func(context.Context, string, int) (*warcraftlogs.FightDetail, error) {
			return fightDetail(), nil
		},
		timeline: func(context.Context, string, warcraftlogs.Fight, int) (*warcraftlogs.Timeline, error) {
			return fullTimeline(), nil
		},
	}
	rec := get(t, wcl, "/report/ExampleReport123/fight/12?player=7")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Cast timeline") {
		t.Error("the timeline did not render")
	}
}

// The probe target answers with no I/O at all: the fake has nothing stubbed,
// so any upstream call would surface as errNotStubbed. A probe that spent an
// API point per check would drain the budget on its own.
func TestHealthzTouchesNothing(t *testing.T) {
	rec := get(t, fakeWCL{}, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Errorf("body = %q, want a status", rec.Body.String())
	}
}

// A bare visit renders the form; a submission renders the report.
func TestIndexRendersTheFormAndThenTheReport(t *testing.T) {
	wcl := fakeWCL{report: func(context.Context, string) (*warcraftlogs.Report, error) {
		return reportWithTwoPulls(), nil
	}}

	bare := get(t, wcl, "/")
	if bare.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", bare.Code, http.StatusOK)
	}
	if strings.Contains(bare.Body.String(), "Fixture raid night") {
		t.Error("a bare visit rendered a report without one being asked for")
	}

	submitted := get(t, wcl, "/?url=https://www.warcraftlogs.com/reports/ExampleReport123")
	if !strings.Contains(submitted.Body.String(), "Fixture raid night") {
		t.Error("the submitted report did not render")
	}
}

// A URL that is not a report never reaches the API, and the message says so.
func TestIndexRejectsSomethingThatIsNotAReportURL(t *testing.T) {
	rec := get(t, fakeWCL{}, "/?url=https://example.com/nope")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d — a bad link is a form error, not an HTTP one", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "does not look like a Warcraft Logs report link") {
		t.Error("the page should explain what a report link looks like")
	}
}

// Today the upstream error string is rendered verbatim. This pins it so that
// #12 replacing it with a taxonomy is a deliberate, visible change rather than
// something a reviewer has to notice.
func TestIndexCurrentlyShowsTheUpstreamErrorVerbatim(t *testing.T) {
	wcl := fakeWCL{report: func(context.Context, string) (*warcraftlogs.Report, error) {
		return nil, errors.New(`warcraftlogs: report "ExampleReport123" not found (it may be private)`)
	}}
	rec := get(t, wcl, "/?url=https://www.warcraftlogs.com/reports/ExampleReport123")
	if !strings.Contains(rec.Body.String(), "not found (it may be private)") {
		t.Error("the upstream message is currently surfaced to the user; if that changed, #12 is why")
	}
}

// routes() is the only place patterns are declared. If one is renamed, this is
// what notices before a link somewhere else stops resolving.
func TestEveryRouteIsReachable(t *testing.T) {
	mux := newTestServer(t, fakeWCL{}).Routes()
	for _, target := range []string{
		"/",
		"/report/ExampleReport123/fight/12",
		"/healthz",
	} {
		req := httptest.NewRequest("GET", target, nil)
		if _, pattern := mux.Handler(req); pattern == "" {
			t.Errorf("no route matches %s", target)
		}
	}
}

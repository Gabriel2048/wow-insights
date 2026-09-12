package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wowinsight/internal/knowledge"
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
	// The same error page as every other failure, not net/http's plain text.
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want the error page", ct)
	}
}

func TestFightRejectsANonNumericFightID(t *testing.T) {
	rec := get(t, fakeWCL{}, "/report/ExampleReport123/fight/notanumber")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want the error page", ct)
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

// A failure on the index page is a fixed sentence beside the form, chosen by
// what kind of failure it was. What Warcraft Logs actually said goes to the
// log and never to the page. (Until #12 the upstream string was rendered
// verbatim, and a test pinned that so the change would be visible; this is
// that change.)
func TestIndexShowsAFixedSentenceAndNeverTheUpstreamText(t *testing.T) {
	const secret = "UPSTREAM SAID SOMETHING NOBODY SHOULD SEE"
	for _, tc := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("%w: ExampleReport123", warcraftlogs.ErrReportNotFound), "That report was not found"},
		{&warcraftlogs.APIError{Status: 503, Body: secret}, "did not answer properly"},
		{&warcraftlogs.APIError{Status: 429, Body: secret, RetryAfter: 5 * time.Minute}, "Try again in about 5 minutes"},
		{&warcraftlogs.APIError{Status: 200, Messages: []string{secret}}, "did not answer properly"},
		{warcraftlogs.ErrNoCredentials, "credentials are missing or rejected"},
	} {
		wcl := fakeWCL{report: func(context.Context, string) (*warcraftlogs.Report, error) { return nil, tc.err }}
		rec := get(t, wcl, "/?url=https://www.warcraftlogs.com/reports/ExampleReport123")
		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Errorf("%v: status = %d, want 200 — a form error is not an HTTP error", tc.err, rec.Code)
		}
		if !strings.Contains(body, tc.want) {
			t.Errorf("%v: page lacks %q", tc.err, tc.want)
		}
		if strings.Contains(body, secret) || strings.Contains(body, tc.err.Error()) {
			t.Errorf("%v: the page carries upstream or error text", tc.err)
		}
	}
}

// routes() is the only place patterns are declared. If one is renamed, this is
// what notices before a link somewhere else stops resolving.
func TestEveryRouteIsReachable(t *testing.T) {
	mux := newTestServer(t, fakeWCL{}).routes()
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

// A player whose spec nobody has authored still gets a timeline, analysed
// with the zero tables, and the page says so — an empty cooldown lane would
// otherwise read as a flawless rotation. The Fire Mage gets the real tables
// and no such notice.
func TestUnknownSpecGetsTheZeroTablesAndANotice(t *testing.T) {
	var passed knowledge.Knowledge
	wcl := fakeWCL{
		fightDetail: func(context.Context, string, int) (*warcraftlogs.FightDetail, error) {
			return fightDetail(), nil
		},
		timeline: func(_ context.Context, _ string, _ warcraftlogs.Fight, _ int, know knowledge.Knowledge) (*warcraftlogs.Timeline, error) {
			passed = know
			return fullTimeline(), nil
		},
	}
	const notice = "No rotation knowledge for Holy Priest yet"

	rec := get(t, wcl, "/report/ExampleReport123/fight/12?player=11")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if passed.Spec != (knowledge.SpecID{}) || len(passed.ProcAuraIDs()) != 0 || len(passed.CooldownIDs()) != 0 {
		t.Errorf("the Holy Priest was analysed with %+v, want the zero tables", passed)
	}
	if !strings.Contains(rec.Body.String(), notice) {
		t.Errorf("the page does not say %q", notice)
	}
	if !strings.Contains(rec.Body.String(), "Cast timeline") {
		t.Error("the timeline did not render for the unauthored spec")
	}

	rec = get(t, wcl, "/report/ExampleReport123/fight/12?player=7")
	if want := (knowledge.SpecID{Class: "Mage", Spec: "Fire"}); passed.Spec != want {
		t.Errorf("the Fire Mage was analysed with %+v, want %+v", passed.Spec, want)
	}
	if strings.Contains(rec.Body.String(), "No rotation knowledge") {
		t.Error("the Fire Mage page carries the no-knowledge notice")
	}
}

// A player in neither the damage nor the healing table has no spec, and the
// page must not blame the class for it.
func TestAPlayerWithNoSpecGetsItsOwnNotice(t *testing.T) {
	wcl := fakeWCL{
		fightDetail: func(context.Context, string, int) (*warcraftlogs.FightDetail, error) {
			d := fightDetail()
			d.Players = append(d.Players, warcraftlogs.PlayerStats{ActorID: 13, Name: "Testrogue", Class: "Rogue", FightDuration: 300 * time.Second})
			d.Fight.FriendlyPlayers = append(d.Fight.FriendlyPlayers, 13)
			return d, nil
		},
		timeline: func(context.Context, string, warcraftlogs.Fight, int, knowledge.Knowledge) (*warcraftlogs.Timeline, error) {
			return fullTimeline(), nil
		},
	}
	body := get(t, wcl, "/report/ExampleReport123/fight/12?player=13").Body.String()
	if !strings.Contains(body, "specialisation is unknown") || strings.Contains(body, "No rotation knowledge for Rogue") {
		t.Error("the page blames the class, or says nothing, for a player with no spec")
	}
}

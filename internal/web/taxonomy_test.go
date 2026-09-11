package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"wowinsight/internal/warcraftlogs"
)

const upstreamSecret = "RAW UPSTREAM BODY THAT MUST NEVER REACH A PAGE"

// A fight that does not exist is the user's mistake, not the server's: a
// 404 with one sentence. Before #12 it was a 502 carrying the upstream string,
// which made typos and outages the same number on any error-rate alert.
func TestNonexistentFightIsA404WithAFixedSentence(t *testing.T) {
	wcl := fakeWCL{fightDetail: func(context.Context, string, int) (*warcraftlogs.FightDetail, error) {
		return nil, fmt.Errorf("%w: ExampleReport123 has no fight 999999", warcraftlogs.ErrFightNotFound)
	}}
	rec := get(t, wcl, "/report/ExampleReport123/fight/999999")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "That fight is not in this report.") {
		t.Errorf("body lacks the sentence: %q", body)
	}
	if strings.Contains(body, "999999") || strings.Contains(body, "warcraftlogs:") {
		t.Error("the page carries the error string")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want an HTML error page", ct)
	}
}

// Every classified failure on the fight route: its status, and that nothing
// the API said reaches the body.
func TestFightPageFailuresCarryNoUpstreamText(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{&warcraftlogs.APIError{Status: 503, Body: upstreamSecret}, http.StatusBadGateway},
		{&warcraftlogs.APIError{Status: 200, Messages: []string{upstreamSecret}, Paths: []string{"reportData.report.fights"}}, http.StatusBadGateway},
		{&warcraftlogs.APIError{Status: 200, Messages: []string{"This report does not exist."}, Paths: []string{"reportData.report"}}, http.StatusNotFound},
		{&warcraftlogs.APIError{Status: 429, Body: upstreamSecret}, http.StatusServiceUnavailable},
		{fmt.Errorf("%w: ExampleReport123", warcraftlogs.ErrReportNotFound), http.StatusNotFound},
		{warcraftlogs.ErrBadCredentials, http.StatusServiceUnavailable},
		{context.DeadlineExceeded, http.StatusGatewayTimeout},
	} {
		wcl := fakeWCL{fightDetail: func(context.Context, string, int) (*warcraftlogs.FightDetail, error) { return nil, tc.err }}
		rec := get(t, wcl, "/report/ExampleReport123/fight/12")
		if rec.Code != tc.status {
			t.Errorf("%v: status = %d, want %d", tc.err, rec.Code, tc.status)
		}
		if body := rec.Body.String(); strings.Contains(body, upstreamSecret) || strings.Contains(body, tc.err.Error()) {
			t.Errorf("%v: the page carries upstream or error text", tc.err)
		}
	}
}

// What the API said is for the log, with the request id — and it is there.
func TestUpstreamTextGoesToTheLog(t *testing.T) {
	s, buf := loggedServer(t, fakeWCL{fightDetail: func(context.Context, string, int) (*warcraftlogs.FightDetail, error) {
		return nil, &warcraftlogs.APIError{Status: 503, Body: upstreamSecret}
	}})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/report/ExampleReport123/fight/12", nil))
	errs := withLevel(lines(t, buf), "ERROR")
	if len(errs) != 1 || errs[0]["upstream_body"] != upstreamSecret || errs[0]["upstream_status"] != float64(503) {
		t.Errorf("ERROR lines = %v, want one carrying the upstream body and status", errs)
	}
	if errs[0]["request_id"] != rec.Header().Get("X-Request-Id") {
		t.Error("the log line does not carry the request id")
	}
}

// A render that fails is a clean 500 and nothing else. html/template streams,
// so without the buffer a 200 and half a page were already out when the
// error happened, and "internal server error" landed in the middle of the
// HTML.
func TestARenderFailureIsACleanFiveHundred(t *testing.T) {
	s := newTestServer(t, fakeWCL{})
	rec := httptest.NewRecorder()
	s.render(rec, httptest.NewRequest("GET", "/", nil), http.StatusOK, "no-such-template.html", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "<") || !strings.Contains(body, "internal server error") {
		t.Errorf("body = %q, want the one line and no partial page", body)
	}
}

// Every rendered page commits with its length known, which is what buffering
// buys and what a proxy or a cache wants to see.
func TestPagesCarryContentLength(t *testing.T) {
	rec := get(t, fightPageClient(), "/report/ExampleReport123/fight/12?player=7")
	got, err := strconv.Atoi(rec.Header().Get("Content-Length"))
	if err != nil || got != rec.Body.Len() {
		t.Errorf("Content-Length = %q for a %d-byte body", rec.Header().Get("Content-Length"), rec.Body.Len())
	}
}

// A value that cannot be marshalled is a 500, not a 200 with no body.
func TestWriteJSONFailsCleanly(t *testing.T) {
	s := newTestServer(t, fakeWCL{})
	rec := httptest.NewRecorder()
	s.writeJSON(rec, httptest.NewRequest("GET", "/", nil), http.StatusOK, map[string]any{"bad": make(chan int)})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// The single most common failure — the timeline fetch fails — used to render
// an empty area indistinguishable from a player who cast nothing. Now the
// page says so, in the place the timeline would have been.
func TestAFailedTimelineIsSaidOnThePage(t *testing.T) {
	wcl := fakeWCL{
		fightDetail: func(context.Context, string, int) (*warcraftlogs.FightDetail, error) { return fightDetail(), nil },
		timeline: func(context.Context, string, warcraftlogs.Fight, int) (*warcraftlogs.Timeline, error) {
			return nil, &warcraftlogs.APIError{Status: 502, Body: upstreamSecret}
		},
	}
	rec := get(t, wcl, "/report/ExampleReport123/fight/12?player=7")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Testmage") {
		t.Fatalf("status = %d; the stats page must still render", rec.Code)
	}
	if !strings.Contains(body, "The cast timeline could not be loaded.") {
		t.Error("the page does not say the timeline failed")
	}
	if strings.Contains(body, upstreamSecret) {
		t.Error("the page carries the upstream body")
	}
}

// A document that arrived partially builds what it has and names what it
// lacks, rather than blanking the timeline over one failed field.
func TestAPartialTimelineNamesWhatIsMissing(t *testing.T) {
	wcl := fakeWCL{
		fightDetail: func(context.Context, string, int) (*warcraftlogs.FightDetail, error) { return fightDetail(), nil },
		timeline: func(context.Context, string, warcraftlogs.Fight, int) (*warcraftlogs.Timeline, error) {
			tl := fullTimeline()
			tl.Incomplete = []string{"phases", "bossCasts"}
			return tl, nil
		},
	}
	rec := get(t, wcl, "/report/ExampleReport123/fight/12?player=7")
	if body := rec.Body.String(); !strings.Contains(body, "Part of the timeline was unavailable from Warcraft Logs: phases, bossCasts.") {
		t.Error("the page does not name the missing fields")
	}
}

// ?player=abc used to be swallowed by an if with no else: a 200 with no
// player, no timeline and no log line, exactly like a bare visit. Now it is
// a log line and a notice.
func TestAnUnresolvablePlayerIsLoggedAndSaid(t *testing.T) {
	for target, want := range map[string]string{
		"/report/ExampleReport123/fight/12?player=abc": "player is not a number",
		"/report/ExampleReport123/fight/12?player=999": "player is not in this fight",
	} {
		s, buf := loggedServer(t, fightPageClient())
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", target, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "pick one above") {
			t.Errorf("%s: no notice on the page", target)
		}
		found := false
		for _, l := range lines(t, buf) {
			if l["msg"] == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no log line saying %q:\n%s", target, want, buf.String())
		}
	}
}

// The link a user submits is bounded before it is parsed or echoed back.
func TestASubmittedLinkIsCapped(t *testing.T) {
	long := "https://www.warcraftlogs.com/reports/ExampleReport123?" + strings.Repeat("x", 5000)
	rec := get(t, fakeWCL{}, "/?url="+long)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), strings.Repeat("x", maxURLParam+1)) {
		t.Error("the page echoes more of the link than the cap allows")
	}
}

// The guard refusing to spend is a 503 that says when to come back.
func TestBudgetGuardIsA503WithTheMinutes(t *testing.T) {
	wcl := fakeWCL{fightDetail: func(context.Context, string, int) (*warcraftlogs.FightDetail, error) {
		return nil, &warcraftlogs.BudgetError{Spent: 3300, Limit: 3600, ResetIn: 12 * time.Minute}
	}}
	rec := get(t, wcl, "/report/ExampleReport123/fight/12")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Try again in about 12 minutes") {
		t.Errorf("body lacks the minutes: %q", rec.Body.String())
	}
}

// A stream that had more events than one page holds is said on the page.
func TestATruncatedStreamIsSaidOnThePage(t *testing.T) {
	wcl := fakeWCL{
		fightDetail: func(context.Context, string, int) (*warcraftlogs.FightDetail, error) { return fightDetail(), nil },
		timeline: func(context.Context, string, warcraftlogs.Fight, int) (*warcraftlogs.Timeline, error) {
			tl := fullTimeline()
			tl.Truncated = []string{"bossCasts"}
			return tl, nil
		},
	}
	rec := get(t, wcl, "/report/ExampleReport123/fight/12?player=7")
	if !strings.Contains(rec.Body.String(), "the following lanes end early: bossCasts.") {
		t.Error("the page does not say the stream was cut")
	}
}

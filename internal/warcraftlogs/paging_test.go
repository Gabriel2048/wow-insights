package warcraftlogs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// pagedAPI serves the master data, then a timeline whose casts continue over
// pages, each page programmed by the test.
type pagedAPI struct {
	pages     map[float64]string // start cursor -> the page's casts field
	bossCasts string             // the first page's bossCasts field; one empty page when unset
	queries   []string
}

func (p *pagedAPI) start(t *testing.T) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
	})
	mux.HandleFunc("POST /api", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OperationName string
			Variables     map[string]any
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.queries = append(p.queries, body.OperationName)
		switch body.OperationName {
		case "MasterData":
			_, _ = w.Write([]byte(`{"data":{"reportData":{"report":{"masterData":{"abilities":[{"gameID":133,"name":"Fireball"}],"actors":[],"npcs":[]}}}}}`))
		case "Timeline":
			bossCasts := p.bossCasts
			if bossCasts == "" {
				bossCasts = `{"data":[]}`
			}
			_, _ = w.Write([]byte(`{"data":{"reportData":{"report":{"casts":` + p.pages[1000] + `,
				"lust":{"data":[]},"procs":{"data":[]},"cooldowns":{"data":[]},"raidCDs":{"data":[]},"bossCasts":` + bossCasts + `,
				"damage":{"data":{"series":[]}},"taken":{"data":{"series":[]}},"fights":[],"phases":[]}}}}`))
		case "CastPage":
			start, _ := body.Variables["start"].(float64)
			page, ok := p.pages[start]
			if !ok {
				page = `null`
			}
			if page == "null-report" {
				_, _ = w.Write([]byte(`{"data":{"reportData":{"report":null}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"reportData":{"report":{"casts":` + page + `}}}}`))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New("id", "secret", WithBaseURL(srv.URL+"/oauth/token", srv.URL+"/api"))
	c.backoffBase = 1
	return c
}

func castsPage(ts float64, next any) string {
	nextText := "null"
	if next != nil {
		nextText = strconv.FormatFloat(next.(float64), 'f', -1, 64)
	}
	return `{"data":[{"timestamp":` + strconv.FormatFloat(ts, 'f', -1, 64) + `,"type":"cast","sourceID":7,"targetID":-1,"abilityGameID":133}],"nextPageTimestamp":` + nextText + `}`
}

var pagedFight = Fight{ID: 12, StartTime: 1000, EndTime: 301000}

// Three pages arrive in order and become one cast list.
func TestCastPagesAreFollowedInOrder(t *testing.T) {
	api := &pagedAPI{pages: map[float64]string{
		1000: castsPage(2000, 5000.0),
		5000: castsPage(6000, 9000.0),
		9000: castsPage(10000, nil),
	}}
	c := api.start(t)
	tl, err := c.Timeline(context.Background(), "ExampleReport123", pagedFight, 7, fire)
	if err != nil {
		t.Fatalf("Timeline() returned %v", err)
	}
	if len(tl.Casts) != 3 || tl.Casts[0].Offset > tl.Casts[1].Offset || tl.Casts[1].Offset > tl.Casts[2].Offset {
		t.Errorf("casts = %+v, want three in order", tl.Casts)
	}
	if strings.Join(api.queries, ",") != "MasterData,Timeline,CastPage,CastPage" {
		t.Errorf("queries = %v", api.queries)
	}
	if len(tl.Truncated) != 0 {
		t.Errorf("Truncated = %v for a stream that ended", tl.Truncated)
	}
}

// A cursor that does not advance would loop forever; it ends the stream and
// marks it cut.
func TestANonAdvancingCursorTerminates(t *testing.T) {
	api := &pagedAPI{pages: map[float64]string{
		1000: castsPage(2000, 5000.0),
		5000: castsPage(6000, 5000.0), // points at itself
	}}
	c := api.start(t)
	tl, err := c.Timeline(context.Background(), "ExampleReport123", pagedFight, 7, fire)
	if err != nil {
		t.Fatalf("Timeline() returned %v", err)
	}
	if len(tl.Casts) != 2 || len(api.queries) != 3 {
		t.Errorf("casts=%d queries=%v, want the two pages fetched once each", len(tl.Casts), api.queries)
	}
	if len(tl.Truncated) != 1 || tl.Truncated[0] != "casts" {
		t.Errorf("Truncated = %v, want [casts]", tl.Truncated)
	}
}

// A null report on page two is an error naming the page — not a zero value
// whose nil cursor ends the loop with the rest of the casts silently dropped.
func TestANullReportOnALaterPageIsAnError(t *testing.T) {
	api := &pagedAPI{pages: map[float64]string{
		1000: castsPage(2000, 5000.0),
		5000: "null-report",
	}}
	c := api.start(t)
	_, err := c.Timeline(context.Background(), "ExampleReport123", pagedFight, 7, fire)
	if !errors.Is(err, ErrUpstream) || !strings.Contains(err.Error(), "page 2") {
		t.Errorf("error = %v, want an upstream error naming page 2", err)
	}
}

// The other streams are one page by design. A cursor on one of them is a
// stream that was cut, and the timeline says which.
func TestACursorOnAnUnpagedStreamIsSurfaced(t *testing.T) {
	api := &pagedAPI{
		pages:     map[float64]string{1000: castsPage(2000, nil)},
		bossCasts: `{"data":[],"nextPageTimestamp":7000}`,
	}
	c := api.start(t)
	tl, err := c.Timeline(context.Background(), "ExampleReport123", pagedFight, 7, fire)
	if err != nil {
		t.Fatalf("Timeline() returned %v", err)
	}
	if len(tl.Truncated) != 1 || tl.Truncated[0] != "bossCasts" {
		t.Errorf("Truncated = %v, want [bossCasts]", tl.Truncated)
	}
	if len(api.queries) != 2 {
		t.Errorf("queries = %v; an unpaged stream must not be followed", api.queries)
	}
}

package warcraftlogs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Every document is a const with nothing substituted into it. The timeline
// document used to be built with Sprintf, the one place in the codebase a
// query was made by concatenation.
func TestEveryDocumentIsNamedAndNothingIsSubstitutedIn(t *testing.T) {
	for _, op := range operations {
		if strings.Contains(op.document, "%") {
			t.Errorf("%s: the document carries a %% verb; queries are consts, never formatted", op.name)
		}
		if !strings.HasPrefix(op.document, "query "+op.name) {
			t.Errorf("%s: the document does not open with its own name (%q)", op.name, op.document[:30])
		}
		if !strings.Contains(op.document, "rateLimitData") {
			t.Errorf("%s: the document does not carry the budget gauge", op.name)
		}
	}
}

// Each filter lands in its own named field. With positional Sprintf, swapping
// two arguments mis-filled two lanes with each other's data and said nothing.
func TestEachFilterFeedsItsOwnLane(t *testing.T) {
	doc := timelineOp.document
	for _, lane := range []string{"lust", "procs", "cooldowns", "raidCDs"} {
		block := regexp.MustCompile(`(?s)` + lane + `: events\((.*?)\)`).FindStringSubmatch(doc)
		if block == nil {
			t.Fatalf("no %s lane in the timeline document", lane)
		}
		if !strings.Contains(block[1], "filterExpression: $"+lane) {
			t.Errorf("the %s lane does not filter on $%s:\n%s", lane, lane, block[1])
		}
	}
	vars := timelineVars("ExampleReport123", Fight{ID: 12, StartTime: 1000, EndTime: 301000}, 7)
	for _, lane := range []string{"lust", "procs", "cooldowns", "raidCDs"} {
		expr, _ := vars[lane].(string)
		if !strings.HasPrefix(expr, "ability.id in (") {
			t.Errorf("variable %s = %q, want a filter expression", lane, expr)
		}
	}
	if !strings.Contains(vars["procs"].(string), "48108") || strings.Contains(vars["lust"].(string), "48108") {
		t.Error("Hot Streak belongs to the procs variable and not the lust one")
	}
}

// An empty set omits the argument rather than sending "ability.id in ()",
// which the API rejects — and because every lane is one document, would cost
// the whole timeline.
func TestAnEmptyAbilitySetOmitsTheFilter(t *testing.T) {
	if expr, ok := abilityFilter(nil); ok || expr != "" {
		t.Errorf("abilityFilter(nil) = %q, %v; want nothing", expr, ok)
	}
	vars := map[string]any{}
	filterVariable(vars, "procs", nil)
	if _, present := vars["procs"]; present {
		t.Error("an empty set put a variable in the map; an absent variable is what omits the argument")
	}
	filterVariable(vars, "procs", []int{1, 2})
	if vars["procs"] != "ability.id in (1,2)" {
		t.Errorf("procs = %v", vars["procs"])
	}
}

// The request names its operation — GraphQL's own way — and that name is
// what the fixture recorder keys on.
func TestRequestsCarryTheOperationName(t *testing.T) {
	var seen []string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
	})
	mux.HandleFunc("POST /api", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OperationName string `json:"operationName"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen = append(seen, body.OperationName)
		_, _ = w.Write([]byte(`{"data":{"rateLimitData":{"limitPerHour":3600,"pointsSpentThisHour":1,"pointsResetIn":900},"reportData":{"report":{"code":"ExampleReport123","masterData":{"actors":[]},"fights":[{"id":1}]}}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New("id", "secret", WithBaseURL(srv.URL+"/oauth/token", srv.URL+"/api"))
	_, _ = c.RateLimit(context.Background())
	_, _ = c.Report(context.Background(), "ExampleReport123")
	_, _ = c.FightDetail(context.Background(), "ExampleReport123", 1)
	if strings.Join(seen, ",") != "RateLimit,Report,Fight" {
		t.Errorf("operation names seen = %v", seen)
	}
}

// Every answer updates the gauge, and the expensive queries are refused past
// the guard until the hour the snapshot measured has rolled over.
func TestBudgetGaugeAndGuard(t *testing.T) {
	spent := 100.0
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
	})
	queries := 0
	mux.HandleFunc("POST /api", func(w http.ResponseWriter, r *http.Request) {
		queries++
		_, _ = w.Write([]byte(`{"data":{"rateLimitData":{"limitPerHour":3600,"pointsSpentThisHour":` + strconv.FormatFloat(spent, 'f', -1, 64) + `,"pointsResetIn":900},"reportData":{"report":{"code":"ExampleReport123","fights":[{"id":1}],"masterData":{"actors":[]}}}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New("id", "secret", WithBaseURL(srv.URL+"/oauth/token", srv.URL+"/api"))

	if _, ok := c.Budget(); ok {
		t.Error("a fresh client claims to know the budget")
	}
	if _, err := c.Report(context.Background(), "ExampleReport123"); err != nil {
		t.Fatal(err)
	}
	b, ok := c.Budget()
	if !ok || b.PointsSpentThisHour != 100 || b.LimitPerHour != 3600 {
		t.Errorf("Budget() = %+v, %v after one call, want the snapshot the answer carried", b, ok)
	}

	// The answer says 91% spent; the next expensive query is refused
	// without a call, a cheap one still goes.
	spent = 0.91 * 3600
	if _, err := c.Report(context.Background(), "ExampleReport123"); err != nil {
		t.Fatal(err)
	}
	before := queries
	_, err := c.FightDetail(context.Background(), "ExampleReport123", 1)
	var budgetErr *BudgetError
	if !errors.As(err, &budgetErr) || !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("FightDetail at 91%% returned %v, want a *BudgetError", err)
	}
	if queries != before {
		t.Error("the refused query still reached the API")
	}
	if budgetErr.ResetIn <= 0 || budgetErr.ResetIn > 15*time.Minute {
		t.Errorf("ResetIn = %v, want the ~15 minutes the snapshot said", budgetErr.ResetIn)
	}
	if _, err := c.Report(context.Background(), "ExampleReport123"); err != nil {
		t.Errorf("a cheap query was refused too: %v", err)
	}

	// The snapshot's hour has rolled over: the guard stands down.
	c.mu.Lock()
	c.budgetAt = time.Now().Add(-time.Hour)
	c.mu.Unlock()
	if _, err := c.FightDetail(context.Background(), "ExampleReport123", 1); err != nil {
		t.Errorf("after the hour rolled over FightDetail returned %v", err)
	}
}

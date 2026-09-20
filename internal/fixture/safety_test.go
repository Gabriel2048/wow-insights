package fixture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// redactorFor builds a redactor over one body, the way Write does.
func redactorFor(t *testing.T, bodies ...string) *redactor {
	t.Helper()
	raw := make([][]byte, len(bodies))
	for i, b := range bodies {
		raw[i] = []byte(b)
	}
	r, err := newRedactor(raw)
	if err != nil {
		t.Fatalf("newRedactor: %v", err)
	}
	return r
}

const roster = `{"data":{"reportData":{"report":{"code":"RealReportCode1","owner":{"name":"Realowner"},
  "masterData":{"actors":[{"id":1,"name":"Realmage","subType":"Mage","server":"Realserver"}]}}}}}`

// THE HOLE THIS ISSUE IS ABOUT. A peer's casts are fetched with
// `useActorIDs: false` so that no master data has to be paid for, which
// inlines the actor in every row. The old refusal could not see it: with no
// roster there were no rules, so nothing could survive, so it passed and
// wrote real names.
func TestEventsCarryingActorsInlineAreRefused(t *testing.T) {
	r := redactorFor(t, roster)
	body := []byte(`{"data":{"reportData":{"report":{"code":"RealReportCode1","casts":{"data":[
	  {"timestamp":1,"type":"cast","source":{"id":9,"name":"Someoneelse","type":"Player"}}]}}}}}`)

	_, err := r.apply(body)
	if err == nil {
		t.Fatal("a recording with actors inlined in its events was accepted; that is how a peer's real name reaches testdata/")
	}
	if !strings.Contains(err.Error(), "casts") {
		t.Errorf("err = %v, want it to name the stream", err)
	}
	if strings.Contains(err.Error(), "Someoneelse") {
		t.Error("the refusal message quotes the name it refused; an error message is a place a real name escapes to")
	}
}

// Character rankings live under data.worldData, which the redactor never
// opened, and carry names, guilds, servers and report codes for up to a
// hundred real people. An unknown shape is refused rather than written.
func TestAnUnknownResponseShapeIsRefusedRatherThanWritten(t *testing.T) {
	r := redactorFor(t, roster)
	body := []byte(`{"data":{"reportData":{"report":{"code":"RealReportCode1"}},
	  "worldData":{"encounter":{"characterRankings":{"rankings":[{"name":"Someoneelse","server":{"name":"Realserver2"}}]}}}}}`)

	_, err := r.apply(body)
	if err == nil {
		t.Fatal("a response shape nothing knows how to redact was written; character rankings would have committed a hundred real names")
	}
	if !strings.Contains(err.Error(), "worldData") {
		t.Errorf("err = %v, want it to name the shape it does not know", err)
	}
}

// Two reports in one recording used to have their code rule decided by map
// iteration order, so the same inputs produced a clean recording on one run
// and a leaked code on the next.
func TestTwoReportsBothGetAPseudonymAndAlwaysTheSameOne(t *testing.T) {
	second := `{"data":{"reportData":{"report":{"code":"RealReportCode2",
	  "masterData":{"actors":[{"id":2,"name":"Otherplayer","subType":"Priest"}]}}}}}`

	var first string
	for run := range 20 {
		// Hand them over in both orders: the result must not depend on it.
		var r *redactor
		if run%2 == 0 {
			r = redactorFor(t, roster, second)
		} else {
			r = redactorFor(t, second, roster)
		}
		out, err := r.apply([]byte(`{"data":{"reportData":{"report":{"code":"RealReportCode2"}}}}`))
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if strings.Contains(string(out), "RealReportCode") {
			t.Fatalf("run %d: a real report code survived: one of two reports had no rule", run)
		}
		if first == "" {
			first = string(out)
		} else if string(out) != first {
			t.Fatalf("run %d produced %s, run 0 produced %s: the pseudonym depends on map order", run, out, first)
		}
	}
}

// The refusal must never quote what it refused.
func TestARefusalNamesTheKindAndNeverTheValue(t *testing.T) {
	r := redactorFor(t, roster)
	// A rankings payload carrying a name the redactor has no rule for.
	body := []byte(`{"data":{"reportData":{"report":{"code":"RealReportCode1","rankings":{"data":[
	  {"roles":{"dps":{"characters":[{"name":"Unknownperson","server":"Unknownrealm"}]}}}]}}}}}`)

	_, err := r.apply(body)
	if err == nil {
		t.Fatal("a ranked character nobody had a rule for was written")
	}
	for _, secret := range []string{"Unknownperson", "Unknownrealm"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the refusal quotes %q", secret)
		}
	}
}

// THE REGRESSION GUARD. Everything already committed must still pass, or
// this change has broken the one recording the whole offline path rests on.
func TestEveryCommittedFixtureStillPasses(t *testing.T) {
	names, err := filepath.Glob(filepath.Join("..", "..", "testdata", "*.json"))
	if err != nil || len(names) == 0 {
		t.Skip("no committed recording here")
	}
	r := &redactor{} // no rules: the committed files are already redacted
	for _, name := range names {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var tree any
		if err := json.Unmarshal(body, &tree); err != nil {
			t.Fatalf("%s: %v", filepath.Base(name), err)
		}
		if err := r.verify(tree); err != nil {
			t.Errorf("%s: the committed recording would now be refused: %v", filepath.Base(name), err)
		}
	}
}

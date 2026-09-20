package warcraftlogs

import (
	"encoding/json"
	"testing"
)

// Warcraft Logs rates a pull against every other parse of that spec, and
// again against only the parses at the same item level. A player looking at
// their own fight wants the second number, because gear is not play — so the
// bracket percentile is what the page leads with.
func TestRankingJoinsTheRosterByNameBecauseTheIDsAreDifferent(t *testing.T) {
	// The id on a ranked row is the character's global Warcraft Logs id; the
	// roster uses this report's actor ids, which are small. Joining on id
	// would silently match nothing, so the join is on name and this test is
	// what says so.
	rankings := rankingsData{Data: []rankedFight{{
		FightID: 12,
		Roles: map[string]rankedRole{
			"dps": {Characters: []rankedCharacter{
				{Name: "Testmage", RankPercent: 55.4, BracketPercent: 61.2, TotalParses: 440, BracketData: 326},
			}},
			"healers": {Characters: []rankedCharacter{
				{Name: "Testpriest", RankPercent: 98, BracketPercent: 99, TotalParses: 120, BracketData: 320},
			}},
		},
	}}}

	byName, ok := rankingsByName(mustJSON(t, rankings), 12)
	if !ok {
		t.Fatal("a well-formed payload was reported undecodable")
	}
	if len(byName) != 2 {
		t.Fatalf("got %d ranked players, want 2 (every role is ranked, not just dps)", len(byName))
	}
	mage, ok := byName["Testmage"]
	if !ok {
		t.Fatal("Testmage is not in the index; the join key must be the name, not the id")
	}
	if mage.TotalParses != 440 {
		t.Errorf("got %d parses, want 440", mage.TotalParses)
	}
	if got := mage.Percentile(); got != "61st" {
		t.Errorf("Percentile() = %q, want %q (the bracket percentile is what the page leads with)", got, "61st")
	}
	if got := mage.Overall(); got != "55th" {
		t.Errorf("Overall() = %q, want %q", got, "55th")
	}
}

// A pull nobody ranked, and a recording made before the fight query asked for
// rankings, both arrive as an absent payload. Neither may become a confident
// zero on the page.
func TestAnAbsentRankingsPayloadIsNoRankingRatherThanZero(t *testing.T) {
	got, ok := rankingsByName(nil, 12)
	if len(got) != 0 {
		t.Errorf("got %d rankings from an absent payload, want none", len(got))
	}
	if !ok {
		t.Error("an absent payload was reported undecodable; absent and broken are different")
	}
}

// fightIDs asks for one fight, but rankings is a list. A row carrying another
// fight's ranking must not be attributed to this one.
func TestRankingsForAnotherFightAreNotUsed(t *testing.T) {
	rankings := rankingsData{Data: []rankedFight{{
		FightID: 99,
		Roles:   map[string]rankedRole{"dps": {Characters: []rankedCharacter{{Name: "Testmage", TotalParses: 5}}}},
	}}}
	if got, _ := rankingsByName(mustJSON(t, rankings), 12); len(got) != 0 {
		t.Errorf("got %d rankings, want none: fight 99's rankings are not fight 12's", len(got))
	}
}

// The teens are the case every naive ordinal gets wrong.
func TestOrdinalHandlesTheTeens(t *testing.T) {
	for n, want := range map[int]string{
		0: "0th", 1: "1st", 2: "2nd", 3: "3rd", 4: "4th",
		11: "11th", 12: "12th", 13: "13th",
		21: "21st", 22: "22nd", 23: "23rd",
		100: "100th", 111: "111th", 101: "101st",
	} {
		if got := ordinal(n); got != want {
			t.Errorf("ordinal(%d) = %q, want %q", n, got, want)
		}
	}
}

// The leaderboard position arrives as "~573" — a string, approximate — on
// most rows. Decoding the payload must not fail the fight page over a tile,
// so an unusable payload is reported rather than returned as an error.
func TestAnUndecodablePayloadCostsTheTileAndNotThePage(t *testing.T) {
	raw := json.RawMessage(`{"data":[{"fightID":12,"roles":{"dps":{"characters":[{"name":"Testmage","rankPercent":"not a number"}]}}}]}`)
	got, ok := rankingsByName(raw, 12)
	if ok {
		t.Error("a payload that will not decode was reported fine")
	}
	if len(got) != 0 {
		t.Errorf("got %d rankings out of an undecodable payload, want none", len(got))
	}
}

// The real API sends the approximate rank as a string. This is the exact
// shape that turned the fight page into a 502 before rankings were decoded
// separately, and it must now cost nothing but the tile.
func TestTheRealShapeOfAnApproximateRankDoesNotBreakTheDecode(t *testing.T) {
	raw := json.RawMessage(`{"data":[{"fightID":12,"roles":{"dps":{"characters":[
		{"name":"Testmage","rank":"~573","best":"~573","rankPercent":97,"bracketPercent":88,"totalParses":19106,"bracketData":323}]}}}]}`)
	got, ok := rankingsByName(raw, 12)
	if !ok {
		t.Fatal("the real payload shape was reported undecodable")
	}
	mage, found := got["Testmage"]
	if !found {
		t.Fatal("Testmage is missing from a payload that names them")
	}
	if mage.TotalParses != 19106 || mage.Percentile() != "88th" {
		t.Errorf("got %s of %d parses, want 88th of 19106", mage.Percentile(), mage.TotalParses)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal the fixture: %v", err)
	}
	return b
}

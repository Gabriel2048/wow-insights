package coach

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"wowinsight/internal/knowledge"
	"wowinsight/internal/warcraftlogs"
)

// **The invariant that keeps the rules honest.** Every sentence a rule writes
// is checked against the evidence that rule chose to show, using exactly the
// validator the model's prose goes through. A rule whose Detail states a
// number it did not put in Evidence fails here — which is the right place for
// it to fail, because it means the page is showing a player a claim and
// withholding the working behind it.
//
// It is also what makes the validator safe to be strict: if the analyser's own
// wording passes it, a rewrite that stays inside the same numbers passes it
// too, and there is no honest sentence the check refuses.
func TestEveryFindingTheAnalyserWritesIsBackedByItsOwnEvidence(t *testing.T) {
	found := realFindings(t)
	if len(found) == 0 {
		t.Fatal("no findings were produced, so this test is asserting nothing")
	}
	for _, f := range found {
		allowed := evidenceNumerals(f)
		for _, prose := range []struct{ what, text string }{{"title", f.Title}, {"detail", f.Detail}} {
			if err := checkNumbers(prose.text, allowed); err != nil {
				t.Errorf("rule %q states a number in its %s that is not in its own evidence: %v\n  %s",
					f.RuleID, prose.what, err, prose.text)
			}
		}
	}
}

// realFindings runs the live rules over a pull built to trip them: a player
// who used their cooldown twice early and then never again.
func realFindings(t *testing.T) []warcraftlogs.Finding {
	t.Helper()
	know := fireTables()
	timeline := &warcraftlogs.Timeline{
		Duration: 7 * time.Minute,
		Casts: []warcraftlogs.Cast{
			{AbilityID: 190319, Name: "Combustion", Offset: 10 * time.Second},
			{AbilityID: 190319, Name: "Combustion", Offset: 2*time.Minute + 13*time.Second},
		},
	}
	return warcraftlogs.Findings(timeline, know, timeline.Duration)
}

// The scanner is where every numeric check begins, so what it reads out of a
// string is worth pinning directly.
func TestNumeralsAreReadWithTheirKind(t *testing.T) {
	for _, c := range []struct {
		text string
		want numeral
	}{
		{"37s", numeral{kind: kindDuration, val: 37}},
		{"37 seconds", numeral{kind: kindDuration, val: 37}},
		{"1500ms", numeral{kind: kindDuration, val: 1.5}},
		{"2 minutes", numeral{kind: kindDuration, val: 120}},
		{"4:53", numeral{kind: kindClock, val: 293}},
		{"1:09:20", numeral{kind: kindClock, val: 4160}},
		{"12%", numeral{kind: kindPercent, val: 12}},
		{"2 uses", numeral{kind: kindCount, val: 2}},
		{"37.4s", numeral{kind: kindDuration, val: 37.4, places: 1}},
	} {
		got := scanNumerals(c.text)
		if len(got) != 1 {
			t.Errorf("%q: read %d numerals, want 1 (a quantity and its unit are one claim)", c.text, len(got))
			continue
		}
		if got[0] != c.want {
			t.Errorf("%q: got %+v, want %+v", c.text, got[0], c.want)
		}
	}
}

// A moment and a length are never the same claim, however equal their
// arithmetic. "4:53" is somewhere in the pull the player can click on; "293
// seconds" is how long something lasted, and a page that swaps them is a page
// pointing at nothing.
func TestAMomentIsNeverALength(t *testing.T) {
	moment := scanNumerals("4:53")
	if admits(moment, scanNumerals("293 seconds")[0]) {
		t.Error("293 seconds was admitted by evidence that says 4:53")
	}
	if !admits(moment, scanNumerals("4:53")[0]) {
		t.Error("4:53 was refused by evidence that says 4:53")
	}
}

// The words a model reaches for when it does not have a number.
func TestQuantitiesDressedAsWordsAreRefused(t *testing.T) {
	for _, c := range []struct {
		text string
		bad  bool
	}{
		{"it sat there for thirty seconds", true},
		{"you got two uses out of it", true},
		{"you used it twice", true},
		{"half of the pull was left", true},
		{"most of the window was wasted", true},
		{"one of your strongest windows went unused", false},
		{"it came back at 5:54 and sat there", false},
		{"all the evidence is below", false},
	} {
		_, got := spelledNumber(c.text)
		if got != c.bad {
			t.Errorf("%q: refused = %v, want %v", c.text, got, c.bad)
		}
	}
}

// Proper nouns are where invention actually lives: a sentence naming a talent
// the player does not have contains no numbers at all and reads with total
// authority.
func TestOnlyWordsFromThisPullMayBeNamed(t *testing.T) {
	lexicon := map[string]bool{"combustion": true, "hot": true, "streak": true}
	for _, c := range []struct {
		text string
		ok   bool
	}{
		{"Combustion sat ready and you had Hot Streak up.", true},
		{"Pressing it earlier would have lined up better.", true},
		{"You should have used Rune of Power instead.", false},
		{"Shifting Power was the better button here.", false},
	} {
		err := checkVocabulary(c.text, lexicon)
		if (err == nil) != c.ok {
			t.Errorf("%q: err = %v, want ok = %v", c.text, err, c.ok)
		}
	}
}

// The sheet is the pull compressed: small enough to send, complete enough to
// judge a hold by.
func TestTheFactSheetIsSmallAndSaysWhatTheJudgementNeeds(t *testing.T) {
	in := fixtureInput(t)
	sheet, err := buildFacts(in)
	if err != nil {
		t.Fatal(err)
	}
	body, err := sheet.marshal()
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 16<<10 {
		t.Errorf("the sheet is %d bytes; a whole pull should fit in a few kilobytes", len(body))
	}
	// The four things a deterministic rule cannot see, and which are the
	// whole reason a model is asked at all.
	for _, want := range []string{"intermission", "lust", "boss_mechanics", "judged_cooldowns"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the sheet has no %q, so nothing can judge whether a hold was deliberate", want)
		}
	}
	// And a moment in the sheet reads the same as a moment in the evidence,
	// so nothing has to convert between two notations for one fact.
	if !strings.Contains(string(body), `"3:44"`) {
		t.Errorf("the sheet does not write moments the way the page does:\n%s", body)
	}
}

// The sheet is built the same way twice, because a recording keyed beside it
// would otherwise miss on the second run for no reason anybody could see.
func TestTheSheetIsTheSameBytesEveryTime(t *testing.T) {
	in := fixtureInput(t)
	in.Know.JudgedCooldowns[12472] = knowledge.JudgedCooldown{Name: "Icy Veins", Base: 180}
	in.Timeline.Casts = append(in.Timeline.Casts, warcraftlogs.Cast{AbilityID: 12472, Name: "Icy Veins", Offset: 30 * time.Second})

	first, err := buildFacts(in)
	if err != nil {
		t.Fatal(err)
	}
	want, err := first.marshal()
	if err != nil {
		t.Fatal(err)
	}
	for range 24 {
		again, err := buildFacts(in)
		if err != nil {
			t.Fatal(err)
		}
		got, err := again.marshal()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("two builds of one pull differ; Go ranges maps randomly and something is not sorted\n got: %s\nwant: %s", got, want)
		}
	}
}

// The sheet holds no one. This is the same assertion the end-to-end test
// makes about the wire, made here against the structure so that a field added
// to the sheet is caught by the cheaper test.
func TestTheSheetNamesNobody(t *testing.T) {
	in := fixtureInput(t)
	sheet, err := buildFacts(in)
	if err != nil {
		t.Fatal(err)
	}
	body, err := sheet.marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := identityOf(in).check(body, in.Findings); err != nil {
		t.Errorf("the sheet would not be allowed out: %v", err)
	}
	// The lust window is where a name would be, because RaidWindow carries
	// the raider who pressed it.
	if strings.Contains(string(body), fakeOther) {
		t.Error("the sheet names whoever pressed Time Warp")
	}
}

// The schema carries two constraints no validator can match, because the
// model is decoded against it and cannot emit anything else: the ref is an
// enum of what was actually handed out, and the properties are in the order
// they should be written.
//
// The order is the one that bit. Go marshals a map's keys alphabetically,
// which put "detail" first — so a constrained decoder was asked for the prose
// before the model had committed to which finding it was about, and a real
// call came back with the detail empty.
func TestTheSchemaNamesTheRefsAndOrdersTheFields(t *testing.T) {
	in := fixtureInput(t)
	refs := []string{ref(in.Findings[0])}
	schema, err := replySchema(refs)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)

	if !strings.Contains(got, `"enum":["cooldown-unused-tail@5:54"]`) {
		t.Errorf("the ref is not an enum of the refs handed out:\n%s", got)
	}
	want := `"properties":{"ref":`
	if !strings.Contains(got, want) {
		t.Errorf("the item's first property is not ref:\n%s", got)
	}
	order := []string{`"ref":`, `"title":`, `"detail":`, `"set_aside":`, `"set_aside_reason":`}
	at := 0
	for _, field := range order {
		i := strings.Index(got[at:], field)
		if i < 0 {
			t.Fatalf("the schema has no %s:\n%s", field, got)
		}
		at += i
	}
	// minItems is accepted at 0 or 1 and is set; maxItems and uniqueItems are
	// refused by the API, so "one block per finding" cannot live here. If
	// that ever changes, this is the test that should start failing.
	if !strings.Contains(got, `"minItems":1`) {
		t.Errorf("the schema does not forbid an empty reply:\n%s", got)
	}
	for _, unsupported := range []string{"maxItems", "uniqueItems"} {
		if strings.Contains(got, unsupported) {
			t.Errorf("the schema sends %s, which the API rejects for an array", unsupported)
		}
	}
}

package warcraftlogs

import (
	"strings"
	"testing"
	"time"

	"wowinsight/internal/knowledge"
)

// combustion is the one Fire Mage cooldown whose spacing is judged.
const combustion = 190319

func judgedFire() knowledge.Knowledge {
	return knowledge.Knowledge{
		JudgedCooldowns: map[int]knowledge.JudgedCooldown{
			combustion: {Name: "Combustion", Base: 120},
		},
	}
}

// timelineOfUses builds a pull in which one ability was cast at the given
// offsets and nothing else happened.
func timelineOfUses(fight time.Duration, ability int, at ...time.Duration) *Timeline {
	t := &Timeline{Duration: fight}
	for _, offset := range at {
		t.Casts = append(t.Casts, Cast{AbilityID: ability, Name: "Combustion", Offset: offset, End: offset})
	}
	return t
}

// The recorded kill, as it actually is. The player used Combustion seven
// times with gaps of 78.5, 73.8, 76.0, 66.1, 62.2 and 61.0 seconds across a
// 431 s pull. Their own fastest gap is 61 s, so the rule holds them to that
// and reports 52 s of cumulative drift.
//
// It reports NO lost use, and that is the interesting part. Perfect play
// from the same opener does fit an eighth Combustion — at 428.2 s, with 3.3 s
// of pull left. A damage cooldown pressed three seconds before the boss dies
// does nothing, so counting it would be a true sentence that misleads, and a
// coaching page cannot afford those. The drift is real and is said; the lost
// use is not claimed.
func TestCooldownDriftOnTheRecordedKill(t *testing.T) {
	at := []time.Duration{1200, 79700, 153500, 229500, 295600, 357800, 418800}
	for i := range at {
		at[i] *= time.Millisecond
	}
	found := Findings(timelineOfUses(431472*time.Millisecond, combustion, at...), judgedFire())

	if len(found) != 1 {
		t.Fatalf("got %d findings, want 1 — drift only. The first use was at 1.2s so it is not late, and the last was at 6:59 of a 7:11 pull so nothing was left unused: %+v", len(found), found)
	}
	f := found[0]
	if f.RuleID != ruleCooldownDrift {
		t.Errorf("RuleID = %q, want %q", f.RuleID, ruleCooldownDrift)
	}
	if f.Severity != Minor {
		t.Errorf("Severity = %v, want minor: the drift is real but cost no use", f.Severity)
	}
	// What happened, why it is bad, what should have happened — in that order.
	for _, want := range []string{
		"it was ready again around", // what should have happened, and when
		"and you used it at",        // what happened instead
		"added up to 52s",           // what it cost across the pull
		"damage you do not do",      // why it matters
	} {
		if !strings.Contains(f.Detail, want) {
			t.Errorf("Detail = %q\n  missing %q", f.Detail, want)
		}
	}
}

// The recorded wipe. Three uses, barely any drift between them — but the
// first came 37.8 s in, which is the actual fault and a different finding.
func TestALateFirstUseIsItsOwnFinding(t *testing.T) {
	at := []time.Duration{37800, 111300, 181200}
	for i := range at {
		at[i] *= time.Millisecond
	}
	found := Findings(timelineOfUses(239727*time.Millisecond, combustion, at...), judgedFire())

	if len(found) != 1 {
		t.Fatalf("got %d findings, want 1 (the late opener; 3.5s of drift is below the floor): %+v", len(found), found)
	}
	if found[0].RuleID != ruleCooldownLate {
		t.Errorf("RuleID = %q, want %q", found[0].RuleID, ruleCooldownLate)
	}
	// It points at the opener, not at the late cast: the opener is where a
	// player should look to see what they did instead.
	if got := found[0].Timestamp(); got != "0:00" {
		t.Errorf("Timestamp() = %q, want 0:00 — the finding is about the opener", got)
	}
	if !strings.Contains(found[0].Detail, "should go out in the opener") {
		t.Errorf("Detail = %q, want it to say what should have happened", found[0].Detail)
	}
}

// THE PROPERTY THIS RULE RESTS ON. Measuring against the player's own
// fastest gap means a player who is late on every single use looks like a
// player whose cooldown is simply longer — so the rule under-reports rather
// than inventing a mistake. It must stay silent here even though this player
// really did drift, because nothing in the log can tell the two apart.
func TestAPlayerLateEveryTimeIsNeverWronglyAccused(t *testing.T) {
	// Every gap identical at 100 s: indistinguishable from a 100 s cooldown.
	at := []time.Duration{0, 100 * time.Second, 200 * time.Second, 300 * time.Second}
	found := Findings(timelineOfUses(400*time.Second, combustion, at...), judgedFire())
	for _, f := range found {
		if f.RuleID == ruleCooldownDrift {
			t.Errorf("accused a player of drift on evenly spaced uses: %q", f.Detail)
		}
	}
}

// A reset or a second charge must not be read as a two-second cooldown. This
// is the Blazing Barrier case from the recorded kill, where the shortest gap
// between uses is 7.6 s: unbounded, that claims forty-one missed uses.
func TestAResetIsNotATwoSecondCooldown(t *testing.T) {
	at := []time.Duration{0, 8 * time.Second, 70 * time.Second, 140 * time.Second, 210 * time.Second}
	found := Findings(timelineOfUses(300*time.Second, combustion, at...), judgedFire())
	for _, f := range found {
		if f.RuleID != ruleCooldownDrift {
			continue
		}
		for _, e := range f.Evidence {
			if e.Label == "room for" && !strings.HasPrefix(e.Value, "5 ") && !strings.HasPrefix(e.Value, "6 ") {
				t.Errorf("room for %q on a 120s cooldown across 300s; an 8s gap was read as the cooldown", e.Value)
			}
		}
	}
}

// A spec nobody has authored judges nothing, which is what makes the zero
// value safe to analyse with.
func TestAnUnauthoredSpecProducesNoFindings(t *testing.T) {
	at := []time.Duration{40 * time.Second, 200 * time.Second, 400 * time.Second}
	if found := Findings(timelineOfUses(500*time.Second, combustion, at...), knowledge.Knowledge{}); len(found) != 0 {
		t.Errorf("got %d findings for a spec with no judged cooldowns, want none: %+v", len(found), found)
	}
}

// A clean pull produces nothing, and the page has to be able to say so
// rather than look broken.
func TestACleanPullProducesNoFindings(t *testing.T) {
	at := []time.Duration{2 * time.Second, 63 * time.Second, 124 * time.Second, 185 * time.Second}
	if found := Findings(timelineOfUses(240*time.Second, combustion, at...), judgedFire()); len(found) != 0 {
		t.Errorf("got %d findings on a pull with no drift, want none: %+v", len(found), found)
	}
}

// Nothing here carries a position, a percentage or a row: that is
// internal/view's business, and the compiler cannot catch a field that
// merely looks like geometry.
func TestAFindingCarriesNoGeometry(t *testing.T) {
	f := Finding{At: 65 * time.Second}
	if got := f.Timestamp(); got != "1:05" {
		t.Errorf("Timestamp() = %q, want 1:05", got)
	}
	if got := f.AtMS(); got != 65000 {
		t.Errorf("AtMS() = %d, want 65000", got)
	}
}

func TestFindingsOfANilTimeline(t *testing.T) {
	if found := Findings(nil, judgedFire()); found != nil {
		t.Errorf("got %+v for a pull that could not be loaded, want nil", found)
	}
}

// Drift and an unused tail are separate findings with separate causes, and
// a pull can carry both. Blaming uses that never came on uses that came late
// explains neither — which is what the first version of this rule did, on a
// real log, with 37s of drift and two minutes of unused cooldown at the end.
func TestDriftAndAnUnusedTailAreSeparateFindings(t *testing.T) {
	// Gaps of 70, 140 and 70 seconds — one bad wait in the middle — and then
	// 320 seconds of pull after the last use.
	at := []time.Duration{0, 70 * time.Second, 210 * time.Second, 280 * time.Second}
	found := Findings(timelineOfUses(600*time.Second, combustion, at...), judgedFire())

	byRule := map[string]Finding{}
	for _, f := range found {
		byRule[f.RuleID] = f
	}
	if len(found) != 2 || byRule[ruleCooldownDrift].RuleID == "" || byRule[ruleCooldownTail].RuleID == "" {
		t.Fatalf("got %d findings, want drift and unused-tail separately: %+v", len(found), found)
	}
	if got := byRule[ruleCooldownDrift].Detail; !strings.Contains(got, "ready again around") {
		t.Errorf("the drift finding does not say when it should have been used: %q", got)
	}
	if strings.Contains(byRule[ruleCooldownDrift].Detail, "left on the table") {
		t.Error("the drift finding claims uses were lost; that is the tail finding's claim, and blaming one on the other explains neither")
	}
	tail := byRule[ruleCooldownTail]
	if tail.Severity != Major {
		t.Errorf("the unused tail is %v, want major", tail.Severity)
	}
	if !strings.Contains(tail.Detail, "4 full uses") {
		t.Errorf("tail Detail = %q, want it to name the four uses left unspent", tail.Detail)
	}
	if !strings.Contains(tail.Detail, "never pressed again") {
		t.Errorf("tail Detail = %q, want it to say plainly what happened", tail.Detail)
	}
}

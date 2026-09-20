package warcraftlogs

import (
	"strings"
	"testing"
	"time"

	"wowinsight/internal/knowledge"
)

// combustion is the one Fire Mage cooldown whose spacing is judged.
const combustion = 190319

// survived means the player was able to act for the whole pull, which is
// what every test below assumes unless it is about a death.
const survived = time.Duration(0)

func judgedFire() knowledge.Knowledge {
	return knowledge.Knowledge{
		JudgedCooldowns: map[int]knowledge.JudgedCooldown{
			combustion: {Name: "Combustion", Base: 120},
		},
	}
}

// timelineOfUses builds a pull in which one ability was cast at the given
// offsets and nothing else happened. The player is taken to have engaged when
// they first cast, which for this fixture is the first of those offsets —
// fightingFrom puts a filler cast in front when a test needs the two to
// differ.
func timelineOfUses(fight time.Duration, ability int, at ...time.Duration) *Timeline {
	t := &Timeline{Duration: fight}
	for _, offset := range at {
		t.Casts = append(t.Casts, Cast{AbilityID: ability, Name: "Combustion", Offset: offset, End: offset})
	}
	return t
}

// fightingFrom puts a filler cast at the front, so the pull has a player who
// engaged before the judged cooldown went out.
func fightingFrom(t *Timeline, at time.Duration) *Timeline {
	t.Casts = append([]Cast{{AbilityID: 133, Name: "Fireball", Offset: at, End: at}}, t.Casts...)
	return t
}

// The recorded wipe. The first Combustion came 37.8 s in — and the player had
// been casting since 0.6 s, so they were fighting the whole time rather than
// waiting to reach the boss.
func TestALateFirstUseIsItsOwnFinding(t *testing.T) {
	at := []time.Duration{37800, 111300, 181200}
	for i := range at {
		at[i] *= time.Millisecond
	}
	found := Findings(fightingFrom(timelineOfUses(239727*time.Millisecond, combustion, at...), 600*time.Millisecond), judgedFire(), survived)

	if len(found) != 1 {
		t.Fatalf("got %d findings, want 1 (the late opener; 3.5s of drift is below the floor): %+v", len(found), found)
	}
	if found[0].RuleID != ruleCooldownLate {
		t.Errorf("RuleID = %q, want %q", found[0].RuleID, ruleCooldownLate)
	}
	// It points at the moment they engaged, not at the late cast: that is
	// where a player looks to see what they did instead.
	if got := found[0].Timestamp(); got != "0:01" {
		t.Errorf("Timestamp() = %q, want 0:01 — the finding is about the opener", got)
	}
	if !strings.Contains(found[0].Detail, "should go out in the opener") {
		t.Errorf("Detail = %q, want it to say what should have happened", found[0].Detail)
	}
}

// A spec nobody has authored judges nothing, which is what makes the zero
// value safe to analyse with.
func TestAnUnauthoredSpecProducesNoFindings(t *testing.T) {
	at := []time.Duration{40 * time.Second, 200 * time.Second, 400 * time.Second}
	if found := Findings(timelineOfUses(500*time.Second, combustion, at...), knowledge.Knowledge{}, survived); len(found) != 0 {
		t.Errorf("got %d findings for a spec with no judged cooldowns, want none: %+v", len(found), found)
	}
}

// A clean pull produces nothing, and the page has to be able to say so
// rather than look broken.
func TestACleanPullProducesNoFindings(t *testing.T) {
	at := []time.Duration{2 * time.Second, 63 * time.Second, 124 * time.Second, 185 * time.Second}
	if found := Findings(timelineOfUses(240*time.Second, combustion, at...), judgedFire(), survived); len(found) != 0 {
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
	if found := Findings(nil, judgedFire(), survived); found != nil {
		t.Errorf("got %+v for a pull that could not be loaded, want nil", found)
	}
}

// A cooldown that came back after the player was dead is not one they
// declined to press. This is the second half of the same pull the phase test
// is about: the player died at 5:15, and Combustion came back at 5:54 — 39
// seconds into being dead. The page called that a full use left on the
// table.
func TestNothingIsAskedOfAPlayerAfterTheyDied(t *testing.T) {
	at := []time.Duration{11300, 76700, 138600, 231700, 292900}
	for i := range at {
		at[i] *= time.Millisecond
	}
	timeline := timelineOfUses(423000*time.Millisecond, combustion, at...)

	died := 315 * time.Second // 5:15
	for _, f := range Findings(timeline, judgedFire(), died) {
		if f.RuleID == ruleCooldownTail {
			t.Errorf("held a dead player to a cooldown that came back after they died: %q", f.Detail)
		}
	}

	// Alive to the end, the same pull really does leave one unused.
	var sawTail bool
	for _, f := range Findings(timeline, judgedFire(), survived) {
		sawTail = sawTail || f.RuleID == ruleCooldownTail
	}
	if !sawTail {
		t.Error("with the player alive to the end the unused tail is real and should be reported")
	}
}

// ActedUntil is the bound, and a player who lived has none beyond the pull.
func TestActedUntilIsTheDeathOrTheWholePull(t *testing.T) {
	lived := PlayerStats{FightDuration: 400 * time.Second}
	if got := lived.ActedUntil(); got != 400*time.Second {
		t.Errorf("ActedUntil() = %v for a survivor, want the whole pull", got)
	}
	died := PlayerStats{FightDuration: 400 * time.Second, Deaths: 1, DiedAt: 315 * time.Second}
	if got := died.ActedUntil(); got != 315*time.Second {
		t.Errorf("ActedUntil() = %v, want the death at 5:15", got)
	}
	// A death the table gave no usable timestamp for must not silently
	// shorten the pull to nothing.
	odd := PlayerStats{FightDuration: 400 * time.Second, Deaths: 1}
	if got := odd.ActedUntil(); got != 400*time.Second {
		t.Errorf("ActedUntil() = %v with no death time, want the whole pull", got)
	}
}

// A pull the player could not reach at the start is not one where they
// declined to press anything. Lateness is measured from their first cast, so
// someone who engaged at 0:30 and used the cooldown at 0:38 was eight seconds
// late — not thirty-eight — and is not reported.
func TestEngagingLateIsNotTheSameAsOpeningLate(t *testing.T) {
	at := []time.Duration{38 * time.Second, 100 * time.Second, 162 * time.Second}
	timeline := fightingFrom(timelineOfUses(300*time.Second, combustion, at...), 30*time.Second)

	for _, f := range Findings(timeline, judgedFire(), survived) {
		if f.RuleID == ruleCooldownLate {
			t.Errorf("a player who only reached the fight at 0:30 was called late: %q", f.Detail)
		}
	}

	// The same cast times, from a player who was fighting from the start.
	timeline = fightingFrom(timelineOfUses(300*time.Second, combustion, at...), time.Second)
	var late bool
	for _, f := range Findings(timeline, judgedFire(), survived) {
		late = late || f.RuleID == ruleCooldownLate
	}
	if !late {
		t.Error("a player fighting from 0:01 who held the cooldown to 0:38 should be told so")
	}
}

package warcraftlogs

import (
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
// offsets and nothing else happened.
func timelineOfUses(fight time.Duration, ability int, at ...time.Duration) *Timeline {
	t := &Timeline{Duration: fight}
	for _, offset := range at {
		t.Casts = append(t.Casts, Cast{AbilityID: ability, Name: "Combustion", Offset: offset, End: offset})
	}
	return t
}

// A spec nobody has authored judges nothing, which is what makes the zero
// value safe to analyse with.
func TestAnUnauthoredSpecProducesNoFindings(t *testing.T) {
	at := []time.Duration{40 * time.Second, 200 * time.Second, 400 * time.Second}
	if found := Analyse(timelineOfUses(500*time.Second, combustion, at...), knowledge.Knowledge{}, PlayerContext{ActedUntil: survived}).Findings; len(found) != 0 {
		t.Errorf("got %d findings for a spec with no judged cooldowns, want none: %+v", len(found), found)
	}
}

// A clean pull produces nothing, and the page has to be able to say so
// rather than look broken.
func TestACleanPullProducesNoFindings(t *testing.T) {
	at := []time.Duration{2 * time.Second, 63 * time.Second, 124 * time.Second, 185 * time.Second}
	if found := Analyse(timelineOfUses(240*time.Second, combustion, at...), judgedFire(), PlayerContext{ActedUntil: survived}).Findings; len(found) != 0 {
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
	if found := Analyse(nil, judgedFire(), PlayerContext{ActedUntil: survived}).Findings; found != nil {
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
	for _, f := range Analyse(timeline, judgedFire(), PlayerContext{ActedUntil: died}).Findings {
		if f.RuleID == ruleCooldownTail {
			t.Errorf("held a dead player to a cooldown that came back after they died: %q", f.Detail)
		}
	}

	// Alive to the end, the same pull really does leave one unused.
	var sawTail bool
	for _, f := range Analyse(timeline, judgedFire(), PlayerContext{ActedUntil: survived}).Findings {
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

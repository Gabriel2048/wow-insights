package warcraftlogs

import (
	"slices"
	"strings"
	"testing"
	"time"

	"wowinsight/internal/knowledge"
)

// **The invariant the Check type exists for.** A page that shows only findings
// cannot be read when there are none: "Nothing to flag" on a 19th-percentile
// parse ending in a death is indistinguishable from "the rules never ran".
// Every rule therefore returns a Check alongside its findings, and the
// signature is what enforces it — there is no way to produce a finding that
// does not appear in the list of what was asked.
func TestEveryFindingComesFromACheckThatRan(t *testing.T) {
	for _, c := range []struct {
		name string
		know knowledge.Knowledge
	}{
		{"the authored spec", fire},
		{"a spec nobody has authored", knowledge.Knowledge{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rep, fight := recordedKill(t)
			tl := buildTimeline(rep, rep.Casts.Data, fight, c.know)
			a := Analyse(tl, c.know, PlayerContext{ActedUntil: tl.Duration})

			if len(a.Checks) == 0 {
				t.Fatal("no checks ran at all, so the page would be blank with nothing to say why")
			}
			for _, f := range a.Findings {
				i := slices.IndexFunc(a.Checks, func(ch Check) bool { return ch.RuleID == f.RuleID })
				if i < 0 {
					t.Errorf("the finding %q traces to no check; it would appear on a page that does not admit to having looked for it", f.RuleID)
					continue
				}
				if !a.Checks[i].Asked {
					t.Errorf("the check %q produced a finding while reporting it was never asked", f.RuleID)
				}
			}
			// And a check that found something says how many, so the panel and
			// the list above it cannot disagree about one rule.
			for _, ch := range a.Checks {
				want := 0
				for _, f := range a.Findings {
					if f.RuleID == ch.RuleID {
						want++
					}
				}
				if ch.Found != want {
					t.Errorf("check %q says it found %d, the list has %d", ch.RuleID, ch.Found, want)
				}
			}
		})
	}
}

// A check either says what it measured or says why it could not be asked.
// Neither is optional: a blank row is the "Nothing to flag" problem again,
// one level down.
func TestEveryCheckSaysWhatItMeasuredOrWhyItCouldNot(t *testing.T) {
	rep, fight := recordedKill(t)
	for _, c := range []struct {
		name string
		know knowledge.Knowledge
	}{{"the authored spec", fire}, {"a spec nobody has authored", knowledge.Knowledge{}}} {
		t.Run(c.name, func(t *testing.T) {
			tl := buildTimeline(rep, rep.Casts.Data, fight, c.know)
			for _, ch := range Analyse(tl, c.know, PlayerContext{ActedUntil: tl.Duration}).Checks {
				switch {
				case ch.Question == "":
					t.Errorf("check %q asks nothing", ch.RuleID)
				case ch.Asked && ch.Measured == "":
					t.Errorf("check %q was asked and reports no number", ch.RuleID)
				case !ch.Asked && ch.Unasked == "":
					t.Errorf("check %q was not asked and does not say why, which reads as a clean answer", ch.RuleID)
				}
			}
		})
	}
}

// A check states counts and never a verdict. Whether a pull was played well
// is the reader's conclusion to draw from numbers — the page puts the parse
// percentile on the same screen so the two can disagree in front of them.
func TestNoCheckPassesJudgement(t *testing.T) {
	rep, fight := recordedKill(t)
	tl := buildTimeline(rep, rep.Casts.Data, fight, fire)
	verdicts := []string{"good", "bad", "poor", "excellent", "clean", "wasted", "should", "mistake", "well played"}

	for _, ch := range Analyse(tl, fire, PlayerContext{ActedUntil: tl.Duration}).Checks {
		var said strings.Builder
		said.WriteString(strings.ToLower(ch.Question + " " + ch.Measured + " " + ch.Unasked))
		for _, e := range ch.Evidence {
			said.WriteString(" " + strings.ToLower(e.Label+" "+e.Value))
		}
		for _, verdict := range verdicts {
			if strings.Contains(said.String(), verdict) {
				t.Errorf("check %q says %q; a check counts and does not judge", ch.RuleID, verdict)
			}
		}
	}
}

// **The check that would have caught the Flamestrike bug.** A proc spent on a
// spell the tables have not authored looks exactly like a proc nobody spent,
// and before Flamestrike was authored seven Hot Streaks on the recorded wipe
// counted as unspent in a stretch where every one was spent correctly.
//
// Nothing unspent may remain except an aura that was still up when the pull
// ended, which was never going to be spent by anybody.
func TestNoProcIsUnspentExceptOnesStillUpWhenThePullEnded(t *testing.T) {
	for _, c := range []struct {
		name string
		load func(*testing.T) (*timelineReport, Fight)
	}{
		{"the kill", recordedKill},
		{"the wipe", recordedWipe},
	} {
		t.Run(c.name, func(t *testing.T) {
			rep, fight := c.load(t)
			tl := buildTimeline(rep, rep.Casts.Data, fight, fire)
			if len(tl.Procs) == 0 {
				t.Fatal("no procs were counted at all")
			}
			for _, l := range tl.Procs {
				if lost := l.Generated - l.Spent - l.StillUp; lost != 0 {
					t.Errorf("%s: %d of %d went neither spent nor held to the end — most likely the spell that spends it is not in CastRules",
						l.Name, lost, l.Generated)
				}
				if len(l.SpentOn) == 0 {
					t.Errorf("%s names no spell that spends it, so the count cannot be checked by a reader", l.Name)
				}
			}
		})
	}
}

// The game reports one effect under several ability ids — Hyperthermia is both
// 383874 and 1242220 — and auraWindows keys on the id. Counting those
// separately doubles the generated total and makes half of them look unspent.
func TestOneProcUnderTwoIdsIsCountedOnce(t *testing.T) {
	rep, fight := recordedKill(t)
	tl := buildTimeline(rep, rep.Casts.Data, fight, fire)

	var ids int
	for id, name := range fire.ProcAuras {
		if name == "Hyperthermia" {
			ids++
			_ = id
		}
	}
	if ids < 2 {
		t.Skip("Hyperthermia is no longer authored under two ids; this test is about that case")
	}
	i := slices.IndexFunc(tl.Procs, func(l ProcLedger) bool { return l.Name == "Hyperthermia" })
	if i < 0 {
		t.Fatal("no Hyperthermia in the ledger")
	}
	raw := 0
	for _, w := range auraWindows(rep.Procs.Data, fight, fire) {
		if w.name == "Hyperthermia" {
			raw++
		}
	}
	if tl.Procs[i].Generated >= raw {
		t.Errorf("%d counted from %d raw windows; two ids for one aura are not being merged", tl.Procs[i].Generated, raw)
	}
}

// A proc aura authored after the recording was made replays as an aura that
// never procced: the procs stream is filtered by ability id, and
// internal/fixture deliberately leaves filter variables out of the recording
// key, so the stale file is served and nothing goes red.
//
// This is the test that makes that loud. It passes today and fails the moment
// a fifth id is authored without a re-record.
func TestEveryAuthoredProcAuraAppearsInTheRecording(t *testing.T) {
	rep, fight := recordedKill(t)
	seen := map[int]bool{}
	for _, e := range rep.Procs.Data {
		seen[e.AbilityGameID] = true
	}
	wipe, wipeFight := recordedWipe(t)
	for _, e := range wipe.Procs.Data {
		seen[e.AbilityGameID] = true
	}
	_, _ = fight, wipeFight

	for _, id := range fire.ProcAuraIDs() {
		if !seen[id] {
			t.Errorf("aura %d (%s) is authored but appears in neither recording; the fixture predates the table, and it will replay as an aura that never procced rather than fail",
				id, fire.ProcAuras[id])
		}
	}
}

// Flamestrike spends a Hot Streak exactly as Pyroblast does. Without a rule
// for it every AoE cast reads as one with nothing behind it.
func TestFlamestrikeSpendsAHotStreak(t *testing.T) {
	rule, ok := fire.Rule(1254851)
	if !ok {
		t.Fatal("Flamestrike has no cast rule")
	}
	if !slices.Contains(rule.Instant, "Hot Streak!") {
		t.Errorf("Flamestrike's instant rule is %v, which does not name Hot Streak!", rule.Instant)
	}
	// And hard casting it is not a mistake, so nothing may mark one.
	if len(rule.HardCast) != 0 {
		t.Errorf("Flamestrike names %v as justifying a hard cast; hard casting it is simply how the spell works", rule.HardCast)
	}
	casts := []Cast{{AbilityID: 1254851, Name: "Flamestrike", Offset: time.Second, End: 3 * time.Second, CastTime: 2 * time.Second, HadBegincast: true}}
	classifyProcs(casts, nil, fire)
	if casts[0].ProcMissing {
		t.Error("a hard-cast Flamestrike is marked a cast that should not have been made")
	}
}

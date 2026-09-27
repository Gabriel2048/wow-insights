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

	// With comparisons that were made, so their wording is read too.
	who := comparedAgainst()
	who.ActedUntil = tl.Duration
	who.Cohorts[1] = CohortResult{Kind: TopPerformers, Cohort: who.Cohorts[0].Cohort}
	for _, ch := range Analyse(tl, fire, who).Checks {
		var said strings.Builder
		said.WriteString(strings.ToLower(ch.Question + " " + ch.Measured + " " + ch.Unasked + " " + ch.Note))
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
// spell the tables have not authored looks exactly like a proc nobody spent:
// before Flamestrike was authored, seven Hot Streaks on a recorded wipe
// counted as unspent in a stretch where every one was spent correctly.
//
// A proc may go unspent — it may expire. This test used to say otherwise,
// that nothing is unspent unless still up when the pull ends, and the raid
// night recorded for #81 falsified it honestly: three Pyroclasms ran out with
// only instant Pyroblasts inside them, one during a Combustion burst of ten
// in thirteen seconds. An instant Pyroblast does not spend Pyroclasm, so they
// lapsed. That is a thing a player does, not a thing the tables got wrong.
//
// So the claim is narrower and is the one that matters: **an unspent proc
// expired; it was not removed by a cast of a spell the tables do not know
// spends it.** A cast landing as the aura ends, of a spell no rule links to
// that aura, is a spender missing from CastRules. A cast of a spell the rules
// do link to it — an instant Pyroblast beside an expiring Pyroclasm — is the
// tables deliberately not crediting it, and is not flagged.
//
// "Expired" cannot be read off a window's length. Pyroclasm stacks to two and
// a new stack resets the whole buff, so a window can run past twenty seconds
// and still have expired; one on the new recording ran twenty-four. What is
// constant is the time from the last application or stack to the removal: an
// aura that expired ran its full duration from there, and one that was spent
// ended short of it.
//
// That is what keeps this from failing on a coincidence. Heat Shimmer lasts
// ten seconds, and on the recorded kill one ran exactly ten and expired on the
// same millisecond an instant Pyroblast landed. A cast that coincides with an
// aura running out its full duration did not spend it — so a cast is flagged
// only when the aura it sits beside ended early.
func TestAnUnspentProcExpiredRatherThanBeingSpentOnAnUnauthoredSpell(t *testing.T) {
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
				if len(l.SpentOn) == 0 {
					t.Errorf("%s names no spell that spends it, so the count cannot be checked by a reader", l.Name)
				}
			}

			// Which spells the tables know interact with each aura, in either
			// list. A cast of one of these beside an expiring aura is a cast
			// the rules chose not to credit, not a spender they are missing.
			known := map[string]map[int]bool{}
			for spell, rule := range fire.CastRules {
				for _, aura := range slices.Concat(rule.Instant, rule.HardCast) {
					if known[aura] == nil {
						known[aura] = map[int]bool{}
					}
					known[aura][spell] = true
				}
			}

			byName := map[string][]auraWindow{}
			for _, w := range auraWindows(rep.Procs.Data, fight, fire) {
				byName[w.name] = append(byName[w.name], w)
			}

			// ranFor is how long an aura had been running since it was last
			// applied, stacked or refreshed, at the moment it ended.
			ranFor := func(name string, w auraWindow) time.Duration {
				last := w.start
				for _, e := range rep.Procs.Data {
					if n, ok := fire.ProcAura(e.AbilityGameID); !ok || n != name {
						continue
					}
					switch e.Type {
					case "applybuff", "applybuffstack", "refreshbuff":
						at := time.Duration(e.Timestamp-fight.StartTime) * time.Millisecond
						if at >= w.start && at <= w.end && at > last {
							last = at
						}
					}
				}
				return w.end - last
			}

			lapsed := 0
			for name, windows := range byName {
				var unspent []auraWindow
				for _, w := range mergeWindows(windows) {
					if !spentIn(tl.Casts, w, name) && w.end < fight.Duration() {
						unspent = append(unspent, w)
					}
				}
				// The aura's full duration, as this pull shows it: the longest
				// any unspent one ran from its last application. An expiry
				// runs this long; anything well short of it was ended by
				// something. The blind spot is that the single longest one is
				// always excused — with Flamestrike's rule deleted this reports
				// two of the three Hot Streaks it spent, not three — which can
				// make a failure smaller but never turn one into a pass.
				var full time.Duration
				for _, w := range unspent {
					full = max(full, ranFor(name, w))
				}
				for _, w := range unspent {
					lapsed++
					if ranFor(name, w) >= full-250*time.Millisecond {
						continue // ran its full duration: it expired
					}
					for _, cast := range tl.Casts {
						landed := cast.End
						if landed < w.end-procSlack || landed > w.end+procSlack || known[name][cast.AbilityID] {
							continue
						}
						t.Errorf("%s ended at %s on a %s, a spell no cast rule links to %s — most likely a spender missing from CastRules",
							name, formatOffset(w.end), cast.Name, name)
					}
				}
			}
			t.Logf("%d procs lapsed unspent; each expired rather than being spent on something the tables do not know", lapsed)
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

// Flamestrike spends a Hot Streak exactly as Pyroblast does, and Pyroclasm
// improves a hard-cast one — but a hard cast without Pyroclasm is ordinary AoE,
// never a mistake.
//
// This test used to assert that Flamestrike's HardCast list was empty, which
// was a belief about the game and a wrong one: Pyroclasm does improve a
// hard-cast Flamestrike. What that assertion was protecting — that no AoE cast
// is marked one that should not have been made — is now held directly, by the
// HardCastNeedsProc flag and by the classification itself.
func TestFlamestrikeSpendsAHotStreakAndIsNeverAMistakeToHardCast(t *testing.T) {
	rule, ok := fire.Rule(1254851)
	if !ok {
		t.Fatal("Flamestrike has no cast rule")
	}
	if !slices.Contains(rule.Instant, "Hot Streak!") {
		t.Errorf("Flamestrike's instant rule is %v, which does not name Hot Streak!", rule.Instant)
	}
	if rule.HardCastNeedsProc {
		t.Error("a hard-cast Flamestrike is required to have a proc behind it; it is hard cast as ordinary AoE")
	}

	hardCast := func() []Cast {
		return []Cast{{AbilityID: 1254851, Name: "Flamestrike", Offset: time.Second, End: 3 * time.Second, CastTime: 2 * time.Second, HadBegincast: true}}
	}
	plain := hardCast()
	classifyProcs(plain, nil, fire)
	if plain[0].ProcMissing {
		t.Error("a hard-cast Flamestrike with nothing up is marked a cast that should not have been made")
	}

	improved := hardCast()
	classifyProcs(improved, []auraWindow{{name: "Pyroclasm", start: 0, end: 4 * time.Second}}, fire)
	if improved[0].Proc != "Pyroclasm" {
		t.Errorf("a hard-cast Flamestrike under Pyroclasm is labelled %q, want Pyroclasm", improved[0].Proc)
	}
}

// Pyroclasm is spent only by a hard cast. With Hot Streak and Pyroclasm both
// up, Pyroblast comes out instantly and Pyroclasm stays — so an instant
// Pyroblast must not claim it, and a hard-cast one with nothing up is still a
// cast that should not have been made.
func TestAnInstantPyroblastDoesNotSpendPyroclasm(t *testing.T) {
	rule, _ := fire.Rule(11366)
	if slices.Contains(rule.Instant, "Pyroclasm") {
		t.Error("an instant Pyroblast is said to spend Pyroclasm; only a hard cast does")
	}
	if !rule.HardCastNeedsProc {
		t.Error("a hard-cast Pyroblast with nothing behind it is no longer judged")
	}
	both := []auraWindow{{name: "Hot Streak!", start: 0, end: 2 * time.Second}, {name: "Pyroclasm", start: 0, end: 10 * time.Second}}
	casts := []Cast{{AbilityID: 11366, Name: "Pyroblast", Offset: time.Second, End: time.Second}}
	classifyProcs(casts, both, fire)
	if strings.Contains(casts[0].Proc, "Pyroclasm") {
		t.Errorf("an instant Pyroblast is labelled %q", casts[0].Proc)
	}
}

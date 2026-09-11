package warcraftlogs

import (
	"testing"
	"time"
)

// fightAt builds a fight starting at 1000ms and running for the given seconds.
func fightAt(seconds float64) Fight {
	return Fight{ID: 1, StartTime: 1000, EndTime: 1000 + seconds*1000}
}

func TestLustWindowsIgnoresNonRaidWideBuffs(t *testing.T) {
	fight := fightAt(100)
	names := map[int]string{80353: "Time Warp", 390386: "Fury of the Aspects"}
	actors := map[int]string{21: "Testmage"}

	events := []event{}
	// A real raid lust: 22 players, all buffed at 10s for 40s.
	for target := 1; target <= 22; target++ {
		events = append(events,
			event{Timestamp: 11000, Type: "applybuff", AbilityGameID: 80353, SourceID: 21, TargetID: target},
			event{Timestamp: 51000, Type: "removebuff", AbilityGameID: 80353, SourceID: 21, TargetID: target},
		)
	}
	// A personal effect reusing a lust spell ID, on one player only.
	events = append(events,
		event{Timestamp: 5000, Type: "applybuff", AbilityGameID: 390386, SourceID: 38, TargetID: 38},
		event{Timestamp: 20000, Type: "removebuff", AbilityGameID: 390386, SourceID: 38, TargetID: 38},
	)

	windows := lustWindows(events, fight, names, actors)
	if len(windows) != 1 {
		t.Fatalf("got %d windows, want 1 (the personal buff must be excluded)", len(windows))
	}
	w := windows[0]
	if w.Name != "Time Warp" {
		t.Errorf("Name = %q, want Time Warp", w.Name)
	}
	if w.Source != "Testmage" {
		t.Errorf("Source = %q, want Testmage", w.Source)
	}
	if w.Start != 10*time.Second || w.Duration() != 40*time.Second {
		t.Errorf("window = %v..%v, want 10s..50s", w.Start, w.End)
	}
	if w.Targets != 22 {
		t.Errorf("Targets = %d, want 22", w.Targets)
	}
}

func TestLustWindowsMergesPerPlayerIntervals(t *testing.T) {
	fight := fightAt(300)
	var events []event
	// Same lust, but each player's buff ticks off at a slightly different
	// moment; these must collapse to a single window, not 10.
	for target := 1; target <= 10; target++ {
		offset := float64(target * 50)
		events = append(events,
			event{Timestamp: 11000 + offset, Type: "applybuff", AbilityGameID: 2825, TargetID: target},
			event{Timestamp: 51000 + offset, Type: "removebuff", AbilityGameID: 2825, TargetID: target},
		)
	}
	windows := lustWindows(events, fight, map[int]string{2825: "Bloodlust"}, nil)
	if len(windows) != 1 {
		t.Fatalf("got %d windows, want 1", len(windows))
	}
}

func TestLustWindowStillUpAtFightEnd(t *testing.T) {
	fight := fightAt(30)
	var events []event
	for target := 1; target <= 10; target++ {
		events = append(events, event{Timestamp: 21000, Type: "applybuff", AbilityGameID: 2825, TargetID: target})
	}
	windows := lustWindows(events, fight, map[int]string{2825: "Bloodlust"}, nil)
	if len(windows) != 1 {
		t.Fatalf("got %d windows, want 1", len(windows))
	}
	if windows[0].End != 30*time.Second {
		t.Errorf("End = %v, want the fight end (30s)", windows[0].End)
	}
}

func TestBuildCastsGapsAndLustOverlap(t *testing.T) {
	fight := fightAt(100)
	lusts := []RaidWindow{{Start: 10 * time.Second, End: 50 * time.Second}}
	events := []event{
		{Timestamp: 6000, AbilityGameID: 1, Type: "cast"},  // 5s in
		{Timestamp: 12000, AbilityGameID: 2, Type: "cast"}, // 11s, 6s gap, in lust
		{Timestamp: 13000, AbilityGameID: 3, Type: "cast"}, // 12s, 1s gap, in lust
	}
	casts := buildCasts(events, fight, map[int]string{1: "A", 2: "B", 3: "C"}, lusts, fire)
	if len(casts) != 3 {
		t.Fatalf("got %d casts, want 3", len(casts))
	}
	if casts[0].Gap != 5*time.Second {
		t.Errorf("first gap = %v, want 5s (measured from the pull)", casts[0].Gap)
	}
	if casts[1].Gap != 6*time.Second {
		t.Errorf("second cast Gap = %v, want 6s", casts[1].Gap)
	}
	if casts[2].Gap != time.Second {
		t.Errorf("third cast Gap = %v, want 1s", casts[2].Gap)
	}
	if got := casts[1].GapMS(); got != 6000 {
		t.Errorf("GapMS() = %d, want 6000", got)
	}
	if casts[0].DuringLust {
		t.Errorf("cast at 5s is before the lust window")
	}
	if !casts[1].DuringLust || !casts[2].DuringLust {
		t.Errorf("casts at 11s and 12s are inside the lust window")
	}
	// Positions are internal/view's business, not this package's.
}

func TestUnknownAbilityFallsBackToID(t *testing.T) {
	casts := buildCasts([]event{{Timestamp: 2000, AbilityGameID: 999, Type: "cast"}}, fightAt(10), nil, nil, fire)
	if casts[0].Name != "Spell 999" {
		t.Errorf("Name = %q, want %q", casts[0].Name, "Spell 999")
	}
}

func TestBuildPhases(t *testing.T) {
	// A 300s fight starting at 1000ms, with transitions matching the shape of
	// real data: phase 1 logged a fraction before the pull.
	fight := fightAt(300)
	transitions := []phaseTransition{
		{ID: 1, StartTime: 700}, // 0.3s before the fight start
		{ID: 2, StartTime: 121000},
		{ID: 3, StartTime: 221000},
	}
	labels := map[int]phaseLabel{
		1: {Name: "Stage One: Serpent's Bargain"},
		2: {Name: "Stage Two: Usurper's Reprisal"},
		3: {Name: "Intermission: The Claimed Vessel", IsIntermission: true},
	}
	phases := buildPhases(transitions, labels, fight)
	if len(phases) != 3 {
		t.Fatalf("got %d phases, want 3", len(phases))
	}
	if phases[0].Start != 0 {
		t.Errorf("first phase starts at %v, want 0 (a pre-pull transition must clamp)", phases[0].Start)
	}
	if phases[0].End != 120*time.Second {
		t.Errorf("first phase ends at %v, want 120s (the next transition)", phases[0].End)
	}
	if phases[2].End != 300*time.Second {
		t.Errorf("last phase ends at %v, want the fight end (300s)", phases[2].End)
	}
	if !phases[2].IsIntermission {
		t.Errorf("phase 3 should be flagged as an intermission")
	}
	if got := phases[1].ShortName(); got != "Stage Two" {
		t.Errorf("ShortName() = %q, want %q", got, "Stage Two")
	}
	// Phases must tile the fight with no gaps.
	for i := 1; i < len(phases); i++ {
		if phases[i].Start != phases[i-1].End {
			t.Errorf("phase %d starts at %v but the previous ended at %v", i, phases[i].Start, phases[i-1].End)
		}
	}
}

func TestBuildPhasesUnnamedAndEmpty(t *testing.T) {
	if got := buildPhases(nil, nil, fightAt(100)); got != nil {
		t.Errorf("no transitions should yield no phases, got %d", len(got))
	}
	phases := buildPhases([]phaseTransition{{ID: 7, StartTime: 1000}}, nil, fightAt(100))
	if len(phases) != 1 || phases[0].Name != "Phase 7" {
		t.Errorf("unnamed phase = %+v, want a Phase 7 fallback", phases)
	}
}

func TestSectionsGroupCastsByPhase(t *testing.T) {
	tl := &Timeline{
		Duration: 300 * time.Second,
		Phases: []Phase{
			{ID: 1, Name: "One", Start: 0, End: 100 * time.Second},
			{ID: 2, Name: "Two", Start: 100 * time.Second, End: 200 * time.Second},
			{ID: 3, Name: "Three", Start: 200 * time.Second, End: 300 * time.Second},
		},
		Casts: []Cast{
			{Offset: 5 * time.Second},
			{Offset: 99 * time.Second},
			{Offset: 100 * time.Second}, // exactly on the boundary -> phase two
			{Offset: 250 * time.Second},
		},
	}
	sections := tl.Sections()
	if len(sections) != 3 {
		t.Fatalf("got %d sections, want 3", len(sections))
	}
	want := []int{2, 1, 1}
	for i, n := range want {
		if got := len(sections[i].Casts); got != n {
			t.Errorf("section %d has %d casts, want %d", i, got, n)
		}
	}
	// Every cast must land somewhere.
	total := 0
	for _, s := range sections {
		total += len(s.Casts)
	}
	if total != len(tl.Casts) {
		t.Errorf("sections hold %d casts, want all %d", total, len(tl.Casts))
	}
}

func TestSectionsWithoutPhaseData(t *testing.T) {
	tl := &Timeline{Casts: []Cast{{Offset: time.Second}, {Offset: 2 * time.Second}}}
	sections := tl.Sections()
	if len(sections) != 1 || len(sections[0].Casts) != 2 {
		t.Fatalf("want one section holding both casts, got %+v", sections)
	}
	if sections[0].Phase.Name != "" {
		t.Errorf("the implicit section should be unnamed, got %q", sections[0].Phase.Name)
	}
}

// The events below are real Fire Mage casts from a Heroic raid pull,
// between 50s and 69s, with timestamps shifted to a fight starting at 1000ms.
// They cover every case that matters: a hard cast interleaved with off-GCD
// instants, a proc that resolves in 0ms, and a cast that never lands.
func fireMageEvents() []event {
	const (
		fireBlast      = 108853
		pyroblast      = 11366
		scorch         = 2948
		shimmer        = 212653
		blazingBarrier = 235313
	)
	raw := []struct {
		ms      float64
		typ     string
		ability int
	}{
		{50761, "cast", shimmer},
		{52554, "begincast", pyroblast}, // 1.9s hard cast, lands at 54465
		{53744, "cast", fireBlast},      // off-GCD, fired mid-cast
		{54248, "cast", fireBlast},
		{54465, "cast", pyroblast},
		{54465, "begincast", pyroblast}, // Hot Streak: begincast and cast together
		{54466, "cast", pyroblast},
		{55931, "begincast", scorch},
		{55931, "cast", scorch},
		{57053, "begincast", pyroblast},
		{57053, "cast", pyroblast},
		{58125, "begincast", scorch}, // never completes
		{59700, "cast", blazingBarrier},
		{61089, "cast", 342245},
		{68386, "cast", 342245},
	}
	events := make([]event, len(raw))
	for i, r := range raw {
		events[i] = event{Timestamp: 1000 + r.ms, Type: r.typ, AbilityGameID: r.ability}
	}
	return events
}

func TestBuildCastsPairsBegincastWithCast(t *testing.T) {
	names := map[int]string{
		11366: "Pyroblast", 108853: "Fire Blast", 2948: "Scorch",
		212653: "Shimmer", 235313: "Blazing Barrier", 342245: "Alter Time",
	}
	casts := buildCasts(fireMageEvents(), fightAt(100), names, nil, fire)

	// 15 events collapse to 11 casts.
	if len(casts) != 11 {
		t.Fatalf("got %d casts from 15 events, want 11", len(casts))
	}

	find := func(name string, nth int) Cast {
		t.Helper()
		seen := 0
		for _, c := range casts {
			if c.Name == name {
				if seen == nth {
					return c
				}
				seen++
			}
		}
		t.Fatalf("no %s #%d among %d casts", name, nth, len(casts))
		return Cast{}
	}

	// The 1.9s hard-cast Pyroblast is one row carrying its cast time.
	hard := find("Pyroblast", 0)
	if hard.CastTime != 1911*time.Millisecond {
		t.Errorf("hard Pyroblast CastTime = %v, want 1.911s", hard.CastTime)
	}
	if hard.Cancelled || !hard.HadBegincast {
		t.Errorf("hard Pyroblast should be a completed cast bar, got %+v", hard)
	}
	if got := hard.CastLabel(); got != "1.9s" {
		t.Errorf("CastLabel() = %q, want 1.9s", got)
	}

	// A proc resolves in 0ms but still had a cast bar, so it is not "instant".
	proc := find("Pyroblast", 1)
	if proc.CastTime != time.Millisecond || !proc.HadBegincast {
		t.Errorf("Hot Streak Pyroblast = %+v, want a ~0ms cast bar", proc)
	}
	if got := proc.CastLabel(); got != "0.0s" {
		t.Errorf("proc CastLabel() = %q, want 0.0s", got)
	}

	// Fire Blast never has a cast bar at all.
	if got := find("Fire Blast", 0).CastLabel(); got != "instant" {
		t.Errorf("Fire Blast CastLabel() = %q, want instant", got)
	}

	// The 0:58 Scorch started and never landed.
	cancelled := find("Scorch", 1)
	if !cancelled.Cancelled {
		t.Errorf("second Scorch should be cancelled, got %+v", cancelled)
	}
	if got := cancelled.CastLabel(); got != "cancelled" {
		t.Errorf("cancelled CastLabel() = %q, want cancelled", got)
	}
}

func TestBuildCastsGapExcludesCastTimeAndOverlap(t *testing.T) {
	names := map[int]string{11366: "Pyroblast", 108853: "Fire Blast", 342245: "Alter Time"}
	casts := buildCasts(fireMageEvents(), fightAt(100), names, nil, fire)

	byStart := map[string]Cast{}
	for _, c := range casts {
		key := formatOffset(c.Offset) + " " + c.Name
		if _, seen := byStart[key]; !seen {
			byStart[key] = c
		}
	}

	// Fire Blast is woven into the Pyroblast cast, so it is not a fresh pause.
	for _, c := range casts {
		if c.Name == "Fire Blast" && c.Gap != 0 {
			t.Errorf("off-GCD Fire Blast at %v has Gap %v, want 0 (it overlaps the Pyroblast cast)", c.Offset, c.Gap)
		}
	}

	// Above a GCD-sized threshold, the only genuine pause within the slice is
	// the 7.3s before the last cast. The first cast is skipped: its gap is
	// measured from the pull, and this slice starts 50s into the fight.
	const threshold = 2 * time.Second
	var downtime []Cast
	for _, c := range casts[1:] {
		if c.Gap >= threshold {
			downtime = append(downtime, c)
		}
	}
	if len(downtime) != 1 {
		t.Fatalf("got %d downtime casts, want 1: %+v", len(downtime), downtime)
	}
	if got := downtime[0].Gap.Round(100 * time.Millisecond); got != 7300*time.Millisecond {
		t.Errorf("downtime gap = %v, want 7.3s", got)
	}

	// Idle time can never exceed the elapsed span. Counting a cast bar as a
	// pause, or double-counting an overlapping off-GCD cast, would break this.
	var total time.Duration
	for _, c := range casts[1:] {
		total += c.Gap
	}
	span := casts[len(casts)-1].End - casts[0].End
	if total > span {
		t.Errorf("gaps total %v across a %v span; idle time cannot exceed elapsed time", total, span)
	}

	// Cast time is excluded from the gaps, so the hard-cast Pyroblast's 1.9s
	// must be absent from the idle total.
	var castTime time.Duration
	for _, c := range casts {
		castTime += c.CastTime
	}
	if castTime == 0 {
		t.Fatal("expected some cast time in this slice")
	}
	if total+castTime > span+time.Second {
		t.Errorf("gaps (%v) plus cast time (%v) overshoot the %v span", total, castTime, span)
	}
}

func TestPrecastDetection(t *testing.T) {
	const (
		pyroblast = 11366
		fireBlast = 108853
	)
	names := map[int]string{pyroblast: "Pyroblast", fireBlast: "Fire Blast"}

	// A real pull opening: a Pyroblast lands at +0.168s with no begincast
	// because the cast bar started before the fight, followed by an ordinary
	// instant and a hard cast.
	events := []event{
		{Timestamp: 1168, Type: "cast", AbilityGameID: pyroblast},
		{Timestamp: 1750, Type: "cast", AbilityGameID: fireBlast},
		{Timestamp: 2486, Type: "begincast", AbilityGameID: pyroblast},
		{Timestamp: 3986, Type: "cast", AbilityGameID: pyroblast},
	}
	casts := buildCasts(events, fightAt(100), names, nil, fire)
	if len(casts) != 3 {
		t.Fatalf("got %d casts, want 3", len(casts))
	}
	if !casts[0].Precast {
		t.Errorf("the opening Pyroblast should be flagged as a precast, got %+v", casts[0])
	}
	if got := casts[0].CastLabel(); got != "precast" {
		t.Errorf("CastLabel() = %q, want precast", got)
	}
	// Fire Blast has no cast bar anywhere, so it stays a genuine instant.
	if casts[1].Precast {
		t.Errorf("Fire Blast must not be treated as a precast")
	}
	if got := casts[1].CastLabel(); got != "instant" {
		t.Errorf("Fire Blast CastLabel() = %q, want instant", got)
	}
	// The later hard cast is unaffected.
	if got := casts[2].CastLabel(); got != "1.5s" {
		t.Errorf("hard cast CastLabel() = %q, want 1.5s", got)
	}
}

func TestPrecastOnlyNearThePull(t *testing.T) {
	const scorch = 2948
	// An unpaired castable spell well into the fight is a gap in the log, not
	// a precast; nobody starts a cast bar 60s before it lands.
	events := []event{
		{Timestamp: 1000, Type: "begincast", AbilityGameID: scorch},
		{Timestamp: 2000, Type: "cast", AbilityGameID: scorch},
		{Timestamp: 61000, Type: "cast", AbilityGameID: scorch},
	}
	casts := buildCasts(events, fightAt(100), map[int]string{scorch: "Scorch"}, nil, fire)
	if casts[1].Precast {
		t.Errorf("an unpaired cast %v into the fight must not be a precast", casts[1].Offset)
	}
}

func TestDuringCastMarksWovenCasts(t *testing.T) {
	names := map[int]string{
		11366: "Pyroblast", 108853: "Fire Blast", 212653: "Shimmer", 2948: "Scorch",
	}
	casts := buildCasts(fireMageEvents(), fightAt(100), names, nil, fire)

	woven := map[string]int{}
	for _, c := range casts {
		if c.DuringCast {
			woven[c.Name]++
		}
	}
	// Both Fire Blasts fall inside the 52.554-54.465 Pyroblast cast bar.
	if woven["Fire Blast"] != 2 {
		t.Errorf("got %d woven Fire Blasts, want 2: %v", woven["Fire Blast"], woven)
	}
	if len(woven) != 1 {
		t.Errorf("only Fire Blast should be woven here, got %v", woven)
	}

	// The Pyroblast that encloses them is not itself woven.
	for _, c := range casts {
		if c.Name == "Pyroblast" && c.CastTime > time.Second && c.DuringCast {
			t.Errorf("the enclosing hard cast must not be marked woven: %+v", c)
		}
	}
}

func TestDuringCastBoundariesAndNesting(t *testing.T) {
	const hard, instant = 1, 2
	names := map[int]string{hard: "Fireball", instant: "Fire Blast"}
	events := []event{
		{Timestamp: 1000, Type: "begincast", AbilityGameID: hard}, // 0.0s -> 2.0s
		{Timestamp: 1500, Type: "cast", AbilityGameID: instant},   // 0.5s, inside
		{Timestamp: 1900, Type: "cast", AbilityGameID: instant},   // 0.9s, inside
		{Timestamp: 3000, Type: "cast", AbilityGameID: hard},      // the Fireball lands
		{Timestamp: 3000, Type: "cast", AbilityGameID: instant},   // exactly at the end
	}
	casts := buildCasts(events, fightAt(100), names, nil, fire)

	var woven, notWoven int
	for _, c := range casts {
		if c.Name != "Fire Blast" {
			continue
		}
		if c.DuringCast {
			woven++
		} else {
			notWoven++
		}
	}
	// Two inside; the one landing exactly as the cast ends is back-to-back.
	if woven != 2 || notWoven != 1 {
		t.Errorf("got %d woven and %d not, want 2 and 1", woven, notWoven)
	}
}

func TestRepeatsPreviousSkipsOnlyTheCollidingLabel(t *testing.T) {
	const pyro, fireBlast, scorch = 11366, 108853, 2948
	names := map[int]string{pyro: "Pyroblast", fireBlast: "Fire Blast", scorch: "Scorch"}

	// A real cast shape: a 1.9s Pyroblast with two Fire Blasts woven in, then
	// an instant Pyroblast starting the moment the bar ends.
	events := []event{
		{Timestamp: 53554, Type: "begincast", AbilityGameID: pyro},
		{Timestamp: 54744, Type: "cast", AbilityGameID: fireBlast},
		{Timestamp: 55248, Type: "cast", AbilityGameID: fireBlast},
		{Timestamp: 55465, Type: "cast", AbilityGameID: pyro},
		{Timestamp: 55465, Type: "begincast", AbilityGameID: pyro},
		{Timestamp: 55466, Type: "cast", AbilityGameID: pyro},
	}
	casts := buildCasts(events, Fight{ID: 1, StartTime: 1000, EndTime: 101000}, names, nil, fire)

	var repeats []Cast
	for _, c := range casts {
		if c.RepeatsPrevious {
			repeats = append(repeats, c)
		}
	}
	if len(repeats) != 1 {
		t.Fatalf("got %d repeats, want 1: %+v", len(repeats), repeats)
	}
	if repeats[0].Name != "Pyroblast" || repeats[0].CastTime > time.Millisecond {
		t.Errorf("the repeat should be the instant Pyroblast, got %+v", repeats[0])
	}
	// The woven Fire Blasts must not break the comparison between the two
	// Pyroblasts, and must not themselves be marked.
	for _, c := range casts {
		if c.Name == "Fire Blast" && c.RepeatsPrevious {
			t.Errorf("a woven cast must never be marked a repeat: %+v", c)
		}
	}

	// A different spell following the bar keeps its label.
	events[3] = event{Timestamp: 55465, Type: "cast", AbilityGameID: pyro}
	events = append(events[:4], event{Timestamp: 55600, Type: "cast", AbilityGameID: scorch})
	for _, c := range buildCasts(events, Fight{ID: 1, StartTime: 1000, EndTime: 101000}, names, nil, fire) {
		if c.Name == "Scorch" && c.RepeatsPrevious {
			t.Errorf("a different spell must keep its label: %+v", c)
		}
	}
}

func TestClassifyProcsPrefersTheExplainingAura(t *testing.T) {
	// Hot Streak and Pyroclasm overlap. Which one explains the cast depends on
	// whether it was instant or hard.
	windows := []auraWindow{
		{name: "Hot Streak!", start: 0, end: 30 * time.Second},
		{name: "Pyroclasm", start: 0, end: 30 * time.Second},
	}
	casts := []Cast{
		{AbilityID: pyroblastID, Name: "Pyroblast", Offset: 5 * time.Second, CastTime: 0},
		{AbilityID: pyroblastID, Name: "Pyroblast", Offset: 10 * time.Second, CastTime: 1900 * time.Millisecond},
	}
	classifyProcs(casts, windows, fire)
	if casts[0].Proc != "Hot Streak!" {
		t.Errorf("instant Pyroblast Proc = %q, want Hot Streak!", casts[0].Proc)
	}
	if casts[1].Proc != "Pyroclasm" {
		t.Errorf("hard-cast Pyroblast Proc = %q, want Pyroclasm", casts[1].Proc)
	}
	for _, c := range casts {
		if c.ProcMissing {
			t.Errorf("neither cast lacked a proc: %+v", c)
		}
	}
}

func TestClassifyProcsFlagsUnjustifiedHardCast(t *testing.T) {
	casts := []Cast{
		{AbilityID: pyroblastID, Name: "Pyroblast", Offset: 10 * time.Second, CastTime: 1900 * time.Millisecond},
		{AbilityID: 108853, Name: "Fire Blast", Offset: 12 * time.Second},
		{AbilityID: pyroblastID, Name: "Pyroblast", Offset: 14 * time.Second, Precast: true},
		{AbilityID: pyroblastID, Name: "Pyroblast", Offset: 16 * time.Second, Cancelled: true, CastTime: 0},
	}
	classifyProcs(casts, nil, fire)
	if !casts[0].ProcMissing {
		t.Errorf("a hard cast with no aura up must be flagged")
	}
	if got := casts[0].ProcLabel(); got != "no proc" {
		t.Errorf("ProcLabel() = %q, want %q", got, "no proc")
	}
	// Other spells, precasts and cancels are never judged.
	for _, c := range casts[1:] {
		if c.ProcMissing || c.Proc != "" {
			t.Errorf("%s at %v should not be classified: %+v", c.Name, c.Offset, c)
		}
	}
}

func TestAuraWindowsPairsAndClosesAtFightEnd(t *testing.T) {
	fight := fightAt(100)
	events := []event{
		{Timestamp: 3000, Type: "applybuff", AbilityGameID: 48108},
		{Timestamp: 5000, Type: "removebuff", AbilityGameID: 48108},
		{Timestamp: 7000, Type: "applybuff", AbilityGameID: 269651},
		{Timestamp: 8000, Type: "applybuffstack", AbilityGameID: 269651}, // ignored
		{Timestamp: 9000, Type: "applybuff", AbilityGameID: 48108},       // never removed
	}
	// One closed Hot Streak, plus a Pyroclasm and a second Hot Streak that are
	// both still up when the fight ends.
	windows := auraWindows(events, fight, fire)
	if len(windows) != 3 {
		t.Fatalf("got %d windows, want 3: %+v", len(windows), windows)
	}
	var closed, open int
	for _, w := range windows {
		if w.end == 100*time.Second {
			open++
			continue
		}
		closed++
		if w.name != "Hot Streak!" || w.start != 2*time.Second || w.end != 4*time.Second {
			t.Errorf("closed window = %+v, want Hot Streak! 2s..4s", w)
		}
	}
	if closed != 1 || open != 2 {
		t.Errorf("got %d closed and %d open windows, want 1 and 2", closed, open)
	}
	// The stack event must not open a window of its own.
	var pyroclasm int
	for _, w := range windows {
		if w.name == "Pyroclasm" {
			pyroclasm++
		}
	}
	if pyroclasm != 1 {
		t.Errorf("got %d Pyroclasm windows, want 1 (applybuffstack is ignored)", pyroclasm)
	}
}

// Hyperthermia makes Pyroblast instant and ramps its damage, but Hot Streak is
// still consumed underneath it, so both belong on the cast. Pyroclasm is not
// consumed under Hyperthermia and does not explain an instant.
func TestClassifyProcsUnderHyperthermia(t *testing.T) {
	full := []auraWindow{
		{name: "Hyperthermia", start: 0, end: 30 * time.Second},
		{name: "Hot Streak!", start: 0, end: 30 * time.Second},
		{name: "Pyroclasm", start: 0, end: 30 * time.Second},
	}
	casts := []Cast{{AbilityID: pyroblastID, Name: "Pyroblast", Offset: 5 * time.Second}}
	classifyProcs(casts, full, fire)
	if casts[0].Proc != "Hyperthermia + Hot Streak!" {
		t.Errorf("Proc = %q, want %q", casts[0].Proc, "Hyperthermia + Hot Streak!")
	}

	// Hyperthermia alone still explains the instant.
	onlyHyper := []auraWindow{
		{name: "Hyperthermia", start: 0, end: 30 * time.Second},
		{name: "Pyroclasm", start: 0, end: 30 * time.Second},
	}
	casts = []Cast{{AbilityID: pyroblastID, Name: "Pyroblast", Offset: 5 * time.Second}}
	classifyProcs(casts, onlyHyper, fire)
	if casts[0].Proc != "Hyperthermia" {
		t.Errorf("Proc = %q, want Hyperthermia (Pyroclasm never explains an instant)", casts[0].Proc)
	}

	// A hard cast is judged on Pyroclasm alone.
	casts = []Cast{{AbilityID: pyroblastID, Name: "Pyroblast", Offset: 5 * time.Second, CastTime: 1900 * time.Millisecond}}
	classifyProcs(casts, full, fire)
	if casts[0].Proc != "Pyroclasm" {
		t.Errorf("hard cast Proc = %q, want Pyroclasm", casts[0].Proc)
	}
}

// Pyroclasm boosts the Pyroblast that lands, so a buff that runs out during the
// cast leaves the spell hitting for normal damage. This does not occur in the
// reference log, so it is only covered here.
func TestClassifyProcsPyroclasmExpiringMidCast(t *testing.T) {
	newCast := func(start, castTime time.Duration) []Cast {
		return []Cast{{
			AbilityID: pyroblastID, Name: "Pyroblast",
			Offset: start, End: start + castTime, CastTime: castTime,
		}}
	}

	// Up at the start with 1s left, gone before the 1.9s cast completes.
	casts := newCast(10*time.Second, 1900*time.Millisecond)
	classifyProcs(casts, []auraWindow{{name: "Pyroclasm", start: 5 * time.Second, end: 11 * time.Second}}, fire)
	if casts[0].Proc != "" {
		t.Errorf("Proc = %q, want empty: the buff expired before impact", casts[0].Proc)
	}
	if casts[0].ProcExpired != "Pyroclasm" {
		t.Errorf("ProcExpired = %q, want Pyroclasm", casts[0].ProcExpired)
	}
	if got := casts[0].ProcLabel(); got != "Pyroclasm expired" {
		t.Errorf("ProcLabel() = %q, want %q", got, "Pyroclasm expired")
	}
	if casts[0].ProcMissing {
		t.Error("an expired proc is not the same as never having one")
	}

	// Surviving to impact still counts, even if it ends right afterwards.
	casts = newCast(10*time.Second, 1900*time.Millisecond)
	classifyProcs(casts, []auraWindow{{name: "Pyroclasm", start: 5 * time.Second, end: 12 * time.Second}}, fire)
	if casts[0].Proc != "Pyroclasm" || casts[0].ProcExpired != "" {
		t.Errorf("a buff lasting to impact should count: %+v", casts[0])
	}

	// Never up at all is still "no proc", not "expired".
	casts = newCast(10*time.Second, 1900*time.Millisecond)
	classifyProcs(casts, nil, fire)
	if !casts[0].ProcMissing || casts[0].ProcExpired != "" {
		t.Errorf("want ProcMissing with no expiry, got %+v", casts[0])
	}
}

// An aura beginning shortly after a cast cannot have enabled it. This is the
// 4:03 case from the reference log: a hard cast started 87ms before
// Hyperthermia was granted, and symmetric tolerance credited it wrongly.
func TestClassifyProcsIgnoresAuraStartingAfterTheCast(t *testing.T) {
	casts := []Cast{{AbilityID: pyroblastID, Name: "Pyroblast",
		Offset: 10 * time.Second, End: 10 * time.Second}}
	classifyProcs(casts, []auraWindow{
		{name: "Hyperthermia", start: 10*time.Second + 87*time.Millisecond, end: 20 * time.Second},
	}, fire)
	if casts[0].Proc != "" {
		t.Errorf("Proc = %q, want empty: the aura began after the cast", casts[0].Proc)
	}

	// An aura consumed by the cast still counts: its removebuff can land a
	// little after the cast that spent it.
	casts = []Cast{{AbilityID: pyroblastID, Name: "Pyroblast",
		Offset: 10 * time.Second, End: 10 * time.Second}}
	classifyProcs(casts, []auraWindow{
		{name: "Hot Streak!", start: 8 * time.Second, end: 10*time.Second + 20*time.Millisecond},
	}, fire)
	if casts[0].Proc != "Hot Streak!" {
		t.Errorf("Proc = %q, want Hot Streak!", casts[0].Proc)
	}
}

// Three Fireballs cast back to back, from a real log at 27-31s. Their bars are
// adjacent to the millisecond, so they read as one band; each must still be
// labelled or the timeline shows unexplained ticks inside a long bar.
func TestConsecutiveHardCastsKeepTheirLabels(t *testing.T) {
	const fireball, pyroblast = 133, 11366
	names := map[int]string{fireball: "Fireball", pyroblast: "Pyroblast"}
	events := []event{
		{Timestamp: 28396, Type: "begincast", AbilityGameID: fireball},
		{Timestamp: 29721, Type: "cast", AbilityGameID: fireball},
		{Timestamp: 29722, Type: "begincast", AbilityGameID: fireball},
		{Timestamp: 31046, Type: "cast", AbilityGameID: fireball},
		{Timestamp: 31054, Type: "begincast", AbilityGameID: fireball},
		{Timestamp: 32362, Type: "cast", AbilityGameID: fireball},
		{Timestamp: 32362, Type: "begincast", AbilityGameID: pyroblast},
		{Timestamp: 32363, Type: "cast", AbilityGameID: pyroblast},
	}
	casts := buildCasts(events, Fight{ID: 1, StartTime: 1000, EndTime: 101000}, names, nil, fire)

	var fireballs []Cast
	for _, c := range casts {
		if c.Name == "Fireball" {
			fireballs = append(fireballs, c)
		}
	}
	if len(fireballs) != 3 {
		t.Fatalf("got %d Fireballs, want 3", len(fireballs))
	}
	for i, c := range fireballs {
		if c.RepeatsPrevious {
			t.Errorf("Fireball %d has its own cast bar and must keep its label: %+v", i+1, c)
		}
		if c.CastTime < 1200*time.Millisecond {
			t.Errorf("Fireball %d CastTime = %v, want ~1.3s", i+1, c.CastTime)
		}
	}
}

// The case the rule exists for still works: an instant sharing a bar's end.
func TestInstantRepeatOnBarEndIsStillSuppressed(t *testing.T) {
	const pyroblast = 11366
	events := []event{
		{Timestamp: 53554, Type: "begincast", AbilityGameID: pyroblast},
		{Timestamp: 55465, Type: "cast", AbilityGameID: pyroblast},
		{Timestamp: 55465, Type: "begincast", AbilityGameID: pyroblast},
		{Timestamp: 55466, Type: "cast", AbilityGameID: pyroblast},
	}
	casts := buildCasts(events, Fight{ID: 1, StartTime: 1000, EndTime: 101000},
		map[int]string{pyroblast: "Pyroblast"}, nil, fire)
	if len(casts) != 2 {
		t.Fatalf("got %d casts, want 2", len(casts))
	}
	if casts[0].RepeatsPrevious {
		t.Error("the hard cast is the first of the pair")
	}
	if !casts[1].RepeatsPrevious {
		t.Error("the instant repeat on the bar's tail should still be suppressed")
	}
}

// The drawn timeline starts before the pull so a precast has room to be shown
// as a bar crossing the pull line rather than a point stacked on the next cast.
func TestFormatOffsetHandlesPrePullTimes(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0:00"},
		{95 * time.Second, "1:35"},
		{-1900 * time.Millisecond, "-0:02"},
		{-65 * time.Second, "-1:05"},
	}
	for _, c := range cases {
		if got := formatOffset(c.in); got != c.want {
			t.Errorf("formatOffset(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCooldownsAreMarked(t *testing.T) {
	const combustion, blazingBarrier, iceCold, fireBlast = 190319, 235313, 414658, 108853
	names := map[int]string{
		combustion: "Combustion", blazingBarrier: "Blazing Barrier",
		iceCold: "Ice Cold", fireBlast: "Fire Blast",
	}
	events := []event{
		{Timestamp: 2000, Type: "cast", AbilityGameID: combustion},
		{Timestamp: 3000, Type: "cast", AbilityGameID: fireBlast},
		{Timestamp: 4000, Type: "cast", AbilityGameID: blazingBarrier},
		{Timestamp: 5000, Type: "cast", AbilityGameID: iceCold},
	}
	casts := buildCasts(events, fightAt(100), names, nil, fire)
	marked := map[string]bool{}
	for _, c := range casts {
		marked[c.Name] = c.Cooldown
	}
	for _, n := range []string{"Combustion", "Blazing Barrier", "Ice Cold"} {
		if !marked[n] {
			t.Errorf("%s should be marked as a cooldown", n)
		}
	}
	if marked["Fire Blast"] {
		t.Error("Fire Blast is filler, not a cooldown")
	}
}

// Stampeding Roar is an ~8s buff, but the reference log applies it to 38
// targets and reuses it later in the fight. Merging every overlapping interval
// produced a single window minutes long.
func TestRaidCooldownWindowsSeparateUses(t *testing.T) {
	const roar = 106898
	fight := Fight{ID: 1, StartTime: 0, EndTime: 600000}
	var events []event
	// Two casts, five minutes apart, each buffing 38 targets for 8s.
	for _, at := range []float64{10000, 310000} {
		for target := 1; target <= 38; target++ {
			jitter := float64(target) * 20 // applications land a moment apart
			events = append(events,
				event{Timestamp: at + jitter, Type: "applybuff", AbilityGameID: roar, TargetID: target, SourceID: 7},
				event{Timestamp: at + jitter + 8000, Type: "removebuff", AbilityGameID: roar, TargetID: target, SourceID: 7},
			)
		}
	}
	windows := raidCooldownWindows(events, fight, map[int]string{7: "Seizure"})
	if len(windows) != 2 {
		t.Fatalf("got %d windows, want 2 (one per cast): %+v", len(windows), windows)
	}
	for i, w := range windows {
		if w.Duration() > 12*time.Second {
			t.Errorf("window %d lasted %v; an 8s buff cannot span minutes", i, w.Duration())
		}
		if w.Targets != 38 {
			t.Errorf("window %d has %d targets, want 38", i, w.Targets)
		}
		if w.Name != "Stampeding Roar" || w.Source != "Seizure" {
			t.Errorf("window %d = %q by %q", i, w.Name, w.Source)
		}
	}
	if gap := windows[1].Start - windows[0].Start; gap < 4*time.Minute {
		t.Errorf("the two uses are %v apart, want ~5m", gap)
	}
}

// A channelled cooldown only ever buffs its caster, so no minimum target count
// may be applied.
func TestRaidCooldownWindowsKeepsSingleTargetChannels(t *testing.T) {
	const tranquility = 740
	fight := Fight{ID: 1, StartTime: 0, EndTime: 600000}
	events := []event{
		{Timestamp: 5000, Type: "applybuff", AbilityGameID: tranquility, TargetID: 7, SourceID: 7},
		{Timestamp: 9000, Type: "removebuff", AbilityGameID: tranquility, TargetID: 7, SourceID: 7},
	}
	windows := raidCooldownWindows(events, fight, map[int]string{7: "Seizure"})
	if len(windows) != 1 {
		t.Fatalf("got %d windows, want 1", len(windows))
	}
	if windows[0].Targets != 1 || windows[0].Duration() != 4*time.Second {
		t.Errorf("window = %+v, want 1 target for 4s", windows[0])
	}
}

// Anti-Magic Zone is a ground effect: players walk in and out, so one placement
// produces many apply/remove pairs. Re-entries must fold into the placement
// they happened inside rather than becoming zero-width segments of their own.
func TestRaidCooldownWindowsFoldsReentries(t *testing.T) {
	const amz = 145629
	fight := Fight{ID: 1, StartTime: 0, EndTime: 600000}
	var events []event
	// One 10s placement at 40s, with 21 players inside for varying spans.
	for target := 1; target <= 21; target++ {
		in := 40000 + float64(target)*30
		events = append(events,
			event{Timestamp: in, Type: "applybuff", AbilityGameID: amz, TargetID: target},
			event{Timestamp: 50000, Type: "removebuff", AbilityGameID: amz, TargetID: target},
		)
	}
	// One player steps back in near the end of that same placement.
	events = append(events,
		event{Timestamp: 49000, Type: "applybuff", AbilityGameID: amz, TargetID: 3},
		event{Timestamp: 49000, Type: "removebuff", AbilityGameID: amz, TargetID: 3},
	)
	windows := raidCooldownWindows(events, fight, nil)
	if len(windows) != 1 {
		t.Fatalf("got %d windows, want 1 placement: %+v", len(windows), windows)
	}
	if d := windows[0].Duration(); d < 9*time.Second || d > 11*time.Second {
		t.Errorf("window lasted %v, want about 10s", d)
	}
	for _, w := range windows {
		if w.Duration() <= 0 {
			t.Errorf("a window with no width reached the timeline: %+v", w)
		}
	}
}

func TestRaidCooldownWindowsCapsUnclosedBuffs(t *testing.T) {
	const roar = 106898
	fight := Fight{ID: 1, StartTime: 0, EndTime: 600000}
	var events []event
	// Eight players get an 8s buff; two never lose it, because they died.
	for target := 1; target <= 8; target++ {
		events = append(events, event{Timestamp: 10000, Type: "applybuff", AbilityGameID: roar, TargetID: target})
		if target > 2 {
			events = append(events, event{Timestamp: 18000, Type: "removebuff", AbilityGameID: roar, TargetID: target})
		}
	}
	windows := raidCooldownWindows(events, fight, nil)
	if len(windows) != 1 {
		t.Fatalf("got %d windows, want 1", len(windows))
	}
	if d := windows[0].Duration(); d > 9*time.Second {
		t.Errorf("window lasted %v; an unclosed buff must not run to the fight end", d)
	}
}

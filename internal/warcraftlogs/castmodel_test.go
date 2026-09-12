package warcraftlogs

import (
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

// The tests in this file pin the cast model's edge handling: what #13 fixed,
// each named for the log phenomenon it protects.

const (
	pyroblastID_  = 11366
	fireballID_   = 133
	scorchID_     = 2948
	fireBlastID_  = 108853
	hotStreakID_  = 48108
	combustionID_ = 190319
)

var fireNames = map[int]string{
	pyroblastID_: "Pyroblast", fireballID_: "Fireball", scorchID_: "Scorch",
	fireBlastID_: "Fire Blast", combustionID_: "Combustion",
}

// ev builds an event at ms milliseconds after the fight's start, which
// fightAt puts at report time 1000.
func ev(ms float64, typ string, ability int) event {
	return event{Timestamp: 1000 + ms, Type: typ, AbilityGameID: ability}
}

func byName(casts []Cast, name string) []Cast {
	var out []Cast
	for _, c := range casts {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

// A begincast that never landed, followed a minute later by a bare cast of
// the same spell, is an abandoned bar and then an instant — not one 60-second
// cast. Before the bound, the pairing swallowed every gap in between.
func TestAbandonedBarIsNotCompletedByAMuchLaterCast(t *testing.T) {
	events := []event{
		ev(1000, "begincast", scorchID_),
		ev(2500, "cast", scorchID_),       // a real 1.5s Scorch, so the spell has a median
		ev(10000, "begincast", scorchID_), // abandoned
		ev(30000, "cast", fireBlastID_),
		ev(70000, "cast", scorchID_), // 60s later: not the same bar
	}
	casts := buildCasts(events, fightAt(100), fireNames, nil, fire)
	scorches := byName(casts, "Scorch")
	if len(scorches) != 3 {
		t.Fatalf("got %d Scorch rows, want 3 (a cast, an abandoned bar, and a separate instant)", len(scorches))
	}
	if !scorches[1].Cancelled {
		t.Errorf("the abandoned Scorch at 10s should be cancelled, got %+v", scorches[1])
	}
	if scorches[2].Cancelled || scorches[2].HadBegincast || scorches[2].Offset != 70*time.Second {
		t.Errorf("the Scorch at 70s should be its own instant row, got %+v", scorches[2])
	}
	// The abandoned bar ran for the spell's typical time, then the gap before
	// the Fire Blast is idle from there, and the 40s before the last Scorch is
	// idle too — none of it hidden under a phantom bar.
	if scorches[1].Wasted() != 1500*time.Millisecond {
		t.Errorf("Wasted = %v, want 1.5s (the spell's median)", scorches[1].Wasted())
	}
	if fb := byName(casts, "Fire Blast")[0]; fb.Gap != 30*time.Second-11500*time.Millisecond {
		t.Errorf("gap before Fire Blast = %v, want 18.5s (from the end of the abandoned bar)", fb.Gap)
	}
	if scorches[2].Gap != 40*time.Second {
		t.Errorf("gap before the last Scorch = %v, want 40s", scorches[2].Gap)
	}
}

// Order inside a millisecond is the game's, and means something. A begincast
// then a cast on one tick is an instant; a cast then a begincast of the same
// spell is a hard cast landing and the next beginning. Every cast-first pair
// in the recorded kill sits one cast time after its begincast.
func TestSameMillisecondOrderIsMeaningful(t *testing.T) {
	// Chain-cast: Fireball lands at 1263 and the next Fireball begins there.
	chain := []event{
		ev(0, "begincast", fireballID_),
		ev(1263, "cast", fireballID_),
		ev(1263, "begincast", fireballID_),
		ev(2526, "cast", fireballID_),
	}
	casts := buildCasts(chain, fightAt(100), fireNames, nil, fire)
	if len(casts) != 2 || casts[0].Cancelled || casts[1].Cancelled {
		t.Fatalf("chain-cast: got %+v, want two completed hard casts", casts)
	}
	if casts[0].CastTime != 1263*time.Millisecond || casts[1].CastTime != 1263*time.Millisecond {
		t.Errorf("chain-cast times = %v, %v; want 1.263s each", casts[0].CastTime, casts[1].CastTime)
	}

	// Instant: begincast and cast together.
	instant := []event{
		ev(0, "begincast", pyroblastID_),
		ev(0, "cast", pyroblastID_),
	}
	casts = buildCasts(instant, fightAt(100), fireNames, nil, fire)
	if len(casts) != 1 || !casts[0].resolvedInstantly() || casts[0].Cancelled {
		t.Errorf("instant: got %+v, want one instantly-resolved cast", casts)
	}
}

// The two events of an instant sometimes straddle a millisecond boundary. A
// median that counted those as one-millisecond casts called Pyroblast's cast
// time 1ms on the recorded wipe, and the precast's reconstructed bar was 1ms
// long. Only bars over the tolerance count, and only logged ones — never an
// abandoned bar and never the reconstruction itself.
func TestMedianIgnoresJitterAbandonedBarsAndItsOwnEstimate(t *testing.T) {
	casts := []Cast{
		{AbilityID: pyroblastID_, CastTime: time.Millisecond, HadBegincast: true},
		{AbilityID: pyroblastID_, CastTime: time.Millisecond, HadBegincast: true},
		{AbilityID: pyroblastID_, CastTime: 1800 * time.Millisecond, HadBegincast: true},
		{AbilityID: pyroblastID_, CastTime: 4 * time.Second, Estimated: true, Precast: true},
		{AbilityID: pyroblastID_, CastTime: 0, Cancelled: true, End: 900 * time.Millisecond},
	}
	if got := medianCastTime(casts, pyroblastID_); got != 1800*time.Millisecond {
		t.Errorf("median = %v, want 1.8s (the one real bar)", got)
	}
	if got := medianCastTime(casts[:2], pyroblastID_); got != 0 {
		t.Errorf("median of jitter-only casts = %v, want 0 (never hard cast)", got)
	}
}

// An abandoned bar ends when the player did something else, or when it would
// have finished, whichever came first — and that time is cancelled, not idle.
func TestCancelledBarEndsAtTheNextEventOrTheTypicalTime(t *testing.T) {
	events := []event{
		ev(0, "begincast", fireballID_),
		ev(1300, "cast", fireballID_),      // the spell's typical time is 1.3s
		ev(5000, "begincast", fireballID_), // abandoned; nothing else for 10s
		ev(15000, "cast", fireBlastID_),
		ev(20000, "begincast", fireballID_), // abandoned; a Fire Blast 400ms later
		ev(20400, "cast", fireBlastID_),
	}
	casts := buildCasts(events, fightAt(100), fireNames, nil, fire)
	fireballs := byName(casts, "Fireball")
	if len(fireballs) != 3 || !fireballs[1].Cancelled || !fireballs[2].Cancelled {
		t.Fatalf("got %+v, want one completed and two abandoned Fireballs", fireballs)
	}
	if fireballs[1].Wasted() != 1300*time.Millisecond {
		t.Errorf("first abandoned bar ran %v, want 1.3s (its typical time; nothing interrupted it)", fireballs[1].Wasted())
	}
	if fireballs[2].Wasted() != 400*time.Millisecond {
		t.Errorf("second abandoned bar ran %v, want 400ms (cut short by the Fire Blast)", fireballs[2].Wasted())
	}
	blasts := byName(casts, "Fire Blast")
	if blasts[0].Gap != 15*time.Second-6300*time.Millisecond {
		t.Errorf("gap before the first Fire Blast = %v, want 8.7s (idle starts when the bar was abandoned)", blasts[0].Gap)
	}
	if blasts[1].Gap != 0 {
		t.Errorf("gap before the second Fire Blast = %v, want 0 (the bar ran right up to it)", blasts[1].Gap)
	}
	for _, c := range casts {
		if c.Cancelled && c.CastTime != 0 {
			t.Errorf("a cancelled cast carries CastTime %v; the time is Wasted()", c.CastTime)
		}
		if c.Cancelled && c.CastLabel() != "cancelled" {
			t.Errorf("CastLabel() = %q for a cancelled cast", c.CastLabel())
		}
	}
}

// A spell abandoned before it was ever completed has no typical time of its
// own; the player's other bars stand in, and failing those, a second.
func TestCancelledBarOfAnUnseenSpellUsesAFallback(t *testing.T) {
	events := []event{
		ev(0, "begincast", fireballID_),
		ev(1300, "cast", fireballID_),
		ev(5000, "begincast", scorchID_), // never completed anywhere in the fight
	}
	casts := buildCasts(events, fightAt(100), fireNames, nil, fire)
	if s := byName(casts, "Scorch")[0]; s.Wasted() != 1300*time.Millisecond {
		t.Errorf("Wasted = %v, want 1.3s (the median across the player's other bars)", s.Wasted())
	}
	alone := []event{ev(5000, "begincast", scorchID_)}
	casts = buildCasts(alone, fightAt(100), fireNames, nil, fire)
	if s := casts[0]; s.Wasted() != unknownCastBar {
		t.Errorf("Wasted = %v, want the fallback %v", s.Wasted(), unknownCastBar)
	}
}

// The precast is the first bare cast of a castable spell inside the window.
// An instant Pyroblast brings its begincast, so it is never mistaken for one
// however early it lands; and it is judged for its proc like any other, and
// does not mark the Fire Blast that generated the proc as woven into it.
func TestInstantPyroblastInTheOpeningIsNotAPrecast(t *testing.T) {
	events := []event{
		ev(0, "cast", pyroblastID_), // the real precast: bare, at the pull
		ev(300, "begincast", fireballID_),
		ev(1600, "cast", fireballID_),
		ev(2000, "cast", fireBlastID_), // generates Hot Streak
		ev(2500, "begincast", pyroblastID_),
		ev(2500, "cast", pyroblastID_), // the instant off it
		ev(4000, "begincast", pyroblastID_),
		ev(5800, "cast", pyroblastID_), // a hard cast, so Pyroblast has a median
	}
	casts := buildCasts(events, fightAt(100), fireNames, nil, fire)
	windows := []auraWindow{{name: "Hot Streak!", start: 2000 * time.Millisecond, end: 2500 * time.Millisecond}}
	classifyProcs(casts, windows, fire)

	pyros := byName(casts, "Pyroblast")
	if len(pyros) != 3 {
		t.Fatalf("got %d Pyroblasts, want 3", len(pyros))
	}
	if !pyros[0].Precast || !pyros[0].Estimated || pyros[0].Offset != -1800*time.Millisecond {
		t.Errorf("the pull Pyroblast should be the precast with a 1.8s reconstructed bar, got %+v", pyros[0])
	}
	if pyros[1].Precast || pyros[1].CastLabel() != "0.0s" {
		t.Errorf("the instant at 2.5s must be an instant, not a precast: label %q, %+v", pyros[1].CastLabel(), pyros[1])
	}
	if pyros[1].Proc != "Hot Streak!" {
		t.Errorf("the instant carries proc %q, want Hot Streak!", pyros[1].Proc)
	}
	if fb := byName(casts, "Fire Blast")[0]; fb.DuringCast {
		t.Error("the Fire Blast that generated the proc is marked as woven into a cast that did not exist")
	}
}

// Only the first bare cast can be the precast — a bar cannot have begun
// before the previous cast finished — and only inside the window.
func TestPrecastIsOnlyTheFirstBareCastInsideTheWindow(t *testing.T) {
	late := []event{
		ev(300, "cast", fireBlastID_),
		ev(2000, "cast", pyroblastID_), // bare, castable, but 2s in: too late
		ev(4000, "begincast", pyroblastID_),
		ev(5800, "cast", pyroblastID_),
	}
	casts := buildCasts(late, fightAt(100), fireNames, nil, fire)
	if p := byName(casts, "Pyroblast")[0]; p.Precast {
		t.Errorf("a bare cast at 2s is outside the %v window, got %+v", precastWindow, p)
	}

	afterAnInstant := []event{
		ev(200, "cast", fireBlastID_), // an off-GCD instant landed first
		ev(400, "cast", pyroblastID_), // still the first bare castable cast
		ev(4000, "begincast", pyroblastID_),
		ev(5800, "cast", pyroblastID_),
	}
	casts = buildCasts(afterAnInstant, fightAt(100), fireNames, nil, fire)
	if p := byName(casts, "Pyroblast")[0]; !p.Precast {
		t.Errorf("the first bare castable cast at 400ms is the precast even after an instant, got %+v", p)
	}
	if casts[0].Name != "Pyroblast" || casts[0].Offset >= 0 {
		t.Errorf("the reconstructed precast should sort first with a negative offset, got %+v", casts[0])
	}
}

// A precast is hard cast on purpose, so that it lands with the pull. It gets
// its verdict — an aura that was up is named — but never "no proc".
func TestPrecastIsJudgedButNeverAMistake(t *testing.T) {
	events := []event{
		ev(0, "cast", pyroblastID_),
		ev(3000, "begincast", pyroblastID_),
		ev(4800, "cast", pyroblastID_),
	}
	casts := buildCasts(events, fightAt(100), fireNames, nil, fire)
	classifyProcs(casts, nil, fire)
	if p := casts[0]; !p.Precast || p.ProcMissing || p.ProcLabel() != "" {
		t.Errorf("precast verdict = %q missing=%v, want no verdict and no mistake", p.ProcLabel(), p.ProcMissing)
	}
	if p := casts[1]; !p.ProcMissing {
		t.Errorf("an ordinary hard cast with no aura is a mistake, got %+v", p)
	}
}

// Six builders sort their input. They must sort a copy: the decoded response
// is shared by all of them, and will be shared across requests once #2 caches
// it, so an in-place sort is a data race waiting for its second goroutine.
func TestBuildersLeaveTheCallersSliceAlone(t *testing.T) {
	unsorted := []event{
		ev(3000, "cast", fireBlastID_),
		ev(1000, "begincast", fireballID_),
		ev(2300, "cast", fireballID_),
		{Timestamp: 1000 + 500, Type: "applybuff", AbilityGameID: hotStreakID_, TargetID: 7, SourceID: 7},
		{Timestamp: 1000 + 200, Type: "removebuff", AbilityGameID: hotStreakID_, TargetID: 7, SourceID: 7},
	}
	fight := fightAt(100)
	check := func(name string, run func([]event)) {
		in := slices.Clone(unsorted)
		run(in)
		if !slices.Equal(in, unsorted) {
			t.Errorf("%s reordered the caller's slice", name)
		}
	}
	check("buildCasts", func(e []event) { buildCasts(e, fight, fireNames, nil, fire) })
	check("auraWindows", func(e []event) { auraWindows(e, fight, fire) })
	check("cooldownWindows", func(e []event) { cooldownWindows(e, fight, fireNames, fire) })
	check("lustWindows", func(e []event) { lustWindows(e, fight, fireNames, nil) })
	check("raidCooldownWindows", func(e []event) { raidCooldownWindows(e, fight, nil) })
	check("buildBossCasts", func(e []event) { buildBossCasts(e, nil, fireNames, fight) })
}

// Windows seeded from a map came out in map order, which is random, and the
// sort that followed was unstable — so two cooldowns applied on the same
// millisecond could swap places between one request and the next. A golden
// test cannot exist on top of that.
func TestWindowsComeOutInTheSameOrderEveryTime(t *testing.T) {
	const a, b = 97463, 145629 // two raid cooldowns applied together
	var events []event
	for target := range 6 {
		for _, id := range []int{a, b} {
			events = append(events,
				event{Timestamp: 1000 + 1000, Type: "applybuff", AbilityGameID: id, TargetID: target, SourceID: 1},
				event{Timestamp: 1000 + 9000, Type: "removebuff", AbilityGameID: id, TargetID: target, SourceID: 1},
			)
		}
	}
	first := raidCooldownWindows(events, fightAt(100), nil)
	if len(first) != 2 {
		t.Fatalf("got %d windows, want 2", len(first))
	}
	for range 50 {
		rand.Shuffle(len(events), func(i, j int) { events[i], events[j] = events[j], events[i] })
		again := raidCooldownWindows(events, fightAt(100), nil)
		if !slices.Equal(again, first) {
			t.Fatalf("the same events produced a different order:\n%+v\n%+v", first, again)
		}
	}
}

// The properties the hand-written tests assert one input at a time, asserted
// over random event streams. Gap >= 0 and sortedness are not here: both are
// enforced unconditionally and can never fail.
func FuzzBuildCasts(f *testing.F) {
	f.Add(uint64(1), 20)
	f.Add(uint64(7), 200)
	f.Fuzz(func(t *testing.T, seed uint64, n int) {
		if n < 0 || n > 500 {
			t.Skip()
		}
		r := rand.New(rand.NewPCG(seed, seed))
		abilities := []int{pyroblastID_, fireballID_, scorchID_, fireBlastID_}
		events := make([]event, 0, n)
		at := 0.0
		for range n {
			at += float64(r.IntN(2500))
			typ := "cast"
			if r.IntN(3) == 0 {
				typ = "begincast"
			}
			events = append(events, ev(at, typ, abilities[r.IntN(len(abilities))]))
		}
		fight := Fight{StartTime: 1000, EndTime: 1000 + at + 1000}
		casts := buildCasts(events, fight, fireNames, nil, fire)

		var gaps time.Duration
		for _, c := range casts {
			gaps += c.Gap
			if c.Cancelled && c.Precast {
				t.Errorf("cast is both cancelled and precast: %+v", c)
			}
			if c.End < c.Offset {
				t.Errorf("End before Offset: %+v", c)
			}
			if c.CastTime > maxCastBar {
				t.Errorf("a cast bar longer than any in the game: %+v", c)
			}
		}
		if span := fight.Duration(); gaps > span+time.Millisecond {
			t.Errorf("gaps sum to %v over a %v fight", gaps, span)
		}
		if len(casts) > len(events) {
			t.Errorf("%d casts from %d events", len(casts), len(events))
		}
	})
}

// The recorded kill, run through buildCasts, with what came out written
// down. When a heuristic moves, this says exactly what it moved. The counts
// were read, not just recorded: 12 abandoned bars on a seven-minute Heroic
// kill, each shorter than the spell's own cast time, is what moving for
// mechanics looks like.
func TestGoldenRecordedKill(t *testing.T) {
	rep, fight := recordedKill(t)
	events := rep.Casts.Data
	casts := buildCasts(events, fight, rep.names(), nil, fire)

	kinds := map[string]int{}
	var wasted time.Duration
	for _, c := range casts {
		wasted += c.Wasted()
		switch {
		case c.Cancelled:
			kinds["cancelled"]++
		case c.Precast:
			kinds["precast"]++
		case c.DuringCast:
			kinds["woven"]++
		case !c.HadBegincast:
			kinds["instant"]++
		case c.resolvedInstantly():
			kinds["proc"]++
		default:
			kinds["hard"]++
		}
	}
	want := map[string]int{"precast": 1, "hard": 115, "proc": 179, "instant": 114, "woven": 59, "cancelled": 12}
	for k, n := range want {
		if kinds[k] != n {
			t.Errorf("%s = %d, want %d", k, kinds[k], n)
		}
	}
	if len(casts) != 480 {
		t.Errorf("len(casts) = %d, want 480", len(casts))
	}
	if wasted.Round(time.Millisecond) != 13625*time.Millisecond {
		t.Errorf("time on abandoned bars = %v, want 13.625s", wasted)
	}

	// The opener, row by row, against the pull as the log times it: the
	// precast Pyroblast landing 168ms in with its reconstructed bar, the
	// Fireball begun as it lands, two instants woven into it, then the
	// first Hot Streak Pyroblast as the Fireball lands.
	type row struct {
		name             string
		offset, castTime time.Duration
		precast, during  bool
	}
	opener := []row{
		{"Pyroblast", -1650 * time.Millisecond, 1818 * time.Millisecond, true, false},
		{"Fireball", 168 * time.Millisecond, 1318 * time.Millisecond, false, false},
		{"Fire Blast", 750 * time.Millisecond, 0, false, true},
		{"Combustion", 1234 * time.Millisecond, 0, false, true},
		{"Pyroblast", 1486 * time.Millisecond, 0, false, false},
		{"Pyroblast", 2591 * time.Millisecond, 0, false, false},
	}
	for i, w := range opener {
		got := row{casts[i].Name, casts[i].Offset, casts[i].CastTime, casts[i].Precast, casts[i].DuringCast}
		if got != w {
			t.Errorf("row %d = %+v, want %+v", i, got, w)
		}
	}
}

package view

import (
	"math"
	"testing"
	"time"

	"wowinsight/internal/warcraftlogs"
)

// The lead-in covers the earliest precast plus a margin; the pull sits
// inside the drawn span; everything shares one axis.
func TestLayoutLeadInAndPositions(t *testing.T) {
	tl := &warcraftlogs.Timeline{
		Duration: 100 * time.Second,
		Casts: []warcraftlogs.Cast{
			// A precast: begun 1.9s before the pull, landing 0.1s after it.
			{Name: "Pyroblast", Offset: -1900 * time.Millisecond, End: 100 * time.Millisecond,
				CastTime: 2 * time.Second, Precast: true, Estimated: true},
			{Name: "Fireball", Offset: 100 * time.Millisecond, End: 1400 * time.Millisecond,
				CastTime: 1300 * time.Millisecond},
		},
		Phases: []warcraftlogs.Phase{{Name: "One", Start: 0, End: 100 * time.Second}},
		Lusts:  []warcraftlogs.RaidWindow{{Name: "Time Warp", Start: 10 * time.Second, End: 50 * time.Second}},
	}
	v := Layout(tl, Options{})

	// The lead-in must cover the precast plus a margin.
	if v.LeadIn < 1900*time.Millisecond+leadInMargin {
		t.Errorf("LeadIn = %v, too small for a cast starting 1.9s before the pull", v.LeadIn)
	}
	if v.Total != v.LeadIn+tl.Duration {
		t.Errorf("Total = %v, want LeadIn + Duration", v.Total)
	}

	// The pull is inside the drawn span, not at its left edge.
	if v.PullPercent <= 0 || v.PullPercent >= 100 {
		t.Errorf("PullPercent = %v, want somewhere inside the timeline", v.PullPercent)
	}
	// The precast begins left of the pull; the next cast begins right of it.
	if v.Casts[0].Percent >= v.PullPercent {
		t.Errorf("precast at %v%% should start before the pull at %v%%", v.Casts[0].Percent, v.PullPercent)
	}
	if v.Casts[1].Percent <= v.PullPercent {
		t.Errorf("Fireball at %v%% should start after the pull", v.Casts[1].Percent)
	}
	// Nothing may be positioned off the drawn area.
	for _, c := range v.Casts {
		if c.Percent < 0 || c.Percent > 100 {
			t.Errorf("%s positioned at %v%%, outside the timeline", c.Name, c.Percent)
		}
	}

	// A phase covering the whole fight runs from the pull to the right edge.
	p := v.Phases.Bars[0]
	if math.Abs(p.StartPercent-v.PullPercent) > 0.001 {
		t.Errorf("phase starts at %v%%, want the pull at %v%%", p.StartPercent, v.PullPercent)
	}
	if math.Abs(p.StartPercent+p.WidthPercent-100) > 0.001 {
		t.Errorf("phase ends at %v%%, want 100", p.StartPercent+p.WidthPercent)
	}

	// Everything shares one domain: a lust window and a cast at the same
	// moment must land on the same position.
	if got, want := v.percent(10*time.Second), v.Lusts.Bars[0].StartPercent; math.Abs(got-want) > 0.001 {
		t.Errorf("lust at %v%% but percent(10s) = %v%%", want, got)
	}
}

func TestLayoutWithoutPrecastKeepsMinimumLeadIn(t *testing.T) {
	tl := &warcraftlogs.Timeline{
		Duration: 60 * time.Second,
		Casts:    []warcraftlogs.Cast{{Name: "Fire Blast", Offset: time.Second, End: time.Second}},
	}
	v := Layout(tl, Options{})
	if v.LeadIn != minLeadIn {
		t.Errorf("LeadIn = %v, want the %v minimum", v.LeadIn, minLeadIn)
	}
	if v.PullPercent <= 0 {
		t.Errorf("PullPercent = %v, want the pull drawn inside the span", v.PullPercent)
	}
}

// The reason the drawing is not in the analysis: two pulls of different
// lengths laid out against one shared axis put the same moment at the same
// place. Against their own axes, a 40% mark means two different moments.
func TestTwoPullsShareOneAxis(t *testing.T) {
	cast := func(at time.Duration) []warcraftlogs.Cast {
		return []warcraftlogs.Cast{{Name: "Fireball", Offset: at, End: at + time.Second}}
	}
	wipe := &warcraftlogs.Timeline{Duration: 3 * time.Minute, Casts: cast(60 * time.Second)}
	kill := &warcraftlogs.Timeline{Duration: 6 * time.Minute, Casts: cast(60 * time.Second)}

	own := []float64{Layout(wipe, Options{}).Casts[0].Percent, Layout(kill, Options{}).Casts[0].Percent}
	if math.Abs(own[0]-own[1]) < 1 {
		t.Fatalf("on their own axes a cast at 1:00 lands at %.1f%% and %.1f%%; they should differ, or this test proves nothing", own[0], own[1])
	}

	shared := Options{LeadIn: 2 * time.Second, Total: 6*time.Minute + 2*time.Second}
	a, b := Layout(wipe, shared), Layout(kill, shared)
	if math.Abs(a.Casts[0].Percent-b.Casts[0].Percent) > 0.001 {
		t.Errorf("on one axis a cast at 1:00 lands at %.3f%% on the wipe and %.3f%% on the kill; want the same place", a.Casts[0].Percent, b.Casts[0].Percent)
	}
	if a.PullPercent != b.PullPercent || a.Total != b.Total {
		t.Error("the two layouts do not share the pull position and span they were given")
	}
	// And the analysis was not touched: it has no idea it was drawn twice.
	if wipe.Casts[0].Offset != 60*time.Second {
		t.Error("laying out changed the analysis")
	}
}

// One packing for every lane. The raid cooldown lane had this test and the
// personal cooldown lane, packed by a copy of the same code, had none; now
// there is one function and one Bar, so one test covers whatever is a lane.
func TestPackRowsStacksOverlaps(t *testing.T) {
	sec := func(n int) time.Duration { return time.Duration(n) * time.Second }
	bars := func() []Bar {
		return []Bar{
			{Name: "A", Start: sec(0), End: sec(10)},
			{Name: "B", Start: sec(5), End: sec(15)},  // overlaps A
			{Name: "C", Start: sec(6), End: sec(9)},   // overlaps A and B
			{Name: "D", Start: sec(20), End: sec(25)}, // overlaps nothing
		}
	}
	rows := packRows(bars())
	if rows != 3 {
		t.Fatalf("used %d rows, want 3", rows)
	}
	packed := bars()
	packRows(packed)
	got := map[string]int{}
	for _, b := range packed {
		got[b.Name] = b.Row
	}
	if got["A"] != 0 || got["B"] != 1 || got["C"] != 2 {
		t.Errorf("overlapping bars share rows: %v", got)
	}
	if got["D"] != 0 {
		t.Errorf("D = row %d, want row 0 reused once A has ended", got["D"])
	}
	// Nothing on the same row may overlap in time.
	for i := range packed {
		for j := i + 1; j < len(packed); j++ {
			a, b := packed[i], packed[j]
			if a.Row == b.Row && a.Start < b.End && b.Start < a.End {
				t.Errorf("%s and %s overlap on row %d", a.Name, b.Name, a.Row)
			}
		}
	}

	// And every lane goes through it: the same overlapping windows as raid
	// cooldowns and as personal cooldowns both come out on three rows.
	windows := []warcraftlogs.RaidWindow{
		{Name: "A", Start: sec(0), End: sec(10)}, {Name: "B", Start: sec(5), End: sec(15)}, {Name: "C", Start: sec(6), End: sec(9)},
	}
	cooldowns := []warcraftlogs.CooldownWindow{
		{Name: "A", Start: sec(0), End: sec(10)}, {Name: "B", Start: sec(5), End: sec(15)}, {Name: "C", Start: sec(6), End: sec(9)},
	}
	v := Layout(&warcraftlogs.Timeline{Duration: sec(60), RaidCDs: windows, Cooldowns: cooldowns}, Options{})
	for name, lane := range map[string]Lane{"raid cooldowns": v.RaidCDs, "personal cooldowns": v.Cooldowns} {
		if lane.Rows != 3 {
			t.Errorf("%s: %d rows, want 3", name, lane.Rows)
		}
	}
}

// Every lane and the boss markers get a position; a player with no damage
// series gets no graph rather than an empty one.
func TestLayoutPositionsEveryLane(t *testing.T) {
	sec := func(n int) time.Duration { return time.Duration(n) * time.Second }
	tl := &warcraftlogs.Timeline{
		Duration:  sec(300),
		Casts:     []warcraftlogs.Cast{{Name: "Fireball", Offset: sec(2), End: sec(4), CastTime: sec(2), Gap: sec(1)}},
		Lusts:     []warcraftlogs.RaidWindow{{Name: "Time Warp", Start: sec(2), End: sec(42)}},
		Phases:    []warcraftlogs.Phase{{Name: "One", Start: 0, End: sec(300)}},
		RaidCDs:   []warcraftlogs.RaidWindow{{Name: "Rallying Cry", Start: sec(60), End: sec(70)}},
		Cooldowns: []warcraftlogs.CooldownWindow{{Name: "Combustion", Start: sec(12), End: sec(22)}},
		BossCasts: []warcraftlogs.BossCast{{Name: "Dread Bolt", Offset: sec(30), End: sec(32), Count: 1}},
		DPS:       &warcraftlogs.DPSGraph{Peak: 100, Points: []warcraftlogs.DPSPoint{{Offset: 0, DPS: 50}, {Offset: sec(3), DPS: 100}}},
	}
	v := Layout(tl, Options{})
	if v.Casts[0].Percent <= 0 || v.Casts[0].CastWidthPercent <= 0 || v.Casts[0].GapWidthPercent <= 0 {
		t.Errorf("cast = %+v, want a position, a width and a gap", v.Casts[0])
	}
	for name, lane := range map[string]Lane{"lusts": v.Lusts, "phases": v.Phases, "raid cooldowns": v.RaidCDs, "cooldowns": v.Cooldowns} {
		if len(lane.Bars) != 1 || lane.Bars[0].WidthPercent <= 0 || lane.Rows != 1 {
			t.Errorf("%s lane = %+v, want one positioned bar on one row", name, lane)
		}
	}
	if len(v.Boss) != 1 || v.Boss[0].Percent <= 0 || v.Boss[0].WidthPercent <= 0 {
		t.Errorf("boss markers = %+v, want one with a position and a bar", v.Boss)
	}
	if v.DPS == nil || v.DPS.Line == "" || v.Taken != nil {
		t.Errorf("DPS drawn=%v Taken=%v, want a curve for the one series and nothing for the missing one", v.DPS != nil, v.Taken)
	}
}

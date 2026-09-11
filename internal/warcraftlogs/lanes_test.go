package warcraftlogs

import (
	"testing"
	"time"
)

// The tests in this file pin the boss and raid cooldown lanes, which #3 will
// read to say why a pull wiped. Each is named for the phenomenon it protects.

const (
	dreadBolt  = 1214148
	nightfall  = 1214150
	bossA      = 28
	bossB      = 97
	addID      = 300
	bossNamesN = 2
)

var bossNPCs = map[int]Actor{
	bossA: {ID: bossA, Name: "The Coiled One", SubType: "Boss"},
	bossB: {ID: bossB, Name: "The Other One", SubType: "Boss"},
	addID: {ID: addID, Name: "Venomous Add", SubType: "NPC"},
}

var bossAbilities = map[int]string{dreadBolt: "Dread Bolt", nightfall: "Eternal Nightfall"}

func bossEv(ms float64, typ string, ability, source int) event {
	return event{Timestamp: 1000 + ms, Type: typ, AbilityGameID: ability, SourceID: source}
}

// Filler arrives in clusters — Dread Bolt fires 78 times on the recorded kill —
// and one marker per cast would bury the mechanics. Casts a second apart
// chain into one marker; a 4s pause starts another.
func TestBossCastsBurstIntoOneMarker(t *testing.T) {
	var events []event
	for i := range 78 {
		events = append(events, bossEv(float64(i)*1000, "cast", dreadBolt, bossA))
	}
	casts := buildBossCasts(events, bossNPCs, bossAbilities, fightAt(200))
	if len(casts) != 1 || casts[0].Count != 78 {
		t.Fatalf("got %+v, want one marker counting 78", casts)
	}
	if casts[0].Label() != "Dread Bolt ×78" {
		t.Errorf("Label() = %q", casts[0].Label())
	}

	two := []event{bossEv(0, "cast", dreadBolt, bossA), bossEv(4000, "cast", dreadBolt, bossA)}
	casts = buildBossCasts(two, bossNPCs, bossAbilities, fightAt(200))
	if len(casts) != 2 {
		t.Errorf("casts 4s apart should be two markers, got %+v", casts)
	}
}

// Adds and the environment cast constantly and say nothing about the fight's
// structure; only the encounter's own NPCs are kept.
func TestBossCastsKeepOnlyTheBoss(t *testing.T) {
	events := []event{
		bossEv(0, "cast", dreadBolt, addID), // an add
		bossEv(0, "cast", dreadBolt, -1),    // the environment
		bossEv(0, "cast", dreadBolt, 999),   // an NPC master data does not know
		bossEv(0, "cast", dreadBolt, bossA), // the boss
	}
	casts := buildBossCasts(events, bossNPCs, bossAbilities, fightAt(200))
	if len(casts) != 1 || casts[0].Source != "The Coiled One" {
		t.Errorf("got %+v, want only the boss's cast", casts)
	}
}

func TestBossCastPairIsOneMarkerWithABar(t *testing.T) {
	events := []event{bossEv(0, "begincast", nightfall, bossA), bossEv(2500, "cast", nightfall, bossA)}
	casts := buildBossCasts(events, bossNPCs, bossAbilities, fightAt(200))
	if len(casts) != 1 || casts[0].CastTime() != 2500*time.Millisecond || casts[0].Interrupted != 0 {
		t.Errorf("got %+v, want one landed cast with a 2.5s bar", casts)
	}
	if casts[0].Timestamp() != "0:00" {
		t.Errorf("Timestamp() = %q", casts[0].Timestamp())
	}
}

// For a wipe post-mortem "did we kick it" is the first question. A begincast
// that never landed — kicked, or cut off by a phase change — is counted as
// interrupted, not as a landed cast, and the marker says so. The recorded
// kill has four: one Dreadmarch and three Eternal Nightfalls.
func TestInterruptedBossCastIsDistinguishable(t *testing.T) {
	alone := []event{bossEv(0, "begincast", nightfall, bossA)}
	casts := buildBossCasts(alone, bossNPCs, bossAbilities, fightAt(200))
	if len(casts) != 1 || !casts[0].Cancelled() || casts[0].Label() != "Eternal Nightfall (interrupted)" {
		t.Errorf("a lone begincast: got %+v label %q", casts, casts[0].Label())
	}

	// A burst of five where the 2nd and 4th were kicked: the marker counts
	// five, three landed, and says so. The kicked ones are stopped by the
	// next begincast of the same spell from the same NPC.
	burst := []event{
		bossEv(0, "begincast", dreadBolt, bossA), bossEv(500, "cast", dreadBolt, bossA),
		bossEv(1000, "begincast", dreadBolt, bossA), // kicked
		bossEv(2000, "begincast", dreadBolt, bossA), bossEv(2500, "cast", dreadBolt, bossA),
		bossEv(3000, "begincast", dreadBolt, bossA), // kicked
		bossEv(4000, "begincast", dreadBolt, bossA), bossEv(4500, "cast", dreadBolt, bossA),
	}
	casts = buildBossCasts(burst, bossNPCs, bossAbilities, fightAt(200))
	if len(casts) != 1 {
		t.Fatalf("got %d markers, want 1: %+v", len(casts), casts)
	}
	b := casts[0]
	if b.Count != 5 || b.Interrupted != 2 || b.Landed() != 3 || b.Cancelled() {
		t.Errorf("Count=%d Interrupted=%d Landed=%d Cancelled=%v, want 5/2/3/false", b.Count, b.Interrupted, b.Landed(), b.Cancelled())
	}
	if b.Label() != "Dread Bolt ×3 of 5" {
		t.Errorf("Label() = %q, want Dread Bolt ×3 of 5", b.Label())
	}
}

// On a council encounter two NPCs cast the same spell. Pairing was keyed on
// the caster and the merge was not, so the second NPC's casts were folded
// into the first's marker and credited to it. Two casters are two markers —
// a behaviour decision, pinned here.
func TestSameSpellFromTwoBossesIsTwoMarkers(t *testing.T) {
	events := []event{
		bossEv(0, "cast", dreadBolt, bossA),
		bossEv(1000, "cast", dreadBolt, bossB),
		bossEv(2000, "cast", dreadBolt, bossA),
	}
	casts := buildBossCasts(events, bossNPCs, bossAbilities, fightAt(200))
	if len(casts) != 2 {
		t.Fatalf("got %d markers, want 2 (one per caster): %+v", len(casts), casts)
	}
	if casts[0].Source != "The Coiled One" || casts[0].Count != 2 || casts[1].Source != "The Other One" || casts[1].Count != 1 {
		t.Errorf("got %+v", casts)
	}
}

// Two warriors each use Rallying Cry: two windows, each credited to its own
// caster with its own head count. Before, the source was recorded once per
// ability for the whole fight and overlapping uses from different casters
// were folded into one — the raid used two cooldowns and the timeline showed
// one, credited to whoever went first.
func TestRaidCooldownFromTwoCastersIsTwoWindows(t *testing.T) {
	const rally = 97463
	actors := map[int]string{6: "Testwarrior", 22: "Testwarrior2"}
	var events []event
	// Warrior 6 at 10s on 20 players; warrior 22 at 15s on 5 — overlapping.
	for target := 1; target <= 20; target++ {
		events = append(events,
			event{Timestamp: 10000, Type: "applybuff", AbilityGameID: rally, SourceID: 6, TargetID: target},
			event{Timestamp: 20000, Type: "removebuff", AbilityGameID: rally, SourceID: 6, TargetID: target})
	}
	for target := 21; target <= 25; target++ {
		events = append(events,
			event{Timestamp: 15000, Type: "applybuff", AbilityGameID: rally, SourceID: 22, TargetID: target},
			event{Timestamp: 25000, Type: "removebuff", AbilityGameID: rally, SourceID: 22, TargetID: target})
	}
	windows := raidCooldownWindows(events, Fight{StartTime: 0, EndTime: 100000}, actors)
	if len(windows) != 2 {
		t.Fatalf("got %d windows, want 2: %+v", len(windows), windows)
	}
	if windows[0].Source != "Testwarrior" || windows[0].Targets != 20 || windows[0].Start != 10*time.Second {
		t.Errorf("first window = %+v, want the first warrior's, on 20 players, at 10s", windows[0])
	}
	if windows[1].Source != "Testwarrior2" || windows[1].Targets != 5 || windows[1].Start != 15*time.Second {
		t.Errorf("second window = %+v, want the second warrior's, on 5 players, at 15s", windows[1])
	}
}

// A player dying under Bloodlust gets no removebuff, and on a wipe that is
// most of the raid. Before, an unclosed lust interval ran to the end of the
// fight — one death turned a 40s window into a five-minute one and flagged
// every cast after it as during lust. raidCooldownWindows had the cap
// already; lust and the personal cooldowns now share it.
func TestLustWithAMissingRemoveIsCapped(t *testing.T) {
	const timeWarp = 80353
	names := map[int]string{timeWarp: "Time Warp"}
	var events []event
	for target := 1; target <= 20; target++ {
		events = append(events, event{Timestamp: 5000, Type: "applybuff", AbilityGameID: timeWarp, SourceID: 21, TargetID: target})
		if target != 7 { // player 7 died under it
			events = append(events, event{Timestamp: 45000, Type: "removebuff", AbilityGameID: timeWarp, SourceID: 21, TargetID: target})
		}
	}
	windows := lustWindows(events, Fight{StartTime: 0, EndTime: 300000}, names, nil)
	if len(windows) != 1 {
		t.Fatalf("got %d windows, want 1: %+v", len(windows), windows)
	}
	if d := windows[0].Duration(); d != 40*time.Second {
		t.Errorf("window lasted %v, want 40s (the median of the players whose buff closed)", d)
	}

	// Nobody's closed — the raid wiped under it — so the game's 40s stands in.
	var wipe []event
	for target := 1; target <= 20; target++ {
		wipe = append(wipe, event{Timestamp: 5000, Type: "applybuff", AbilityGameID: timeWarp, SourceID: 21, TargetID: target})
	}
	windows = lustWindows(wipe, Fight{StartTime: 0, EndTime: 300000}, names, nil)
	if len(windows) != 1 || windows[0].Duration() != 40*time.Second {
		t.Errorf("all-unclosed lust = %+v, want one 40s window", windows)
	}
}

// A Combustion applied at 10s and never removed in a 100s fight is not a
// 90-second Combustion.
func TestPersonalCooldownWithAMissingRemoveIsCapped(t *testing.T) {
	const combustion = 190319
	names := map[int]string{combustion: "Combustion"}
	events := []event{
		{Timestamp: 10000, Type: "applybuff", AbilityGameID: combustion, TargetID: 21},
	}
	windows := cooldownWindows(events, Fight{StartTime: 0, EndTime: 100000}, names, fire)
	if len(windows) != 1 || windows[0].Duration() != 10*time.Second {
		t.Errorf("got %+v, want one window capped at the 10s fallback", windows)
	}

	// With one closed use to learn from, the cap is that use's length.
	learned := []event{
		{Timestamp: 10000, Type: "applybuff", AbilityGameID: combustion, TargetID: 21},
		{Timestamp: 22000, Type: "removebuff", AbilityGameID: combustion, TargetID: 21},
		{Timestamp: 60000, Type: "applybuff", AbilityGameID: combustion, TargetID: 21},
	}
	windows = cooldownWindows(learned, Fight{StartTime: 0, EndTime: 100000}, names, fire)
	if len(windows) != 2 || windows[1].Duration() != 12*time.Second {
		t.Errorf("got %+v, want the second window capped at 12s like the first", windows)
	}
}

// The buff queries start at the pull, so a buff put up before it — a Blazing
// Barrier at the ready, a Pyroclasm banked, a Stampeding Roar on the run in —
// appears only as its removebuff. The API does not synthesise an applybuff at
// the boundary (checked against a real report: the recorded kill has exactly
// this Blazing Barrier). Each builder opens such a window at the fight's
// start rather than dropping it — but only for a removebuff that is the
// first thing seen for that buff and player; a later one with nothing open
// is a duplicate and is ignored, as before.
func TestABuffUpBeforeThePullIsOpenedAtTheStart(t *testing.T) {
	fight := Fight{StartTime: 1000, EndTime: 101000}
	const (
		barrier = 235313
		pyro    = 269651
		roar    = 106898
		lust    = 80353
	)
	// Personal cooldown.
	cd := cooldownWindows([]event{
		{Timestamp: 9000, Type: "removebuff", AbilityGameID: barrier, TargetID: 21},
		{Timestamp: 30000, Type: "removebuff", AbilityGameID: barrier, TargetID: 21}, // duplicate: ignored
	}, fight, map[int]string{barrier: "Blazing Barrier"}, fire)
	if len(cd) != 1 || cd[0].Start != 0 || cd[0].End != 8*time.Second {
		t.Errorf("cooldownWindows = %+v, want one window from the pull to 8s", cd)
	}
	// Proc aura.
	auras := auraWindows([]event{
		{Timestamp: 4000, Type: "removebuff", AbilityGameID: pyro, TargetID: 21},
	}, fight, fire)
	if len(auras) != 1 || auras[0].start != 0 || auras[0].end != 3*time.Second {
		t.Errorf("auraWindows = %+v, want one window from the pull to 3s", auras)
	}
	// Raid cooldown, on enough players to be one.
	var events []event
	for target := 1; target <= 6; target++ {
		events = append(events, event{Timestamp: 7000, Type: "removebuff", AbilityGameID: roar, SourceID: 7, TargetID: target})
	}
	raid := raidCooldownWindows(events, fight, map[int]string{7: "Testdruid"})
	if len(raid) != 1 || raid[0].Start != 0 || raid[0].End != 6*time.Second || raid[0].Source != "Testdruid" || raid[0].Targets != 6 {
		t.Errorf("raidCooldownWindows = %+v, want one window from the pull to 6s by Testdruid on 6", raid)
	}
	// Lust.
	events = nil
	for target := 1; target <= 6; target++ {
		events = append(events, event{Timestamp: 31000, Type: "removebuff", AbilityGameID: lust, SourceID: 21, TargetID: target})
	}
	lusts := lustWindows(events, fight, map[int]string{lust: "Time Warp"}, nil)
	if len(lusts) != 1 || lusts[0].Start != 0 || lusts[0].End != 30*time.Second {
		t.Errorf("lustWindows = %+v, want one window from the pull to 30s", lusts)
	}
}

// A series on a different grid cannot be summed into the others and is left
// out rather than misaligned. The API has never sent one; this pins the guard.
func TestBuildDPSDropsASeriesOnAnotherGrid(t *testing.T) {
	var resp dpsGraphResponse
	resp.Data.Series = append(resp.Data.Series,
		struct {
			PointStart    float64   `json:"pointStart"`
			PointInterval float64   `json:"pointInterval"`
			Data          []float64 `json:"data"`
		}{1000, 1000, []float64{100, 100}},
		struct {
			PointStart    float64   `json:"pointStart"`
			PointInterval float64   `json:"pointInterval"`
			Data          []float64 `json:"data"`
		}{1000, 500, []float64{900, 900, 900, 900}},
	)
	graph := buildDPS(resp, Fight{StartTime: 1000, EndTime: 3000})
	if graph == nil || graph.Peak != 100 {
		t.Errorf("graph = %+v, want the first series alone (peak 100)", graph)
	}
}

// The recorded kill's boss and raid cooldown lanes, with what came out
// written down. Two boss-typed NPCs, four casts that never landed, Rallying
// Cry from two warriors and Stampeding Roar from three druids: the numbers
// were read before they were pinned.
func TestGoldenRecordedKillLanes(t *testing.T) {
	rep, fight := recordedKill(t)
	names, actors, npcs := rep.names(), rep.actorNames(), rep.npcs()

	bosses := buildBossCasts(rep.BossCasts.Data, npcs, names, fight)
	interrupted := map[string]int{}
	sources := map[string]bool{}
	total := 0
	for _, b := range bosses {
		total += b.Count
		interrupted[b.Name] += b.Interrupted
		sources[b.Source] = true
	}
	if len(sources) != 2 {
		t.Errorf("boss markers come from %d NPCs, want 2", len(sources))
	}
	if interrupted["Dreadmarch"] != 1 || interrupted["Eternal Nightfall"] != 3 {
		t.Errorf("interrupted = %v, want Dreadmarch 1 and Eternal Nightfall 3", interrupted)
	}
	// 144 casts that landed plus the 4 that did not, counted from the raw
	// events by hand.
	if total != 148 {
		t.Errorf("boss casts counted = %d, want 148", total)
	}

	raid := raidCooldownWindows(rep.RaidCDs.Data, fight, actors)
	casters := map[string]map[string]bool{}
	for _, w := range raid {
		if casters[w.Name] == nil {
			casters[w.Name] = map[string]bool{}
		}
		casters[w.Name][w.Source] = true
	}
	if n := len(casters["Rallying Cry"]); n != 2 {
		t.Errorf("Rallying Cry credited to %d casters, want 2", n)
	}
	if n := len(casters["Stampeding Roar"]); n != 3 {
		t.Errorf("Stampeding Roar credited to %d casters, want 3", n)
	}
}

// The boss filter is by game id, and leaves the environment out: it is typed
// as a boss with game id 0 and a negative actor id, and buildBossCasts drops
// it — so the query must not let its casts back in.
func TestBossFilterUsesGameIDsAndSkipsTheEnvironment(t *testing.T) {
	m := masterData{NPCs: []Actor{
		{ID: -1, Name: "Environment", SubType: "Boss", GameID: 0},
		{ID: 97, Name: "Hex Lord Malacrass", SubType: "Boss", GameID: 259854},
		{ID: 28, Name: "Zul'jan", SubType: "Boss", GameID: 257911},
		{ID: 300, Name: "Venomous Add", SubType: "NPC", GameID: 111111},
	}}
	ids := m.bossIDs()
	if len(ids) != 2 || ids[0] != 257911 || ids[1] != 259854 {
		t.Errorf("bossIDs() = %v, want the two bosses' game ids in order", ids)
	}
}

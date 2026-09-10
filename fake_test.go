package main

import (
	"context"
	"errors"
	"log"
	"testing"
	"time"

	"wowinsight/internal/warcraftlogs"
)

// fakeWCL is the only stand-in for the Warcraft Logs client in these tests.
// Each method is a field, so adding one to logsClient costs one field and one
// nil check here and no edit anywhere else. Do not let a second fake appear.
type fakeWCL struct {
	report      func(ctx context.Context, code string) (*warcraftlogs.Report, error)
	rateLimit   func(ctx context.Context) (warcraftlogs.RateLimit, error)
	fightDetail func(ctx context.Context, code string, fightID int) (*warcraftlogs.FightDetail, error)
	timeline    func(ctx context.Context, code string, fight warcraftlogs.Fight, sourceID int) (*warcraftlogs.Timeline, error)
}

// errNotStubbed is what an unset method returns. A handler reaching for
// something the test did not set up is worth surfacing rather than papering
// over with a zero value — with one wrinkle: the fight handler tolerates a
// Timeline error by design, so an unstubbed Timeline is indistinguishable from
// the tolerated case. Tests that care assert on the rendered body instead.
var errNotStubbed = errors.New("fakeWCL: this method was not expected to be called")

func (f fakeWCL) Report(ctx context.Context, code string) (*warcraftlogs.Report, error) {
	if f.report == nil {
		return nil, errNotStubbed
	}
	return f.report(ctx, code)
}

func (f fakeWCL) RateLimit(ctx context.Context) (warcraftlogs.RateLimit, error) {
	if f.rateLimit == nil {
		return warcraftlogs.RateLimit{}, errNotStubbed
	}
	return f.rateLimit(ctx)
}

func (f fakeWCL) FightDetail(ctx context.Context, code string, fightID int) (*warcraftlogs.FightDetail, error) {
	if f.fightDetail == nil {
		return nil, errNotStubbed
	}
	return f.fightDetail(ctx, code, fightID)
}

func (f fakeWCL) Timeline(ctx context.Context, code string, fight warcraftlogs.Fight, sourceID int) (*warcraftlogs.Timeline, error) {
	if f.timeline == nil {
		return nil, errNotStubbed
	}
	return f.timeline(ctx, code, fight, sourceID)
}

// newTestServer wires a server with the real templates and a discarding logger,
// so a test asserting on output is not competing with log noise.
func newTestServer(t *testing.T, wcl logsClient) *server {
	t.Helper()
	tpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates() returned error: %v", err)
	}
	return newServer(wcl, tpl, log.New(discard{}, "", 0))
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Every fixture below is a function returning a fresh value, never a
// package-level var. buildCasts, auraWindows and buildPhases sort their input
// in place, and the suite runs with -shuffle=on, so a shared fixture would be
// mutated by whichever test happened to run first.

// fullTimeline returns a timeline with every lane populated, so that every
// branch of fight.html executes.
//
// The position fields are set BY HAND. This is emphatically not a golden
// render: the numbers are invented, not produced by layout(), which is
// unexported and reachable only from Client.Timeline. Hand-setting them is what
// makes {{if .CastWidthPercent}} and data-total-ms non-zero — a real log does
// not reliably exercise those branches, since a player casting only instants
// renders no cast bars at all. A positioned golden waits for #17.
func fullTimeline() *warcraftlogs.Timeline {
	sec := func(n float64) time.Duration { return time.Duration(n * float64(time.Second)) }
	return &warcraftlogs.Timeline{
		Duration:    sec(300),
		LeadIn:      sec(2),
		Total:       sec(302),
		PullPercent: 0.66,
		Casts: []warcraftlogs.Cast{
			{
				Name: "Pyroblast", AbilityID: 11366,
				Offset: sec(-1.5), End: sec(0.4), CastTime: sec(1.9),
				Precast: true, Estimated: true, HadBegincast: true,
				Percent: 0.17, CastWidthPercent: 0.63,
			},
			{
				Name: "Fireball", AbilityID: 133,
				Offset: sec(2), End: sec(4), CastTime: sec(2), HadBegincast: true,
				Gap: sec(1.6), DuringLust: true,
				Percent: 1.32, CastWidthPercent: 0.66,
				GapStartPercent: 0.79, GapWidthPercent: 0.53,
			},
			{
				Name: "Fire Blast", AbilityID: 108853,
				Offset: sec(3), End: sec(3), DuringCast: true,
				Percent: 1.66, Proc: "Hot Streak!",
			},
			{
				Name: "Scorch", AbilityID: 2948,
				Offset: sec(9), End: sec(9), HadBegincast: true, Cancelled: true,
				Percent: 3.64,
			},
			{
				Name: "Combustion", AbilityID: 190319,
				Offset: sec(12), End: sec(12), Cooldown: true, Percent: 4.64,
			},
			{
				Name: "Pyroblast", AbilityID: 11366,
				Offset: sec(20), End: sec(20), RepeatsPrevious: true,
				ProcMissing: true, Percent: 7.28,
			},
		},
		Lusts: []warcraftlogs.RaidWindow{
			{AbilityID: 80353, Name: "Time Warp", Source: "Testmage",
				Start: sec(2), End: sec(42), Targets: 20,
				StartPercent: 1.32, WidthPercent: 13.2},
		},
		Phases: []warcraftlogs.Phase{
			{ID: 1, Name: "Stage One: The Gathering", Start: 0, End: sec(150),
				StartPercent: 0.66, WidthPercent: 49.6},
			{ID: 2, Name: "Intermission", IsIntermission: true, Start: sec(150), End: sec(300),
				StartPercent: 50.3, WidthPercent: 49.6},
		},
		BossCasts: []warcraftlogs.BossCast{
			{AbilityID: 1214148, Name: "Dread Bolt", Source: "The Coiled One", Count: 5,
				Offset: sec(30), End: sec(32), Percent: 10.6, WidthPercent: 0.66},
		},
		Cooldowns: []warcraftlogs.CooldownWindow{
			{AbilityID: 190319, Name: "Combustion", Start: sec(12), End: sec(22),
				StartPercent: 4.64, WidthPercent: 3.31, Row: 0},
		},
		CooldownRows: 1,
		RaidCDs: []warcraftlogs.RaidWindow{
			{AbilityID: 97463, Name: "Rallying Cry", Source: "Testwarrior",
				Start: sec(60), End: sec(70), Targets: 20,
				StartPercent: 20.5, WidthPercent: 3.31, Row: 0},
		},
		RaidCDRows: 1,
		DPS:        graph(1_200_000, 800_000),
		Taken:      graph(90_000, 40_000),
	}
}

func graph(peak, mean float64) *warcraftlogs.DPSGraph {
	return &warcraftlogs.DPSGraph{
		Peak: peak, Mean: mean, Interval: 3 * time.Second,
		Points: []warcraftlogs.DPSPoint{
			{Offset: 0, DPS: mean, Percent: 0.66},
			{Offset: 3 * time.Second, DPS: peak, Percent: 1.65},
		},
		Line: "0.660,33.333 1.650,0.000",
		Area: "M0.660,100 L0.660,33.333 1.650,0.000 L1.650,100 Z",
	}
}

// fullFightPage returns a fight page with a player selected and every lane
// present — the shape the template is asked to render most often.
func fullFightPage() fightPageData {
	detail := fightDetail()
	player := detail.Players[0]
	return fightPageData{
		Detail:     detail,
		Fight:      detail.Fight,
		SelectedID: player.ActorID,
		Player:     &player,
		Timeline:   fullTimeline(),
	}
}

func fightDetail() *warcraftlogs.FightDetail {
	kill, heroic := false, 4
	return &warcraftlogs.FightDetail{
		ReportCode:  "ExampleReport123",
		ReportTitle: "Fixture raid night",
		Fight: warcraftlogs.Fight{
			ID: 12, Name: "The Coiled Altar", Kill: &kill, Difficulty: &heroic,
			StartTime: 1000, EndTime: 301000, BossPercentage: 18.4,
			FriendlyPlayers: []int{7, 11},
		},
		Players: []warcraftlogs.PlayerStats{
			{ActorID: 7, Name: "Testmage", Server: "Testrealm", Class: "Mage", Spec: "Fire",
				ItemLevel: 678, Damage: 240_000_000, Healing: 0, Deaths: 0,
				ActiveTime: 250 * time.Second, FightDuration: 300 * time.Second},
			{ActorID: 11, Name: "Testpriest", Server: "Testrealm", Class: "Priest", Spec: "Holy",
				ItemLevel: 675, Damage: 4_000_000, Healing: 90_000_000, Overheal: 30_000_000,
				Deaths: 1, ActiveTime: 200 * time.Second, FightDuration: 300 * time.Second},
		},
	}
}

// reportWithTwoPulls is the shape the index page renders: one kill, one wipe,
// so both outcome classes appear.
func reportWithTwoPulls() *warcraftlogs.Report {
	kill, wipe := true, false
	heroic := 4
	r := &warcraftlogs.Report{
		Code: "ExampleReport123", Title: "Fixture raid night",
		StartTime: 1_700_000_000_000, EndTime: 1_700_000_600_000,
		Fights: []warcraftlogs.Fight{
			{ID: 12, Name: "The Coiled Altar", Kill: &kill, Difficulty: &heroic,
				StartTime: 1000, EndTime: 301000},
			{ID: 19, Name: "The Coiled Altar", Kill: &wipe, Difficulty: &heroic,
				StartTime: 400000, EndTime: 560000},
		},
	}
	r.Owner.Name = "Testmage"
	r.Zone.Name = "The Venomous Abyss"
	return r
}

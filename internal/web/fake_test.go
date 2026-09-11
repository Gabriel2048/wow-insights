package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"wowinsight/internal/view"
	"wowinsight/internal/warcraftlogs"
)

// fakeWCL is the only stand-in for the Warcraft Logs client in these tests.
// Each method is a field, so adding one to logsClient costs one field and one
// nil check here and no edit anywhere else. Do not let a second fake appear.
type fakeWCL struct {
	report      func(ctx context.Context, code string) (*warcraftlogs.Report, error)
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
func newTestServer(t *testing.T, wcl logsClient) *Server {
	t.Helper()
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatalf("ParseTemplates() returned error: %v", err)
	}
	return New(wcl, tpl, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// Every fixture below is a function returning a fresh value, never a
// package-level var. buildCasts, auraWindows and buildPhases sort their input
// in place, and the suite runs with -shuffle=on, so a shared fixture would be
// mutated by whichever test happened to run first.

// fullTimeline returns an analysed timeline with every lane populated, so
// that every branch of fight.html executes once it is laid out. The analysis
// carries no positions; view.Layout computes them, which is why the
// fixture need not invent any — the precast is what makes the lead-in and
// the cast bars non-zero.
func fullTimeline() *warcraftlogs.Timeline {
	sec := func(n float64) time.Duration { return time.Duration(n * float64(time.Second)) }
	return &warcraftlogs.Timeline{
		Duration: sec(300),
		Casts: []warcraftlogs.Cast{
			{
				Name: "Pyroblast", AbilityID: 11366,
				Offset: sec(-1.5), End: sec(0.4), CastTime: sec(1.9),
				Precast: true, Estimated: true, HadBegincast: true,
			},
			{
				Name: "Fireball", AbilityID: 133,
				Offset: sec(2), End: sec(4), CastTime: sec(2), HadBegincast: true,
				Gap: sec(1.6), DuringLust: true,
			},
			{
				Name: "Fire Blast", AbilityID: 108853,
				Offset: sec(3), End: sec(3), DuringCast: true, Proc: "Hot Streak!",
			},
			{
				Name: "Scorch", AbilityID: 2948,
				Offset: sec(9), End: sec(9), HadBegincast: true, Cancelled: true,
			},
			{
				Name: "Combustion", AbilityID: 190319,
				Offset: sec(12), End: sec(12), Cooldown: true,
			},
			{
				Name: "Pyroblast", AbilityID: 11366,
				Offset: sec(20), End: sec(20), RepeatsPrevious: true,
				ProcMissing: true,
			},
		},
		Lusts: []warcraftlogs.RaidWindow{
			{AbilityID: 80353, Name: "Time Warp", Source: "Testmage",
				Start: sec(2), End: sec(42), Targets: 20},
		},
		Phases: []warcraftlogs.Phase{
			{ID: 1, Name: "Stage One: The Gathering", Start: 0, End: sec(150)},
			{ID: 2, Name: "Intermission", IsIntermission: true, Start: sec(150), End: sec(300)},
		},
		BossCasts: []warcraftlogs.BossCast{
			{AbilityID: 1214148, Name: "Dread Bolt", Source: "The Coiled One", Count: 5,
				Offset: sec(30), End: sec(32)},
		},
		Cooldowns: []warcraftlogs.CooldownWindow{
			{AbilityID: 190319, Name: "Combustion", Start: sec(12), End: sec(22)},
		},
		RaidCDs: []warcraftlogs.RaidWindow{
			{AbilityID: 97463, Name: "Rallying Cry", Source: "Testwarrior",
				Start: sec(60), End: sec(70), Targets: 20},
		},
		DPS:   graph(1_200_000, 800_000),
		Taken: graph(90_000, 40_000),
	}
}

func graph(peak, mean float64) *warcraftlogs.DPSGraph {
	return &warcraftlogs.DPSGraph{
		Peak: peak, Mean: mean, Interval: 3 * time.Second,
		Points: []warcraftlogs.DPSPoint{
			{Offset: 0, DPS: mean},
			{Offset: 3 * time.Second, DPS: peak},
		},
	}
}

// fullFightPage returns a fight page with a player selected and every lane
// present — the shape the template is asked to render most often. The axis
// is fixed at a 2s lead-in so the ruler's x-domain is a known number.
func fullFightPage() fightPageData {
	detail := fightDetail()
	player := detail.Players[0]
	return fightPageData{
		Detail:     detail,
		Fight:      view.Fight{Fight: detail.Fight},
		SelectedID: player.ActorID,
		Player:     &player,
		Timeline:   laidOut(fullTimeline()),
	}
}

// pageWith is fullFightPage with a different analysis drawn on it.
func pageWith(t *warcraftlogs.Timeline) fightPageData {
	page := fullFightPage()
	page.Timeline = laidOut(t)
	return page
}

// laidOut draws an analysis the way the page fixture wants it drawn.
func laidOut(t *warcraftlogs.Timeline) *view.Timeline {
	return view.Layout(t, view.Options{LeadIn: 2 * time.Second})
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

package warcraftlogs

import (
	"testing"

	"wowinsight/internal/knowledge"
)

// fire is the Fire Mage tables, the spec the analysis was written against.
// Every test that judges procs or cooldowns analyses with the real table, so
// a change to it shows up here, not only on the page.
var fire = func() knowledge.Knowledge {
	k, ok := knowledge.Lookup(knowledge.SpecID{Class: "Mage", Spec: "Fire"})
	if !ok {
		panic("the Fire Mage tables are missing")
	}
	return k
}()

// pyroblastID is the spell the Fire Mage rule judges.
const pyroblastID = 11366

// An unauthored spec is analysed with the zero tables. Everything that does
// not depend on the spec still comes out of the recorded kill; everything
// that does is absent rather than wrong — and the query never asks for it.
func TestUnknownSpecKeepsTheClassAgnosticLanes(t *testing.T) {
	rep, fight := recordedKill(t)
	var none knowledge.Knowledge

	known := buildTimeline(rep, rep.Casts.Data, fight, fire)
	unknown := buildTimeline(rep, rep.Casts.Data, fight, none)

	if len(unknown.Casts) != len(known.Casts) {
		t.Errorf("casts: %d with the zero tables, %d with Fire Mage's", len(unknown.Casts), len(known.Casts))
	}
	for _, lane := range []struct {
		name   string
		k, u   int
		agnost bool
	}{
		{"lusts", len(known.Lusts), len(unknown.Lusts), true},
		{"raid cooldowns", len(known.RaidCDs), len(unknown.RaidCDs), true},
		{"phases", len(known.Phases), len(unknown.Phases), true},
		{"boss casts", len(known.BossCasts), len(unknown.BossCasts), true},
		{"personal cooldowns", len(known.Cooldowns), len(unknown.Cooldowns), false},
	} {
		switch {
		case lane.k == 0:
			t.Errorf("%s: the recording holds none, so this proves nothing", lane.name)
		case lane.agnost && lane.u != lane.k:
			t.Errorf("%s: %d with the zero tables, %d with Fire Mage's; the lane is class-agnostic", lane.name, lane.u, lane.k)
		case !lane.agnost && lane.u != 0:
			t.Errorf("%s: %d with the zero tables, want none", lane.name, lane.u)
		}
	}

	judged, flagged := 0, 0
	for _, c := range known.Casts {
		if c.Proc != "" || c.ProcMissing {
			judged++
		}
		if c.Cooldown {
			flagged++
		}
	}
	if judged == 0 || flagged == 0 {
		t.Fatalf("Fire Mage's tables judged %d casts and flagged %d cooldowns on the recording; the comparison below needs both", judged, flagged)
	}
	for _, c := range unknown.Casts {
		if c.Proc != "" || c.ProcExpired != "" || c.ProcMissing || c.Cooldown {
			t.Errorf("with the zero tables, %s at %s was judged: %+v", c.Name, c.Timestamp(), c)
			break
		}
	}

	vars := timelineVars("ExampleReport123", fight, 21, nil, none)
	for _, filter := range []string{"procs", "cooldowns"} {
		if _, sent := vars[filter]; sent {
			t.Errorf("the query carries a %s filter for a spec with no tables: %v", filter, vars[filter])
		}
	}
	for _, filter := range []string{"lust", "raidCDs"} {
		if _, sent := vars[filter]; !sent {
			t.Errorf("the query dropped the class-agnostic %s filter", filter)
		}
	}
}

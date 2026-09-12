package warcraftlogs

import (
	"slices"
	"testing"
)

func TestPlayerTitle(t *testing.T) {
	cases := []struct {
		class, spec, want string
	}{
		{"DeathKnight", "Blood", "Blood Death Knight"},
		{"Hunter", "BeastMastery", "Beast Mastery Hunter"},
		{"DemonHunter", "Havoc", "Havoc Demon Hunter"},
		{"Priest", "", "Priest"},
	}
	for _, c := range cases {
		p := PlayerStats{Class: c.class, Spec: c.spec}
		if got := p.Title(); got != c.want {
			t.Errorf("Title() with class %q spec %q = %q, want %q", c.class, c.spec, got, c.want)
		}
	}
}

func TestPlayerRates(t *testing.T) {
	p := PlayerStats{Damage: 1000, Healing: 500, Overheal: 500, FightDuration: 10e9, ActiveTime: 5e9}
	if got := p.DPS(); got != 100 {
		t.Errorf("DPS() = %v, want 100", got)
	}
	if got := p.ActivePercent(); got != 50 {
		t.Errorf("ActivePercent() = %v, want 50", got)
	}
	if got := p.OverhealPercent(); got != 50 {
		t.Errorf("OverhealPercent() = %v, want 50", got)
	}
	// A zero-length fight must not divide by zero.
	if got := (PlayerStats{Damage: 1000}).DPS(); got != 0 {
		t.Errorf("DPS() with no duration = %v, want 0", got)
	}
}

func TestSpecFromIcon(t *testing.T) {
	for icon, want := range map[string]string{
		"Mage-Fire":         "Fire",
		"DeathKnight-Blood": "Blood",
		"Paladin":           "", // no spec half at all
		"Paladin-":          "", // a hyphen and nothing after it
		"Mage-Fire-Extra":   "Fire",
		"":                  "",
	} {
		if got := specFromIcon(icon); got != want {
			t.Errorf("specFromIcon(%q) = %q, want %q", icon, got, want)
		}
	}
}

// Two players can share a name — on different realms, or as the same person
// twice in a split raid. The roster comes out of a map, so the order must be
// total or the dropdown reorders between requests.
func TestRosterOrderIsTotal(t *testing.T) {
	report := &fightDetailReport{}
	report.Fights = []fightWire{{ID: 1, StartTime: 0, EndTime: 1000, FriendlyPlayers: []int{3, 1, 2, 4}}}
	report.MasterData.Actors = []Actor{
		{ID: 3, Name: "testmage", Type: "Player", SubType: "Mage"},
		{ID: 1, Name: "Testmage", Type: "Player", SubType: "Mage"},
		{ID: 2, Name: "Testmage", Type: "Player", SubType: "Mage"},
		{ID: 4, Name: "Alpha", Type: "Player", SubType: "Priest"},
	}
	var first []int
	for run := range 20 {
		var ids []int
		for _, p := range buildFightDetail(report).Players {
			ids = append(ids, p.ActorID)
		}
		if first == nil {
			first = ids
			// Alpha first; then the Testmages, upper case before lower,
			// and the two identical names by actor id.
			if want := []int{4, 1, 2, 3}; !slices.Equal(ids, want) {
				t.Fatalf("order = %v, want %v", ids, want)
			}
		} else if !slices.Equal(ids, first) {
			t.Fatalf("run %d ordered %v, run 0 ordered %v", run, ids, first)
		}
	}
}

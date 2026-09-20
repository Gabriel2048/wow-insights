package warcraftlogs

import (
	"encoding/json"
	"math"
	"os"
	"slices"
	"testing"
	"time"

	"wowinsight/internal/knowledge"
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
	p := PlayerStats{Damage: 1000, Healing: 500, Overheal: 500, FightDuration: 10e9, DamageUptime: 5e9}
	if got := p.DPS(); got != 100 {
		t.Errorf("DPS() = %v, want 100", got)
	}
	if got := p.DamageUptimePercent(); got != 50 {
		t.Errorf("DamageUptimePercent() = %v, want 50", got)
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

// The merge behind the stats cards: damage and active time from the damage
// table, healing and overheal from the healing table, deaths counted, spec
// and item level from whichever table lists the player first, and a class
// the master data did not know filled in from the table.
func TestBuildFightDetailMergesTheTables(t *testing.T) {
	report := &fightDetailReport{}
	report.Fights = []fightWire{{ID: 1, StartTime: 0, EndTime: 100000, FriendlyPlayers: []int{1, 2, 3}}}
	report.MasterData.Actors = []Actor{
		{ID: 1, Name: "Testmage", Type: "Player", SubType: "Mage"},
		{ID: 2, Name: "Testpriest", Type: "Player", SubType: unknownClass},
		{ID: 3, Name: "Testrogue", Type: "Player", SubType: "Rogue"},
		{ID: 9, Name: "Testpet", Type: "Pet", SubType: "Cat"},
	}
	report.Damage.Data.Entries = []tableEntry{
		{ID: 1, Type: "Mage", Icon: "Mage-Fire", ItemLevel: 300, Total: 5000, ActiveTime: 80000},
		{ID: 2, Type: "Priest", Icon: "Priest-Holy", ItemLevel: 290, Total: 10, ActiveTime: 1000},
		{ID: 9, Type: "Pet", Icon: "custom-icon-cat", Total: 1},
	}
	report.Healing.Data.Entries = []tableEntry{
		{ID: 2, Type: "Priest", Icon: "Priest-Holy", ItemLevel: 290, Total: 8000, Overheal: 2000, ActiveTime: 90000},
	}
	report.Deaths.Data.Entries = []tableEntry{{ID: 2}, {ID: 2}, {ID: 9}}

	detail := buildFightDetail(report)
	want := map[int]PlayerStats{
		1: {ActorID: 1, Name: "Testmage", Class: "Mage", Spec: "Fire", ItemLevel: 300, Damage: 5000, DamageUptime: 80 * time.Second, FightDuration: 100 * time.Second},
		2: {ActorID: 2, Name: "Testpriest", Class: "Priest", Spec: "Holy", ItemLevel: 290, Damage: 10, Healing: 8000, Overheal: 2000, Deaths: 2, DamageUptime: time.Second, FightDuration: 100 * time.Second},
		// In neither table: the class from the master data, no spec.
		3: {ActorID: 3, Name: "Testrogue", Class: "Rogue", FightDuration: 100 * time.Second},
	}
	if len(detail.Players) != len(want) {
		t.Fatalf("%d players, want %d (the pet is not one): %+v", len(detail.Players), len(want), detail.Players)
	}
	for _, p := range detail.Players {
		if p != want[p.ActorID] {
			t.Errorf("player %d = %+v\n     want %+v", p.ActorID, p, want[p.ActorID])
		}
	}
}

// Every spec the recorded roster carries is spelt the way the catalogue
// spells it — the one place the catalogue's spellings meet real bytes — and
// no player comes out with the master data's "Unknown" as a class.
func TestRecordedRosterSpecsAreCatalogued(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/fight-1.json")
	if err != nil {
		skipWithoutRecording(t, err)
	}
	var env struct{ Data fightDetailResponse }
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	detail := buildFightDetail(env.Data.ReportData.Report)
	specs := map[knowledge.SpecID]bool{}
	for _, p := range detail.Players {
		if p.Class == unknownClass || p.Class == "" {
			t.Errorf("%s has class %q", p.Name, p.Class)
		}
		if p.Spec == "" {
			continue
		}
		if !knowledge.Catalogued(p.SpecID()) {
			t.Errorf("%s is %+v, which the catalogue does not spell that way", p.Name, p.SpecID())
		}
		specs[p.SpecID()] = true
	}
	if len(specs) < 12 {
		t.Errorf("only %d distinct specs in the recorded roster; the check is too thin to mean much", len(specs))
	}
}

// A ranked pull reaches the roster. The join is on name because the id on a
// ranked row is the character's global Warcraft Logs id, not this report's
// actor id — see rankings.go. A player the rankings do not mention keeps the
// zero value, and Ranked() is what tells the page which is which.
func TestBuildFightDetailAttachesRankingsByName(t *testing.T) {
	report := &fightDetailReport{}
	report.Fights = []fightWire{{ID: 4, StartTime: 0, EndTime: 100000, FriendlyPlayers: []int{1, 2}}}
	report.MasterData.Actors = []Actor{
		{ID: 1, Name: "Testmage", Type: "Player", SubType: "Mage"},
		{ID: 2, Name: "Testrogue", Type: "Player", SubType: "Rogue"},
	}
	// The id on a ranked row is deliberately nothing like actor 1: joining on
	// it would match nobody. This is also the real wire shape, rank included.
	report.Rankings = []byte(`{"data":[{"fightID":4,"roles":{"dps":{"characters":[
		{"id":14133569,"name":"Testmage","rank":"~412","rankPercent":55.4,"bracketPercent":61.2,"totalParses":440,"bracketData":326}]}}}]}`)

	detail := buildFightDetail(report)
	mage, ok := detail.Player(1)
	if !ok {
		t.Fatal("actor 1 is not in the roster")
	}
	if !mage.Ranked() {
		t.Fatalf("Testmage is unranked, want ranked: %+v", mage.Ranking)
	}
	if mage.Ranking.TotalParses != 440 || mage.Ranking.Percentile() != "61st" {
		t.Errorf("got %s of %d parses, want 61st of 440", mage.Ranking.Percentile(), mage.Ranking.TotalParses)
	}
	rogue, _ := detail.Player(2)
	if rogue.Ranked() {
		t.Errorf("Testrogue is ranked, want unranked: the rankings do not mention them")
	}
}

// PlayerStats is compared field-wise by the table test above, so it must stay
// free of pointer fields — Fight already holds them and cannot be a map key
// because of it. AGENTS.md asks that new work not deepen that.
func TestPlayerStatsStaysComparable(t *testing.T) {
	a := PlayerStats{ActorID: 1, Name: "Testmage", Ranking: Ranking{TotalParses: 440, BracketPercent: 61.2}}
	b := PlayerStats{ActorID: 1, Name: "Testmage", Ranking: Ranking{TotalParses: 440, BracketPercent: 61.2}}
	if a != b {
		t.Error("two PlayerStats with the same fields are not equal; a pointer field has crept in")
	}
	// This line does not compile at all if PlayerStats stops being
	// comparable, which is the stronger half of the assertion.
	if !map[PlayerStats]bool{a: true}[b] {
		t.Error("PlayerStats does not work as a map key")
	}
}

// Damage uptime is not casting uptime, and the name is the whole point of
// this test existing. On the committed recording nearly every damage dealer
// reads between 99.5% and 99.9% — impossible as time spent pressing buttons,
// ordinary as time dealing damage, because a damage-over-time effect and a
// pet both keep it running while their owner stands still.
//
// It matters because #52 reports tens of seconds of real pauses on the same
// pull. Two numbers that are both right read as a contradiction when one of
// them is misnamed.
func TestDamageUptimeIsNotCastingUptime(t *testing.T) {
	report := &fightDetailReport{}
	report.Fights = []fightWire{{ID: 1, StartTime: 0, EndTime: 100000, FriendlyPlayers: []int{1}}}
	report.MasterData.Actors = []Actor{{ID: 1, Name: "Testmage", Type: "Player", SubType: "Mage"}}
	report.Damage.Data.Entries = []tableEntry{
		{ID: 1, Type: "Mage", Icon: "Mage-Fire", Total: 5000, ActiveTime: 97100},
	}

	p, ok := buildFightDetail(report).Player(1)
	if !ok {
		t.Fatal("actor 1 is not in the roster")
	}
	if got := p.DamageUptimePercent(); math.Abs(got-97.1) > 0.001 {
		t.Errorf("DamageUptimePercent() = %v, want 97.1", got)
	}
	// A pull nobody was in must not divide by zero.
	if got := (PlayerStats{DamageUptime: time.Second}).DamageUptimePercent(); got != 0 {
		t.Errorf("a zero-length pull reports %v uptime, want 0", got)
	}
}

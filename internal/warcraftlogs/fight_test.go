package warcraftlogs

import "testing"

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

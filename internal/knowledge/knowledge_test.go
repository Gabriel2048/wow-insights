package knowledge

import (
	"slices"
	"testing"
)

// Every row of the table is checked for the things a typo would break: a
// rule that names an aura the spec does not track would never match, and an
// id in two tables at once would be counted twice.
func TestEveryAuthoredSpecIsConsistent(t *testing.T) {
	if len(specs) == 0 {
		t.Fatal("the table is empty")
	}
	for _, k := range specs {
		t.Run(k.Spec.Spec+" "+k.Spec.Class, func(t *testing.T) {
			if k.Spec.Class == "" || k.Spec.Spec == "" {
				t.Fatalf("Spec = %+v, want both halves", k.Spec)
			}
			names := map[string]bool{}
			for id, name := range k.ProcAuras {
				if id <= 0 || name == "" {
					t.Errorf("ProcAuras[%d] = %q", id, name)
				}
				names[name] = true
			}
			for id, name := range k.Cooldowns {
				if id <= 0 || name == "" {
					t.Errorf("Cooldowns[%d] = %q", id, name)
				}
				if _, both := k.ProcAuras[id]; both {
					t.Errorf("%d is both a proc aura and a cooldown", id)
				}
			}
			for spell, rule := range k.CastRules {
				if spell <= 0 {
					t.Errorf("CastRules[%d]", spell)
				}
				if len(rule.Instant)+len(rule.HardCast) == 0 {
					t.Errorf("CastRules[%d] names no aura", spell)
				}
				for _, name := range slices.Concat(rule.Instant, rule.HardCast) {
					if !names[name] {
						t.Errorf("CastRules[%d] names %q, which ProcAuras does not track", spell, name)
					}
				}
			}
			if got, ok := Lookup(k.Spec); !ok || got.Spec != k.Spec {
				t.Errorf("Lookup(%+v) = %+v, %v", k.Spec, got.Spec, ok)
			}
		})
	}
}

func TestFireMageIsTheReferenceSpec(t *testing.T) {
	k, ok := Lookup(SpecID{Class: "Mage", Spec: "Fire"})
	if !ok {
		t.Fatal("Fire Mage is not in the table")
	}
	if name, _ := k.ProcAura(48108); name != "Hot Streak!" {
		t.Errorf("ProcAura(48108) = %q, want Hot Streak!", name)
	}
	if !k.IsCooldown(190319) {
		t.Error("Combustion is not a cooldown")
	}
	rule, ok := k.Rule(11366)
	if !ok || !slices.Equal(rule.Instant, []string{"Hyperthermia", "Hot Streak!"}) || !slices.Equal(rule.HardCast, []string{"Pyroclasm"}) {
		t.Errorf("Rule(Pyroblast) = %+v, %v", rule, ok)
	}
	if got := k.ProcAuraIDs(); !slices.IsSorted(got) || len(got) != 4 {
		t.Errorf("ProcAuraIDs() = %v, want four sorted ids", got)
	}
	if got := k.CooldownIDs(); !slices.IsSorted(got) || len(got) != 5 {
		t.Errorf("CooldownIDs() = %v, want five sorted ids", got)
	}
}

// The zero value is what an unauthored spec analyses with, so it must answer
// every question with "no" and never with a panic.
func TestZeroKnowledgeIsSafe(t *testing.T) {
	var k Knowledge
	if name, ok := k.ProcAura(48108); ok || name != "" {
		t.Errorf("ProcAura = %q, %v", name, ok)
	}
	if k.IsCooldown(190319) {
		t.Error("IsCooldown = true")
	}
	if _, ok := k.Rule(11366); ok {
		t.Error("Rule found one")
	}
	if k.ProcAuraIDs() != nil || k.CooldownIDs() != nil {
		t.Error("ids are not nil, so a query filter would be sent")
	}
	if _, ok := Lookup(SpecID{Class: "Warlock", Spec: "Destruction"}); ok {
		t.Error("Lookup found a spec nobody authored")
	}
}

func TestTheTableRejectsADuplicateSpec(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("index() accepted the same spec twice")
		}
	}()
	index([]Knowledge{fireMage, fireMage})
}

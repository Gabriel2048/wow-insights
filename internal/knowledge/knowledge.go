// Package knowledge holds what the analysis knows about each specialisation:
// which auras explain how a spell was cast, and which of the player's own
// cooldowns are worth picking out of the rotation. It is data, not behaviour —
// every spec differs in ids and names, none in what is done with them — so
// Knowledge is a value type and adding a spec is a file and a table row.
//
// The package imports nothing else in this module. The analysis reads it; it
// never reads the analysis. ARCHITECTURE.md draws the edge and the gate
// verifies it.
package knowledge

import "sort"

// SpecID names a specialisation the way Warcraft Logs does: the class as an
// actor's subType ("Mage", "DeathKnight") and the spec as the second half of
// a table icon ("Fire", "Blood"). It is comparable, so it keys maps directly.
type SpecID struct {
	Class string
	Spec  string
}

// CastRule says which auras explain a cast of one spell, in the order they
// are reported when several are up at once. A cast that resolved instantly is
// judged against Instant; one with a cast bar against HardCast.
type CastRule struct {
	Instant  []string
	HardCast []string
}

// Knowledge is one specialisation's tables. The zero value is a spec nothing
// is known about, and every method on it says so: no aura is tracked, no
// ability is a cooldown, no cast has a rule. An unknown spec therefore
// degrades to the class-agnostic lanes and asks the API for nothing extra.
type Knowledge struct {
	Spec SpecID
	// ProcAuras are the auras that explain how a spell was cast, by aura id.
	// The name is what a CastRule refers to; two ids can share one name when
	// the game reports the same effect under several ids.
	ProcAuras map[int]string
	// Cooldowns are the player's own cooldowns worth picking out of the
	// rotation, by ability or aura id. The name here documents the table; the
	// page prints the name the log reports.
	Cooldowns map[int]string
	// CastRules are the spells whose cast quality the proc auras can judge,
	// by spell id. A spell with no rule is not judged: for it, an aura being
	// up says nothing about why it was cast.
	CastRules map[int]CastRule
}

// ProcAura reports whether an aura is tracked, and its name if it is.
func (k Knowledge) ProcAura(id int) (string, bool) {
	name, ok := k.ProcAuras[id]
	return name, ok
}

// IsCooldown reports whether an ability is one of the player's own cooldowns.
func (k Knowledge) IsCooldown(id int) bool {
	_, ok := k.Cooldowns[id]
	return ok
}

// Rule reports the cast rule for a spell, if it has one.
func (k Knowledge) Rule(spell int) (CastRule, bool) {
	rule, ok := k.CastRules[spell]
	return rule, ok
}

// ProcAuraIDs is the sorted set of tracked aura ids, so a query built from it
// is stable. Nil when nothing is tracked.
func (k Knowledge) ProcAuraIDs() []int { return sortedKeys(k.ProcAuras) }

// CooldownIDs is the sorted set of cooldown ids. Nil when there are none.
func (k Knowledge) CooldownIDs() []int { return sortedKeys(k.Cooldowns) }

func sortedKeys(m map[int]string) []int {
	if len(m) == 0 {
		return nil
	}
	ids := make([]int, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

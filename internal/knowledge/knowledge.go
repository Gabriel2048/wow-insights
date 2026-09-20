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

import (
	"maps"
	"slices"
	"time"
)

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
	// JudgedCooldowns are the cooldowns whose *spacing* is worth judging —
	// the ones a player is expected to use on cooldown for damage. It is
	// deliberately a smaller set than Cooldowns, which is everything worth
	// picking out of the rotation: judging a defensive produces nonsense.
	// On the recorded kill Blazing Barrier's shortest gap between uses is
	// 7.6 seconds, which would claim forty-one missed uses.
	JudgedCooldowns map[int]JudgedCooldown

	// BaseCasts are how long the game makes a spell take with no haste, by
	// spell id. They exist so the global cooldown can be modelled: haste is
	// an observed cast bar over its base, and the global cooldown is hasted
	// by exactly the same multiplier.
	//
	// The table need not be complete, and weighting it by coverage would be
	// false precision — one spell out of three was measured to give the same
	// answer as two. What it must not contain is a number nobody can check:
	// a base that is wrong by 15% moves the reported pause count by a third
	// at the tightest threshold, as a scatter of one-second pauses through
	// otherwise perfect casting.
	BaseCasts map[int]BaseCast
}

// BaseCast is how long one spell occupies the player before the next can
// begin, with no haste.
type BaseCast struct {
	// Base is the cast bar, or for a channel its full length.
	Base time.Duration
	// Channel marks a spell the log reports as an instant even though it
	// takes time. Warcraft Logs emits no begincast for a channel, so in the
	// event stream a three-second Mind Flay is byte-identical to a Shadow
	// Word: Death and nothing but this flag can tell them apart. A channel
	// is therefore useless for measuring haste — there is no bar to measure —
	// and must still be counted as time the player was busy.
	Channel bool
}

// BaseCast reports the base cast time of a spell, if the spec has one.
func (k Knowledge) BaseCast(spell int) (BaseCast, bool) {
	b, ok := k.BaseCasts[spell]
	return b, ok && b.Base > 0
}

// JudgedCooldown is one cooldown the analysis holds a player to.
type JudgedCooldown struct {
	Name string
	// Base is the cooldown the game gives the ability with no talents, in
	// seconds. It is a bound, not the answer: talents shorten cooldowns and
	// the log does not say which the player took, so the analysis measures
	// the player's own shortest gap between uses and trusts that instead —
	// Base only stops a reset or a double-charge from being read as a
	// two-second cooldown.
	Base int
}

// Judged reports the cooldown rule for an ability, if its spacing is judged.
func (k Knowledge) Judged(ability int) (JudgedCooldown, bool) {
	cd, ok := k.JudgedCooldowns[ability]
	return cd, ok
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
// is stable. Empty when nothing is tracked.
func (k Knowledge) ProcAuraIDs() []int { return slices.Sorted(maps.Keys(k.ProcAuras)) }

// CooldownIDs is the sorted set of cooldown ids. Empty when there are none.
func (k Knowledge) CooldownIDs() []int { return slices.Sorted(maps.Keys(k.Cooldowns)) }

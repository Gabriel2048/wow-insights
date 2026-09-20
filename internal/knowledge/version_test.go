package knowledge

import (
	"testing"
	"time"
)

// A cache keyed on a version must be invalidated by any edit that changes an
// analysis, and by nothing else.
func TestVersionChangesWithEveryTableThatChangesAnAnalysis(t *testing.T) {
	base := func() Knowledge {
		return Knowledge{
			Spec:            SpecID{Class: "Mage", Spec: "Fire"},
			ProcAuras:       map[int]string{48108: "Hot Streak!", 269651: "Pyroclasm"},
			Cooldowns:       map[int]string{190319: "Combustion"},
			CastRules:       map[int]CastRule{11366: {Instant: []string{"Hyperthermia", "Hot Streak!"}, HardCast: []string{"Pyroclasm"}}},
			JudgedCooldowns: map[int]JudgedCooldown{190319: {Name: "Combustion", Base: 120}},
			BaseCasts:       map[int]BaseCast{133: {Base: 1750 * time.Millisecond}},
		}
	}
	was := base().Version()

	edits := map[string]func(*Knowledge){
		"a new proc aura":           func(k *Knowledge) { k.ProcAuras[1242220] = "Hyperthermia" },
		"a renamed proc aura":       func(k *Knowledge) { k.ProcAuras[48108] = "Hot Streak" },
		"a new cooldown":            func(k *Knowledge) { k.Cooldowns[235313] = "Blazing Barrier" },
		"a new cast rule":           func(k *Knowledge) { k.CastRules[133] = CastRule{HardCast: []string{"Pyroclasm"}} },
		"a changed judged cooldown": func(k *Knowledge) { k.JudgedCooldowns[190319] = JudgedCooldown{Name: "Combustion", Base: 60} },
		"a different spec entirely": func(k *Knowledge) { k.Spec = SpecID{Class: "Mage", Spec: "Frost"} },
		"a new base cast time":      func(k *Knowledge) { k.BaseCasts[2948] = BaseCast{Base: 1500 * time.Millisecond} },
		"a changed base cast time": func(k *Knowledge) {
			k.BaseCasts[133] = BaseCast{Base: 2 * time.Second}
		},
		"a spell that turns out to be a channel": func(k *Knowledge) {
			k.BaseCasts[133] = BaseCast{Base: 1750 * time.Millisecond, Channel: true}
		},
		"an aura moved to hard cast": func(k *Knowledge) {
			k.CastRules[11366] = CastRule{Instant: []string{"Hyperthermia"}, HardCast: []string{"Hot Streak!", "Pyroclasm"}}
		},
	}
	for what, edit := range edits {
		k := base()
		edit(&k)
		if got := k.Version(); got == was {
			t.Errorf("%s did not change the version (%s); anything cached against it would go stale silently", what, got)
		}
	}
}

// THE ONE THAT MATTERS. CastRule.Instant and HardCast are ordered lists — the
// analysis names the first aura in them that was up — so reordering them
// changes what the page says about a cast. A digest that sorted them would
// call these two tables identical and serve one's answer for the other.
func TestReorderingAnOrderedRuleChangesTheVersion(t *testing.T) {
	forwards := Knowledge{CastRules: map[int]CastRule{
		11366: {Instant: []string{"Hyperthermia", "Hot Streak!"}},
	}}
	backwards := Knowledge{CastRules: map[int]CastRule{
		11366: {Instant: []string{"Hot Streak!", "Hyperthermia"}},
	}}
	if forwards.Version() == backwards.Version() {
		t.Error("two rules naming the same auras in different orders share a version; the order decides which aura the page credits, so this is a stale answer waiting to happen")
	}

	// And the two lists are distinct even when one is empty: an aura that
	// justifies an instant does not justify a hard cast.
	instant := Knowledge{CastRules: map[int]CastRule{11366: {Instant: []string{"Hot Streak!"}}}}
	hard := Knowledge{CastRules: map[int]CastRule{11366: {HardCast: []string{"Hot Streak!"}}}}
	if instant.Version() == hard.Version() {
		t.Error("an Instant rule and a HardCast rule naming the same aura share a version")
	}
}

// Go ranges a map in a random order. A digest that walked the maps directly
// would differ between two calls in one process, and every cache entry would
// miss.
func TestVersionIsStableAcrossCalls(t *testing.T) {
	first, _ := Lookup(SpecID{Class: "Mage", Spec: "Fire"})
	want := first.Version()
	for range 50 {
		if got := first.Version(); got != want {
			t.Fatalf("Version() = %q then %q; a map was walked unsorted", want, got)
		}
	}
}

// Renaming two auras must not be able to produce the digest of the original,
// which is what length-prefixing the hashed fields prevents.
func TestAuraNamesCannotBeShuffledAcrossFields(t *testing.T) {
	a := Knowledge{ProcAuras: map[int]string{1: "ab", 2: "c"}}
	b := Knowledge{ProcAuras: map[int]string{1: "a", 2: "bc"}}
	if a.Version() == b.Version() {
		t.Error("two different tables hash the same; the fields are being concatenated without lengths")
	}
}

// "Nobody has authored this spec" is a real state a result can be computed
// under, so it needs a version of its own.
func TestTheZeroValueHasItsOwnVersion(t *testing.T) {
	zero := Knowledge{}.Version()
	if zero == "" {
		t.Fatal("the zero value has no version")
	}
	if fire, _ := Lookup(SpecID{Class: "Mage", Spec: "Fire"}); fire.Version() == zero {
		t.Error("an authored spec shares a version with the unauthored zero value")
	}
}

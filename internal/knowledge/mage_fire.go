package knowledge

import "time"

// fireMage is the reference spec: the data the analysis was written against.
var fireMage = Knowledge{
	Spec: SpecID{Class: "Mage", Spec: "Fire"},

	// Pyroblast is instant under Hot Streak or Hyperthermia, and a hard cast
	// is only worth doing under Pyroclasm.
	ProcAuras: map[int]string{
		48108:   "Hot Streak!",
		269651:  "Pyroclasm",
		1242220: "Hyperthermia",
		383874:  "Hyperthermia",
		// Measured rather than remembered: across eighteen boss pulls, every
		// removal of each of these landed on the player's own cast of one
		// spell, and every such cast was instant. Glorious Incandescence was
		// consumed by Fire Blast 465 times of 468; Heat Shimmer by Scorch 151
		// times. Glorious Incandescence is a Sunfury hero-talent aura, so a
		// Frostfire Fire Mage never gets it — which is harmless, because an
		// aura that never comes up produces no ledger row.
		451073: "Glorious Incandescence",
		458964: "Heat Shimmer",
	},

	// 235314 and 1265927 are the absorb effects that share the Blazing
	// Barrier name, kept so a log that reports the cast under a different id
	// still matches.
	Cooldowns: map[int]string{
		190319:  "Combustion",
		235313:  "Blazing Barrier",
		235314:  "Blazing Barrier",
		1265927: "Blazing Barrier",
		414658:  "Ice Cold",
	},

	// Combustion is the only Fire Mage cooldown whose spacing is worth
	// judging. Blazing Barrier and Ice Cold are defensives — a player holding
	// one for the mechanic that is coming is playing correctly, and calling
	// that drift would be wrong. 120 seconds is the untalented cooldown;
	// Kindling shortens it, which is why the analysis measures rather than
	// assumes (on the recorded kill this player's own shortest gap is 61 s).
	JudgedCooldowns: map[int]JudgedCooldown{
		190319: {Name: "Combustion", Base: 120},
	},

	// Two entries, and deliberately only two. Both bases are the game's, and
	// the recorded kill independently confirms their ratio: reading each
	// Fireball bar against the nearest Scorch bar within six seconds recovers
	// a base with a median of 1750 ms over 26 samples.
	//
	// Pyroblast is absent on purpose. The same method gives it a median of
	// 2576 ms across a 2525-3046 ms range — a 20% spread that Fireball and
	// Scorch do not have, so something other than haste is moving that bar.
	// Adding it changes no reported pause on either recording and introduces
	// a number that cannot be defended. Flamestrike is absent because neither
	// recording contains a single bar of it.
	BaseCasts: map[int]BaseCast{
		133:  {Base: 1750 * time.Millisecond}, // Fireball
		2948: {Base: 1500 * time.Millisecond}, // Scorch
	},

	// Several auras can be up at once, and which ones matter depends on the
	// cast. An instant Pyroblast is spent from Hot Streak, and Hyperthermia
	// also makes it instant while ramping its damage; when both are up Hot
	// Streak is still consumed, so both are worth naming. A hard cast is only
	// justified by Pyroclasm, which is checked at the start of the cast but
	// consumed when it lands. Pyroclasm survives a Pyroblast cast under
	// Hyperthermia.
	CastRules: map[int]CastRule{
		// Pyroclasm is spent only by a hard cast. With Hot Streak and
		// Pyroclasm both up, pressing Pyroblast casts it instantly and
		// Pyroclasm stays, so the instant list must not name it.
		11366: { // Pyroblast
			Instant:           []string{"Hyperthermia", "Hot Streak!"},
			HardCast:          []string{"Pyroclasm"},
			HardCastNeedsProc: true,
		},
		// Flamestrike spends a Hot Streak exactly as Pyroblast does, and is
		// what a Fire Mage spends it on when there is more than one target.
		// Without this row every Flamestrike in an AoE burst reads as a cast
		// with no proc behind it: on the recorded wipe, seven Hot Streaks
		// counted as wasted in a stretch where all seven were spent
		// correctly.
		//
		// Pyroclasm improves a hard-cast Flamestrike just as it does a
		// Pyroblast — but unlike Pyroblast, a hard-cast Flamestrike without it
		// is ordinary AoE and not a mistake, so HardCastNeedsProc is false.
		1254851: { // Flamestrike
			Instant:  []string{"Hyperthermia", "Hot Streak!"},
			HardCast: []string{"Pyroclasm"},
		},
		2948: { // Scorch
			Instant: []string{"Heat Shimmer"},
		},
		108853: { // Fire Blast
			Instant: []string{"Glorious Incandescence"},
		},
	},
}

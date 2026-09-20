package knowledge

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

	// Several auras can be up at once, and which ones matter depends on the
	// cast. An instant Pyroblast is spent from Hot Streak, and Hyperthermia
	// also makes it instant while ramping its damage; when both are up Hot
	// Streak is still consumed, so both are worth naming. A hard cast is only
	// justified by Pyroclasm, which is checked at the start of the cast but
	// consumed when it lands. Pyroclasm survives a Pyroblast cast under
	// Hyperthermia.
	CastRules: map[int]CastRule{
		11366: { // Pyroblast
			Instant:  []string{"Hyperthermia", "Hot Streak!"},
			HardCast: []string{"Pyroclasm"},
		},
	},
}

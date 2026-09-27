package knowledge

// catalogue is every specialisation, spelt the way Warcraft Logs spells it:
// the class as an actor's subType and the spec as the second half of a table
// icon, both CamelCase with no spaces ("DeathKnight-Blood",
// "Hunter-BeastMastery"). A Knowledge whose Spec is not in here can never be
// looked up, so the table test checks every authored row against it.
var catalogue = map[SpecID]bool{}

func init() {
	for class, specs := range map[string][]string{
		"DeathKnight": {"Blood", "Frost", "Unholy"},
		// Devourer is the third Demon Hunter specialisation. It was missing
		// until the recording was re-taken from a raid night that had one in
		// it (#81), and a player of it got "unknown spec" handling instead.
		"DemonHunter": {"Havoc", "Vengeance", "Devourer"},
		"Druid":       {"Balance", "Feral", "Guardian", "Restoration"},
		"Evoker":      {"Devastation", "Preservation", "Augmentation"},
		"Hunter":      {"BeastMastery", "Marksmanship", "Survival"},
		"Mage":        {"Arcane", "Fire", "Frost"},
		"Monk":        {"Brewmaster", "Mistweaver", "Windwalker"},
		"Paladin":     {"Holy", "Protection", "Retribution"},
		"Priest":      {"Discipline", "Holy", "Shadow"},
		"Rogue":       {"Assassination", "Outlaw", "Subtlety"},
		"Shaman":      {"Elemental", "Enhancement", "Restoration"},
		"Warlock":     {"Affliction", "Demonology", "Destruction"},
		"Warrior":     {"Arms", "Fury", "Protection"},
	} {
		for _, spec := range specs {
			catalogue[SpecID{Class: class, Spec: spec}] = true
		}
	}
}

// Catalogued reports whether id is a specialisation the game has, spelt as
// Warcraft Logs spells it.
func Catalogued(id SpecID) bool { return catalogue[id] }

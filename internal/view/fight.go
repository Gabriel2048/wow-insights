package view

import (
	"wowinsight/internal/warcraftlogs"
)

// Fight is a pull as a page describes it: the domain value plus what only a
// page needs — the id Wowhead's tooltips want, which is not a fact about the
// fight but about a third party's URL scheme.
type Fight struct {
	warcraftlogs.Fight
}

// wowheadDifficulties maps Warcraft Logs difficulty IDs to the game's own
// difficulty IDs, which Wowhead takes as its "dd" tooltip parameter. Without
// it Wowhead shows Mythic values for every spell.
var wowheadDifficulties = map[int]int{
	1: 17, // Looking For Raid
	3: 14, // Normal
	4: 15, // Heroic
	5: 16, // Mythic
}

// WowheadDifficulty is the difficulty id to ask Wowhead for, or 0 when the
// fight has no difficulty we can map.
func (f Fight) WowheadDifficulty() int {
	if f.Difficulty == nil {
		return 0
	}
	return wowheadDifficulties[*f.Difficulty]
}

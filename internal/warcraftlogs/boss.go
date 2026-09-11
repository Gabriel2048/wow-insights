package warcraftlogs

import (
	"fmt"
	"sort"
	"time"
)

// bossSubType is the master-data classification for an encounter's own NPCs,
// as opposed to the adds that fight alongside them.
const bossSubType = "Boss"

// bossBurstWindow groups repeats of one ability into a single marker. Filler
// casts arrive in clusters — Dread Bolt fires 78 times in this fight — and
// drawing each one would bury the mechanics worth reacting to.
const bossBurstWindow = 3 * time.Second

// BossCast is one boss ability, or a burst of the same ability, on the timeline.
type BossCast struct {
	Offset    time.Duration
	End       time.Duration
	AbilityID int
	Name      string
	Source    string
	Count     int

	Percent      float64
	WidthPercent float64
}

// CastTime is how long the boss spent casting it.
func (b BossCast) CastTime() time.Duration { return b.End - b.Offset }

// Label names the ability, noting how many casts a burst covers.
func (b BossCast) Label() string {
	if b.Count > 1 {
		return fmt.Sprintf("%s ×%d", b.Name, b.Count)
	}
	return b.Name
}

// Timestamp renders the first cast of the group as m:ss.
func (b BossCast) Timestamp() string { return formatOffset(b.Offset) }

// buildBossCasts turns enemy cast events into timeline markers. Only the
// encounter's own NPCs are kept: adds and the environment cast constantly and
// say little about the fight's structure.
func buildBossCasts(events []event, npcs map[int]Actor, names map[int]string, fight Fight) []BossCast {
	events = sortedByTime(events)

	relative := func(t float64) time.Duration {
		return time.Duration(t-fight.StartTime) * time.Millisecond
	}

	type entry struct {
		start, end time.Duration
		ability    int
		source     string
	}
	var entries []entry
	pending := map[[2]int]int{} // ability+source -> index of an unfinished cast

	for _, e := range events {
		npc, known := npcs[e.SourceID]
		// A negative source id is the environment, which is typed as a boss but
		// is not one.
		if !known || npc.SubType != bossSubType || e.SourceID <= 0 {
			continue
		}
		at := relative(e.Timestamp)
		key := [2]int{e.AbilityGameID, e.SourceID}
		switch e.Type {
		case "begincast":
			entries = append(entries, entry{at, at, e.AbilityGameID, npc.Name})
			pending[key] = len(entries) - 1
		case "cast":
			if i, open := pending[key]; open {
				delete(pending, key)
				entries[i].end = at
				continue
			}
			entries = append(entries, entry{at, at, e.AbilityGameID, npc.Name})
		}
	}

	sort.SliceStable(entries, func(i, j int) bool { return entries[i].start < entries[j].start })

	var casts []BossCast
	latest := map[int]int{}           // ability -> index of its most recent marker
	lastAt := map[int]time.Duration{} // ability -> when it last went off
	for _, e := range entries {
		// Chain off the previous cast, not the start of the group, so a long
		// run of filler stays one marker instead of splitting every 3s.
		i, seen := latest[e.ability]
		if seen && e.start-lastAt[e.ability] <= bossBurstWindow {
			casts[i].Count++
			if e.end > casts[i].End {
				casts[i].End = e.end
			}
			lastAt[e.ability] = e.start
			continue
		}
		name := names[e.ability]
		if name == "" {
			name = fmt.Sprintf("Spell %d", e.ability)
		}
		casts = append(casts, BossCast{
			Offset: e.start, End: e.end, AbilityID: e.ability,
			Name: name, Source: e.source, Count: 1,
		})
		latest[e.ability] = len(casts) - 1
		lastAt[e.ability] = e.start
	}
	return casts
}

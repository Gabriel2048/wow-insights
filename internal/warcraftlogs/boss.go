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

// BossCast is one boss ability, or a burst of the same ability from the same
// NPC, on the timeline.
type BossCast struct {
	Offset    time.Duration
	End       time.Duration
	AbilityID int
	Name      string
	Source    string
	Count     int
	// Interrupted is how many of Count began and never landed — kicked, or
	// cut off by a phase change. For a wipe post-mortem "did we kick it" is
	// the first question, and a marker that counted a kicked cast as a
	// landed one answered it wrong.
	Interrupted int
}

// CastTime is how long the boss spent casting it.
func (b BossCast) CastTime() time.Duration { return b.End - b.Offset }

// Landed is how many casts in the burst actually resolved.
func (b BossCast) Landed() int { return b.Count - b.Interrupted }

// Cancelled reports whether nothing in the marker landed.
func (b BossCast) Cancelled() bool { return b.Interrupted == b.Count }

// Label names the ability, noting how many casts a burst covers and how many
// of those were stopped.
func (b BossCast) Label() string {
	switch {
	case b.Count == 1 && b.Interrupted == 1:
		return b.Name + " (interrupted)"
	case b.Interrupted > 0:
		return fmt.Sprintf("%s ×%d of %d", b.Name, b.Landed(), b.Count)
	case b.Count > 1:
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
		start, end  time.Duration
		ability     int
		sourceID    int
		source      string
		interrupted bool
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
			// A bar still pending when the same NPC begins the same spell
			// again was stopped, not finished.
			if i, open := pending[key]; open {
				entries[i].interrupted = true
			}
			entries = append(entries, entry{start: at, end: at, ability: e.AbilityGameID, sourceID: e.SourceID, source: npc.Name})
			pending[key] = len(entries) - 1
		case "cast":
			if i, open := pending[key]; open {
				delete(pending, key)
				entries[i].end = at
				continue
			}
			entries = append(entries, entry{start: at, end: at, ability: e.AbilityGameID, sourceID: e.SourceID, source: npc.Name})
		}
	}
	// Whatever is still pending when the stream ends never landed either.
	for _, i := range pending {
		entries[i].interrupted = true
	}

	sort.SliceStable(entries, func(i, j int) bool { return entries[i].start < entries[j].start })

	var casts []BossCast
	// A burst is one NPC repeating one ability. Keyed on both, as the
	// pairing above is: on a council encounter two NPCs casting the same
	// spell are two markers, not one credited to whoever cast first.
	type burst struct{ ability, source int }
	latest := map[burst]int{}           // -> index of its most recent marker
	lastAt := map[burst]time.Duration{} // -> when it last went off
	for _, e := range entries {
		k := burst{e.ability, e.sourceID}
		// Chain off the previous cast, not the start of the group, so a long
		// run of filler stays one marker instead of splitting every 3s.
		i, seen := latest[k]
		if seen && e.start-lastAt[k] <= bossBurstWindow {
			casts[i].Count++
			if e.interrupted {
				casts[i].Interrupted++
			}
			if e.end > casts[i].End {
				casts[i].End = e.end
			}
			lastAt[k] = e.start
			continue
		}
		name := names[e.ability]
		if name == "" {
			name = fmt.Sprintf("Spell %d", e.ability)
		}
		interrupted := 0
		if e.interrupted {
			interrupted = 1
		}
		casts = append(casts, BossCast{
			Offset: e.start, End: e.end, AbilityID: e.ability,
			Name: name, Source: e.source, Count: 1, Interrupted: interrupted,
		})
		latest[k] = len(casts) - 1
		lastAt[k] = e.start
	}
	return casts
}

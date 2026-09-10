package warcraftlogs

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// lustAbilityIDs are the raid-wide haste effects: Bloodlust, Heroism, Time
// Warp, and their pet, Evoker and drum equivalents.
var lustAbilityIDs = []int{
	2825,   // Bloodlust (Shaman, Horde)
	32182,  // Heroism (Shaman, Alliance)
	80353,  // Time Warp (Mage)
	390386, // Fury of the Aspects (Evoker)
	264667, // Primal Rage (Hunter pet)
	90355,  // Ancient Hysteria (Core Hound)
	160452, // Netherwinds (Nether Ray)
	146555, // Drums of Rage
	178207, // Drums of Fury
	230935, // Drums of the Mountain
	256740, // Drums of the Maelstrom
	309658, // Drums of Deathly Ferocity
}

// procAuras are the auras that explain how a spell was cast. The set is Fire
// mage specific for now: Pyroblast is instant under Hot Streak or Hyperthermia,
// and a hard cast is only worth doing under Pyroclasm.
var procAuras = map[int]string{
	48108:   "Hot Streak!",
	269651:  "Pyroclasm",
	1242220: "Hyperthermia",
	383874:  "Hyperthermia",
}

// personalCooldowns are the player's own cooldowns worth picking out of the
// rotation. Fire mage for now; 235314 and 1265927 are the absorb effects that
// share the Blazing Barrier name, kept so a log that reports the cast under a
// different id still matches.
var personalCooldowns = map[int]bool{
	190319:  true, // Combustion
	235313:  true, // Blazing Barrier
	235314:  true,
	1265927: true,
	414658:  true, // Ice Cold
}

// pyroblastID is the spell whose cast quality we can judge from those auras.
const pyroblastID = 11366

// procSlack absorbs the tie between a cast and the aura it consumes: the
// removebuff lands on, or just after, the millisecond of the cast that spent
// it. It is applied only to the closing edge of a window. An aura that begins
// after a cast cannot have enabled it, so the opening edge gets a single
// millisecond of tolerance for events sharing a timestamp.
const (
	procSlack      = 100 * time.Millisecond
	procStartSlack = time.Millisecond
)

// raidCooldownAuras are the group-wide cooldowns anyone in the raid might use.
// They are curated rather than detected: some land on the whole raid (Rallying
// Cry, Anti-Magic Zone), while others are channelled and leave their buff only
// on the caster (Tranquility, Divine Hymn), so counting how many players got
// the buff would throw the second kind away. Single-target externals such as
// Ironbark and Guardian Spirit are deliberately absent: they are not raid-wide.
var raidCooldownAuras = map[int]string{
	97463:  "Rallying Cry",
	145629: "Anti-Magic Zone",
	374227: "Zephyr",
	106898: "Stampeding Roar",
	77764:  "Stampeding Roar",
	31821:  "Aura Mastery",
	740:    "Tranquility",
	64843:  "Divine Hymn",
	196718: "Darkness",
	81782:  "Power Word: Barrier",
	98008:  "Spirit Link Totem",
	115310: "Revival",
	363534: "Rewind",
}

// raidCooldownMinWindow drops windows too short to be a real use: a buff
// applied and removed in the same instant leaves a segment with no width.
const raidCooldownMinWindow = 250 // milliseconds

// defaultRaidCooldownLength caps an unclosed buff when nothing comparable ever
// closed, so it cannot run to the end of the fight.
const defaultRaidCooldownLength = 10000 // milliseconds

// raidCooldownIDs is the sorted key set, so the query is stable.
func raidCooldownIDs() []int {
	ids := make([]int, 0, len(raidCooldownAuras))
	for id := range raidCooldownAuras {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

// raidCooldownWindows turns raid cooldown buffs into windows. Unlike lust, no
// minimum number of targets applies: the curated list is the filter, and a
// channelled cooldown only ever buffs its caster.
func raidCooldownWindows(events []event, fight Fight, actors map[int]string) []RaidWindow {
	sort.SliceStable(events, func(i, j int) bool { return events[i].Timestamp < events[j].Timestamp })

	type key struct{ ability, target int }
	// closed records whether a removebuff was actually seen. Players who die,
	// or leave the log, can leave a buff open; extending those to the end of
	// the fight would stretch an 8s cooldown across minutes.
	type interval struct {
		start, end float64
		closed     bool
	}

	opened := map[key]float64{}
	intervals := map[int][]interval{}
	targets := map[int]map[int]bool{}
	sources := map[int]int{}

	for _, e := range events {
		if _, tracked := raidCooldownAuras[e.AbilityGameID]; !tracked {
			continue
		}
		k := key{e.AbilityGameID, e.TargetID}
		switch e.Type {
		case "applybuff", "refreshbuff":
			if _, open := opened[k]; !open {
				opened[k] = e.Timestamp
			}
			if targets[e.AbilityGameID] == nil {
				targets[e.AbilityGameID] = map[int]bool{}
			}
			targets[e.AbilityGameID][e.TargetID] = true
			if _, ok := sources[e.AbilityGameID]; !ok && e.SourceID != 0 {
				sources[e.AbilityGameID] = e.SourceID
			}
		case "removebuff":
			if start, open := opened[k]; open {
				delete(opened, k)
				intervals[e.AbilityGameID] = append(intervals[e.AbilityGameID], interval{start, e.Timestamp, true})
			}
		}
	}
	for k, start := range opened {
		intervals[k.ability] = append(intervals[k.ability], interval{start, fight.EndTime, false})
	}

	var windows []RaidWindow
	for abilityID, list := range intervals {
		sort.Slice(list, func(i, j int) bool { return list[i].start < list[j].start })

		// An interval left open by a player who died or dropped out of the log
		// is capped at how long the buff usually lasts. Running it to the end
		// of the fight would stretch an 8s cooldown across minutes.
		// The median of the intervals that did close is the best available
		// estimate of how long this cooldown lasts.
		var lengths []float64
		for _, iv := range list {
			if iv.closed {
				lengths = append(lengths, iv.end-iv.start)
			}
		}
		typical := float64(defaultRaidCooldownLength)
		if len(lengths) > 0 {
			sort.Float64s(lengths)
			typical = lengths[len(lengths)/2]
		}
		for i := range list {
			if !list[i].closed && list[i].start+typical < list[i].end {
				list[i].end = list[i].start + typical
			}
		}

		// Overlapping intervals are one use. Players wander in and out of a
		// ground effect like Anti-Magic Zone, so a re-entry belongs to the
		// placement it happened inside, not to a new one.
		merged := []interval{list[0]}
		for _, iv := range list[1:] {
			last := &merged[len(merged)-1]
			if iv.start <= last.end {
				if iv.end > last.end {
					last.end = iv.end
				}
				continue
			}
			merged = append(merged, iv)
		}
		for _, iv := range merged {
			if iv.end-iv.start < raidCooldownMinWindow {
				continue // a flicker, not a use
			}
			windows = append(windows, RaidWindow{
				AbilityID: abilityID,
				Name:      raidCooldownAuras[abilityID],
				Source:    actors[sources[abilityID]],
				Start:     time.Duration(iv.start-fight.StartTime) * time.Millisecond,
				End:       time.Duration(iv.end-fight.StartTime) * time.Millisecond,
				Targets:   len(targets[abilityID]),
			})
		}
	}
	sort.Slice(windows, func(i, j int) bool { return windows[i].Start < windows[j].Start })
	return windows
}

// raidLustMinTargets is how many players a haste buff must land on before it
// counts as a raid lust. Some abilities in this list also exist as personal
// effects that reuse the same spell ID; requiring a raid-wide application
// keeps those out of the timeline.
const raidLustMinTargets = 5

// labelCollision is how close a repeat of the same spell has to be to the end
// of the previous cast before their labels overlap on the timeline.
const labelCollision = 50 * time.Millisecond

// precastWindow is how far into a pull an unpaired cast can land and still be
// read as a precast. A spell started before the pull lands within roughly one
// cast bar of it; later unpaired casts are more likely a gap in the log.
const precastWindow = 5 * time.Second

// Cast is a single spell cast, positioned relative to the start of the fight.
// A hard cast is one Cast built from a begincast/cast pair, not two rows.
type Cast struct {
	Offset     time.Duration // when the cast started
	End        time.Duration // when it landed; equal to Offset for instants
	AbilityID  int
	Name       string
	CastTime   time.Duration // time spent casting
	Gap        time.Duration // idle time since the previous cast finished
	DuringLust bool
	Percent    float64 // position along the fight, 0-100

	// HadBegincast distinguishes a spell the game considers castable but which
	// resolved instantly (a proc) from one that is inherently instant.
	HadBegincast bool

	// Cancelled marks a cast that started and never landed, because it was
	// interrupted, moved out of, or replaced.
	Cancelled bool

	// Precast marks a spell that landed just after the pull but whose cast bar
	// began before it, so the begincast falls outside the logged fight.
	Precast bool

	// Estimated marks a cast whose bar is inferred rather than logged. Only a
	// precast can be estimated: its start is derived from how long the same
	// spell took to cast elsewhere in the fight.
	Estimated bool

	// Cooldown marks one of the player's own cooldowns, so the timeline can
	// pick it out of the surrounding filler.
	Cooldown bool

	// DuringCast marks a cast that began while another was still in progress,
	// which only off-GCD spells usable while casting can do. Fire Blast woven
	// into a Fireball is the common case.
	DuringCast bool

	// RepeatsPrevious marks an instant recast of the same spell landing on the
	// tail of the previous cast's bar, so the timeline can skip its label: it
	// would otherwise print the same name against both ends of one bar. A
	// repeat that has a bar of its own keeps its label, because it is a
	// separate cast the eye needs to separate.
	RepeatsPrevious bool

	// Proc names the aura that made this cast worth taking, when one was up.
	Proc string

	// ProcMissing marks a hard cast that had no aura justifying it, which for
	// Pyroblast means a cast that should not have been made.
	ProcMissing bool

	// ProcExpired names an aura that was up when the cast began but had run
	// out before it landed. The damage bonus applies on impact, so the cast
	// gained nothing and the proc was wasted.
	ProcExpired string

	// Positions for the timeline track.
	GapStartPercent  float64
	GapWidthPercent  float64
	CastWidthPercent float64
}

// IsInstant reports whether the spell never entered a cast bar at all.
func (c Cast) IsInstant() bool { return !c.HadBegincast }

// ProcLabel describes why the cast was taken: the aura that enabled it, or
// "no proc" for a hard cast that had none.
func (c Cast) ProcLabel() string {
	switch {
	case c.Proc != "":
		return c.Proc
	case c.ProcExpired != "":
		return c.ProcExpired + " expired"
	case c.ProcMissing:
		return "no proc"
	}
	return ""
}

// CastLabel renders the cast time column: "cancelled", "instant", or a
// duration. A 0.0s value means the spell had a cast bar that resolved
// immediately, which is what a proc looks like.
func (c Cast) CastLabel() string {
	switch {
	case c.Cancelled:
		return "cancelled"
	case c.Precast:
		return "precast"
	case !c.HadBegincast:
		return "instant"
	default:
		return fmt.Sprintf("%.1fs", c.CastTime.Seconds())
	}
}

// Timestamp renders the cast's position as m:ss.
func (c Cast) Timestamp() string { return formatOffset(c.Offset) }

// IdleFrom is when the idle period preceding this cast began, i.e. the moment
// the previous cast finished.
func (c Cast) IdleFrom() time.Duration { return c.Offset - c.Gap }

// IdleFromLabel renders the start of that idle period as m:ss.
func (c Cast) IdleFromLabel() string { return formatOffset(c.IdleFrom()) }

// GapMS is the idle time in milliseconds, for the client-side threshold that
// decides which pauses are worth showing.
func (c Cast) GapMS() int64 { return c.Gap.Milliseconds() }

// RaidWindow is a period during which a raid-wide haste buff was active.
type RaidWindow struct {
	AbilityID    int
	Name         string
	Source       string // who cast it, when known
	Start        time.Duration
	End          time.Duration
	Targets      int
	StartPercent float64
	WidthPercent float64

	// Row is the stacking row within the raid cooldown lane. Two cooldowns
	// running at once are drawn one above the other rather than on top of
	// each other.
	Row int
}

// Duration is how long the buff was up.
func (l RaidWindow) Duration() time.Duration { return l.End - l.Start }

// Label describes the window for display, e.g. "Time Warp (Testmage)".
func (l RaidWindow) Label() string {
	if l.Source == "" {
		return l.Name
	}
	return fmt.Sprintf("%s (%s)", l.Name, l.Source)
}

// Timestamp renders the window's start as m:ss.
func (l RaidWindow) Timestamp() string { return formatOffset(l.Start) }

// CooldownWindow is how long one of the player's cooldowns was actually up.
// Its length is the buff, not the cast: a Blazing Barrier that absorbs its
// shield early ends early.
type CooldownWindow struct {
	AbilityID int
	Name      string
	Start     time.Duration
	End       time.Duration

	StartPercent float64
	WidthPercent float64

	// Row is the stacking row, so two cooldowns up at once are drawn one above
	// the other rather than overlapping.
	Row int
}

// Duration is how long the cooldown lasted.
func (c CooldownWindow) Duration() time.Duration { return c.End - c.Start }

// Timestamp renders the moment it went up as m:ss.
func (c CooldownWindow) Timestamp() string { return formatOffset(c.Start) }

// cooldownWindows pairs apply and remove events into the spans a cooldown was
// up for. One still running when the fight ends is closed at the end.
func cooldownWindows(events []event, fight Fight, names map[int]string) []CooldownWindow {
	sort.SliceStable(events, func(i, j int) bool { return events[i].Timestamp < events[j].Timestamp })

	relative := func(t float64) time.Duration {
		return time.Duration(t-fight.StartTime) * time.Millisecond
	}
	open := map[int]time.Duration{}
	var windows []CooldownWindow
	add := func(abilityID int, start, end time.Duration) {
		name := names[abilityID]
		if name == "" {
			name = fmt.Sprintf("Spell %d", abilityID)
		}
		windows = append(windows, CooldownWindow{
			AbilityID: abilityID, Name: name, Start: start, End: end,
		})
	}

	for _, e := range events {
		if !personalCooldowns[e.AbilityGameID] {
			continue
		}
		switch e.Type {
		case "applybuff", "refreshbuff":
			if _, up := open[e.AbilityGameID]; !up {
				open[e.AbilityGameID] = relative(e.Timestamp)
			}
		case "removebuff":
			if start, up := open[e.AbilityGameID]; up {
				delete(open, e.AbilityGameID)
				add(e.AbilityGameID, start, relative(e.Timestamp))
			}
		}
	}
	for id, start := range open {
		add(id, start, fight.Duration())
	}
	sort.Slice(windows, func(i, j int) bool { return windows[i].Start < windows[j].Start })
	return windows
}

// Phase is a named section of an encounter, positioned within one pull.
type Phase struct {
	ID             int
	Name           string
	IsIntermission bool
	Start          time.Duration
	End            time.Duration
	StartPercent   float64
	WidthPercent   float64
}

// Duration is how long the phase lasted in this pull.
func (p Phase) Duration() time.Duration { return p.End - p.Start }

// Timestamp renders the phase start as m:ss.
func (p Phase) Timestamp() string { return formatOffset(p.Start) }

// ShortName drops the descriptive half of a phase name, turning
// "Stage Two: Usurper's Reprisal" into "Stage Two" for the timeline label.
func (p Phase) ShortName() string {
	if before, _, found := strings.Cut(p.Name, ":"); found {
		return strings.TrimSpace(before)
	}
	return p.Name
}

// Section is a phase together with the casts that fell inside it.
type Section struct {
	Phase Phase
	Casts []Cast
}

// Timeline is one player's casts across a fight, with the raid lust windows
// that overlap them.
type Timeline struct {
	Casts     []Cast
	Lusts     []RaidWindow
	Phases    []Phase
	BossCasts []BossCast
	Cooldowns []CooldownWindow
	RaidCDs   []RaidWindow
	DPS       *DPSGraph

	// Taken is the damage the player received, on the same bucket grid as DPS.
	Taken *DPSGraph

	// Duration is the length of the fight itself. The drawn timeline starts
	// LeadIn before the pull, so a cast begun beforehand has somewhere to be
	// drawn, and spans Total.
	Duration time.Duration
	LeadIn   time.Duration
	Total    time.Duration

	// PullPercent is where the pull sits along the drawn timeline.
	PullPercent float64

	// RaidCDRows and CooldownRows are how many stacking rows each cooldown
	// lane needs.
	RaidCDRows   int
	CooldownRows int
}

// packCooldownWindows stacks the player's own cooldowns the same way, so a
// cooldown starting as another ends does not have its label hidden.
func packCooldownWindows(windows []CooldownWindow) int {
	var rowEnds []time.Duration
	for i := range windows {
		placed := false
		for row, end := range rowEnds {
			if windows[i].Start >= end {
				windows[i].Row = row
				rowEnds[row] = windows[i].End
				placed = true
				break
			}
		}
		if !placed {
			windows[i].Row = len(rowEnds)
			rowEnds = append(rowEnds, windows[i].End)
		}
	}
	return len(rowEnds)
}

// packRaidWindows assigns each window the first row already free at the moment
// it starts, so overlapping cooldowns stack instead of colliding. Windows must
// already be sorted by start. It returns the number of rows used.
func packRaidWindows(windows []RaidWindow) int {
	var rowEnds []time.Duration
	for i := range windows {
		placed := false
		for row, end := range rowEnds {
			if windows[i].Start >= end {
				windows[i].Row = row
				rowEnds[row] = windows[i].End
				placed = true
				break
			}
		}
		if !placed {
			windows[i].Row = len(rowEnds)
			rowEnds = append(rowEnds, windows[i].End)
		}
	}
	return len(rowEnds)
}

// minLeadIn keeps a sliver of pre-pull room even when nothing was precast, so
// the pull reads as a moment on a timeline rather than a hard edge.
const minLeadIn = 1500 * time.Millisecond

// leadInMargin is the breathing room left to the left of the earliest cast.
const leadInMargin = 750 * time.Millisecond

// layout fixes the drawn time span and converts every offset into a percentage
// of it. Positions are computed in one place so the casts, phases, lust
// windows and DPS curve cannot drift out of alignment.
func (t *Timeline) layout() {
	t.LeadIn = minLeadIn
	for _, c := range t.Casts {
		if c.Offset < 0 && -c.Offset+leadInMargin > t.LeadIn {
			t.LeadIn = -c.Offset + leadInMargin
		}
	}
	t.Total = t.LeadIn + t.Duration
	if t.Total <= 0 {
		return
	}
	t.PullPercent = t.percent(0)

	for i := range t.Casts {
		c := &t.Casts[i]
		c.Percent = t.percent(c.Offset)
		c.CastWidthPercent = t.span(c.CastTime)
		c.GapWidthPercent = t.span(c.Gap)
		c.GapStartPercent = c.Percent - c.GapWidthPercent
	}
	for i := range t.Lusts {
		l := &t.Lusts[i]
		l.StartPercent = t.percent(l.Start)
		l.WidthPercent = t.span(l.End - l.Start)
	}
	for i := range t.Phases {
		p := &t.Phases[i]
		p.StartPercent = t.percent(p.Start)
		p.WidthPercent = t.span(p.End - p.Start)
	}
	t.RaidCDRows = packRaidWindows(t.RaidCDs)
	for i := range t.RaidCDs {
		r := &t.RaidCDs[i]
		r.StartPercent = t.percent(r.Start)
		r.WidthPercent = t.span(r.End - r.Start)
	}
	t.CooldownRows = packCooldownWindows(t.Cooldowns)
	for i := range t.Cooldowns {
		c := &t.Cooldowns[i]
		c.StartPercent = t.percent(c.Start)
		c.WidthPercent = t.span(c.End - c.Start)
	}
	for i := range t.BossCasts {
		b := &t.BossCasts[i]
		b.Percent = t.percent(b.Offset)
		b.WidthPercent = t.span(b.End - b.Offset)
	}
	for _, graph := range []*DPSGraph{t.DPS, t.Taken} {
		if graph == nil {
			continue
		}
		for i := range graph.Points {
			graph.Points[i].Percent = t.percent(graph.Points[i].Offset)
		}
		graph.Line, graph.Area = plot(graph.Points, graph.Peak)
	}
}

// percent places a fight-relative offset along the drawn timeline.
func (t *Timeline) percent(offset time.Duration) float64 {
	return 100 * float64(offset+t.LeadIn) / float64(t.Total)
}

// span converts a length of time into a width along the drawn timeline.
func (t *Timeline) span(d time.Duration) float64 {
	return 100 * float64(d) / float64(t.Total)
}

// TotalMS is the drawn span in milliseconds, for the timeline ruler.
func (t *Timeline) TotalMS() int64 { return t.Total.Milliseconds() }

// LeadInMS is the pre-pull span in milliseconds, for the timeline ruler.
func (t *Timeline) LeadInMS() int64 { return t.LeadIn.Milliseconds() }

// Sections groups the casts by phase. When the encounter has no phase data,
// every cast lands in a single unnamed section.
func (t *Timeline) Sections() []Section {
	if len(t.Phases) == 0 {
		return []Section{{Casts: t.Casts}}
	}
	sections := make([]Section, len(t.Phases))
	for i, phase := range t.Phases {
		sections[i].Phase = phase
	}
	for _, cast := range t.Casts {
		// Phases are contiguous, so the last one starting at or before the
		// cast owns it.
		index := 0
		for i, phase := range t.Phases {
			if cast.Offset >= phase.Start {
				index = i
			}
		}
		sections[index].Casts = append(sections[index].Casts, cast)
	}
	return sections
}

// DurationMS is the fight length in milliseconds, for the timeline ruler.
func (t *Timeline) DurationMS() int64 { return t.Duration.Milliseconds() }

// CancelledCount is how many casts started but never landed.
func (t *Timeline) CancelledCount() int {
	n := 0
	for _, c := range t.Casts {
		if c.Cancelled {
			n++
		}
	}
	return n
}

// LongestGap is the worst single pause in the fight.
func (t *Timeline) LongestGap() time.Duration {
	var worst time.Duration
	for _, c := range t.Casts {
		if c.Gap > worst {
			worst = c.Gap
		}
	}
	return worst
}

// formatOffset renders a fight-relative offset as m:ss. Offsets before the pull
// are negative, and read as -m:ss.
func formatOffset(d time.Duration) string {
	sign := ""
	if d < 0 {
		sign, d = "-", -d
	}
	d = d.Round(time.Second)
	return fmt.Sprintf("%s%d:%02d", sign, int(d.Minutes()), int(d.Seconds())%60)
}

// event is one row of the Warcraft Logs events stream.
type event struct {
	Timestamp     float64 `json:"timestamp"`
	Type          string  `json:"type"`
	SourceID      int     `json:"sourceID"`
	TargetID      int     `json:"targetID"`
	AbilityGameID int     `json:"abilityGameID"`
}

type eventPage struct {
	Data              []event  `json:"data"`
	NextPageTimestamp *float64 `json:"nextPageTimestamp"`
}

type ability struct {
	GameID int    `json:"gameID"`
	Name   string `json:"name"`
}

const timelineQuery = `query ($code: String!, $id: Int!, $source: Int!, $start: Float!, $end: Float!) {
  reportData {
    report(code: $code) {
      casts: events(
        dataType: Casts, fightIDs: [$id], sourceID: $source,
        startTime: $start, endTime: $end, limit: 10000
      ) { data nextPageTimestamp }
      lust: events(
        dataType: Buffs, fightIDs: [$id],
        startTime: $start, endTime: $end, limit: 10000,
        filterExpression: "%s"
      ) { data nextPageTimestamp }
      procs: events(
        dataType: Buffs, fightIDs: [$id], targetID: $source,
        startTime: $start, endTime: $end, limit: 10000,
        filterExpression: "%s"
      ) { data nextPageTimestamp }
      cooldowns: events(
        dataType: Buffs, fightIDs: [$id], targetID: $source,
        startTime: $start, endTime: $end, limit: 10000,
        filterExpression: "%s"
      ) { data nextPageTimestamp }
      raidCDs: events(
        dataType: Buffs, fightIDs: [$id],
        startTime: $start, endTime: $end, limit: 10000,
        filterExpression: "%s"
      ) { data nextPageTimestamp }
      damage: graph(
        dataType: DamageDone, fightIDs: [$id], sourceID: $source,
        startTime: $start, endTime: $end
      )
      taken: graph(
        dataType: DamageTaken, fightIDs: [$id], sourceID: $source,
        startTime: $start, endTime: $end
      )
      bossCasts: events(
        dataType: Casts, fightIDs: [$id], hostilityType: Enemies,
        startTime: $start, endTime: $end, limit: 10000
      ) { data nextPageTimestamp }
      masterData {
        abilities { gameID name }
        actors(type: "Player") { id name subType }
        npcs: actors(type: "NPC") { id name subType }
      }
      fights(fightIDs: [$id]) { encounterID phaseTransitions { id startTime } }
      phases { encounterID phases { id name isIntermission } }
    }
  }
}`

// abilityFilter builds an events filter expression for a set of ability IDs.
func abilityFilter(ids []int) string {
	text := make([]string, len(ids))
	for i, id := range ids {
		text[i] = fmt.Sprint(id)
	}
	return "ability.id in (" + strings.Join(text, ",") + ")"
}

// procAuraIDs is the sorted key set of procAuras, so the query is stable.
func procAuraIDs() []int {
	ids := make([]int, 0, len(procAuras))
	for id := range procAuras {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

// cooldownIDs is the sorted key set of personalCooldowns, so the query is stable.
func cooldownIDs() []int {
	ids := make([]int, 0, len(personalCooldowns))
	for id := range personalCooldowns {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

// auraWindow is a period during which one aura was up.
type auraWindow struct {
	name       string
	start, end time.Duration
}

// auraWindows turns buff events into per-aura intervals, relative to the
// fight. Stack events are ignored: only whether the aura was up matters.
func auraWindows(events []event, fight Fight) []auraWindow {
	sort.SliceStable(events, func(i, j int) bool { return events[i].Timestamp < events[j].Timestamp })

	relative := func(t float64) time.Duration {
		return time.Duration(t-fight.StartTime) * time.Millisecond
	}
	open := map[int]time.Duration{}
	var windows []auraWindow
	for _, e := range events {
		name, tracked := procAuras[e.AbilityGameID]
		if !tracked {
			continue
		}
		switch e.Type {
		case "applybuff", "refreshbuff":
			if _, up := open[e.AbilityGameID]; !up {
				open[e.AbilityGameID] = relative(e.Timestamp)
			}
		case "removebuff":
			if start, up := open[e.AbilityGameID]; up {
				delete(open, e.AbilityGameID)
				windows = append(windows, auraWindow{name, start, relative(e.Timestamp)})
			}
		}
	}
	for id, start := range open {
		windows = append(windows, auraWindow{procAuras[id], start, fight.Duration()})
	}
	return windows
}

// Several auras can be up at once, and which ones matter depends on the cast.
//
// An instant Pyroblast is spent from Hot Streak, and Hyperthermia also makes it
// instant while ramping its damage; when both are up Hot Streak is still
// consumed, so both are worth naming. A hard cast is only justified by
// Pyroclasm, which is checked at the start of the cast but consumed when it
// lands. Pyroclasm survives a Pyroblast cast under Hyperthermia.
var (
	instantProcOrder  = []string{"Hyperthermia", "Hot Streak!"}
	hardCastProcOrder = []string{"Pyroclasm"}
)

// classifyProcs records which aura explains each Pyroblast, and flags the hard
// casts that had none. Only Pyroblast is judged: for any other spell an aura
// being up says nothing about why it was cast.
func classifyProcs(casts []Cast, windows []auraWindow) {
	for i := range casts {
		if casts[i].AbilityID != pyroblastID || casts[i].Precast || casts[i].Cancelled {
			continue
		}
		activeAt := func(at time.Duration) map[string]bool {
			active := map[string]bool{}
			for _, w := range windows {
				if at >= w.start-procStartSlack && at <= w.end+procSlack {
					active[w.name] = true
				}
			}
			return active
		}

		order := hardCastProcOrder
		if casts[i].CastTime <= time.Millisecond {
			order = instantProcOrder
		}

		// The damage bonus lands with the spell, so a hard cast is judged on
		// what is still up when it completes. An aura present at the start but
		// gone by then expired mid-cast and did nothing.
		atStart := activeAt(casts[i].Offset)
		atEnd := atStart
		if casts[i].End > casts[i].Offset {
			atEnd = activeAt(casts[i].End)
		}

		var found, expired []string
		for _, name := range order {
			switch {
			case atEnd[name]:
				found = append(found, name)
			case atStart[name]:
				expired = append(expired, name)
			}
		}
		casts[i].Proc = strings.Join(found, " + ")
		casts[i].ProcExpired = strings.Join(expired, " + ")
		if casts[i].Proc == "" && casts[i].ProcExpired == "" && casts[i].CastTime > time.Millisecond {
			casts[i].ProcMissing = true
		}
	}
}

// Timeline fetches the cast timeline for one player in one fight, along with
// the raid lust windows that ran during it.
func (c *Client) Timeline(ctx context.Context, code string, fight Fight, sourceID int) (*Timeline, error) {
	var data struct {
		ReportData struct {
			Report *struct {
				Casts      eventPage        `json:"casts"`
				Lust       eventPage        `json:"lust"`
				Procs      eventPage        `json:"procs"`
				Cooldowns  eventPage        `json:"cooldowns"`
				RaidCDs    eventPage        `json:"raidCDs"`
				Damage     dpsGraphResponse `json:"damage"`
				Taken      dpsGraphResponse `json:"taken"`
				BossCasts  eventPage        `json:"bossCasts"`
				MasterData struct {
					Abilities []ability `json:"abilities"`
					Actors    []Actor   `json:"actors"`
					NPCs      []Actor   `json:"npcs"`
				} `json:"masterData"`
				Fights []struct {
					EncounterID      int `json:"encounterID"`
					PhaseTransitions []struct {
						ID        int     `json:"id"`
						StartTime float64 `json:"startTime"`
					} `json:"phaseTransitions"`
				} `json:"fights"`
				Phases []struct {
					EncounterID int `json:"encounterID"`
					Phases      []struct {
						ID             int    `json:"id"`
						Name           string `json:"name"`
						IsIntermission bool   `json:"isIntermission"`
					} `json:"phases"`
				} `json:"phases"`
			} `json:"report"`
		} `json:"reportData"`
	}

	query := fmt.Sprintf(timelineQuery,
		abilityFilter(lustAbilityIDs), abilityFilter(procAuraIDs()),
		abilityFilter(cooldownIDs()), abilityFilter(raidCooldownIDs()))
	vars := map[string]any{
		"code": code, "id": fight.ID, "source": sourceID,
		"start": fight.StartTime, "end": fight.EndTime,
	}
	if err := c.Query(ctx, query, vars, &data); err != nil {
		return nil, err
	}
	report := data.ReportData.Report
	if report == nil {
		return nil, fmt.Errorf("warcraftlogs: report %q not found", code)
	}

	casts := report.Casts.Data
	// The events API pages; a long fight can exceed one page of casts.
	for next := report.Casts.NextPageTimestamp; next != nil; {
		var page struct {
			ReportData struct {
				Report struct {
					Casts eventPage `json:"casts"`
				} `json:"report"`
			} `json:"reportData"`
		}
		vars["start"] = *next
		const pageQuery = `query ($code: String!, $id: Int!, $source: Int!, $start: Float!, $end: Float!) {
  reportData { report(code: $code) {
    casts: events(dataType: Casts, fightIDs: [$id], sourceID: $source,
                  startTime: $start, endTime: $end, limit: 10000) { data nextPageTimestamp }
  } }
}`
		if err := c.Query(ctx, pageQuery, vars, &page); err != nil {
			return nil, err
		}
		casts = append(casts, page.ReportData.Report.Casts.Data...)
		next = page.ReportData.Report.Casts.NextPageTimestamp
	}

	names := make(map[int]string, len(report.MasterData.Abilities))
	for _, a := range report.MasterData.Abilities {
		names[a.GameID] = a.Name
	}
	actors := make(map[int]string, len(report.MasterData.Actors))
	for _, a := range report.MasterData.Actors {
		actors[a.ID] = a.Name
	}

	timeline := &Timeline{Duration: fight.Duration()}
	timeline.Lusts = lustWindows(report.Lust.Data, fight, names, actors)
	timeline.Casts = buildCasts(casts, fight, names, timeline.Lusts)
	classifyProcs(timeline.Casts, auraWindows(report.Procs.Data, fight))
	timeline.DPS = buildDPS(report.Damage, fight)
	timeline.Taken = buildDPS(report.Taken, fight)

	npcs := make(map[int]Actor, len(report.MasterData.NPCs))
	for _, npc := range report.MasterData.NPCs {
		npcs[npc.ID] = npc
	}
	timeline.BossCasts = buildBossCasts(report.BossCasts.Data, npcs, names, fight)
	timeline.Cooldowns = cooldownWindows(report.Cooldowns.Data, fight, names)
	timeline.RaidCDs = raidCooldownWindows(report.RaidCDs.Data, fight, actors)

	if len(report.Fights) > 0 {
		encounterID := report.Fights[0].EncounterID
		transitions := make([]phaseTransition, 0, len(report.Fights[0].PhaseTransitions))
		for _, t := range report.Fights[0].PhaseTransitions {
			transitions = append(transitions, phaseTransition{ID: t.ID, StartTime: t.StartTime})
		}
		labels := map[int]phaseLabel{}
		for _, e := range report.Phases {
			if e.EncounterID != encounterID {
				continue
			}
			for _, p := range e.Phases {
				labels[p.ID] = phaseLabel{Name: p.Name, IsIntermission: p.IsIntermission}
			}
		}
		timeline.Phases = buildPhases(transitions, labels, fight)
	}

	timeline.layout()
	return timeline, nil
}

// buildCasts converts raw cast events into fight-relative casts. Warcraft Logs
// emits a begincast and a cast for anything with a cast bar, so the two are
// paired into a single Cast carrying its cast time; a begincast that never
// completes is kept as a cancelled cast.
func buildCasts(events []event, fight Fight, names map[int]string, lusts []RaidWindow) []Cast {
	sort.SliceStable(events, func(i, j int) bool { return events[i].Timestamp < events[j].Timestamp })

	name := func(abilityID int) string {
		if n := names[abilityID]; n != "" {
			return n
		}
		return fmt.Sprintf("Spell %d", abilityID)
	}
	// Casts are timestamped against the report, not the fight.
	relative := func(timestamp float64) time.Duration {
		return time.Duration(timestamp-fight.StartTime) * time.Millisecond
	}

	// A spell that has a cast bar anywhere in the fight is one the player must
	// cast, so an unpaired cast of it means the begincast was not logged.
	castable := map[int]bool{}
	for _, e := range events {
		if e.Type == "begincast" {
			castable[e.AbilityGameID] = true
		}
	}

	casts := make([]Cast, 0, len(events))
	pending := map[int]int{} // ability -> index of its unfinished cast

	for _, e := range events {
		offset := relative(e.Timestamp)
		switch e.Type {
		case "begincast":
			// Assumed cancelled until a matching cast arrives.
			casts = append(casts, Cast{
				Offset:       offset,
				End:          offset,
				AbilityID:    e.AbilityGameID,
				Name:         name(e.AbilityGameID),
				HadBegincast: true,
				Cancelled:    true,
			})
			pending[e.AbilityGameID] = len(casts) - 1
		case "cast":
			if i, open := pending[e.AbilityGameID]; open {
				delete(pending, e.AbilityGameID)
				casts[i].Cancelled = false
				casts[i].End = offset
				casts[i].CastTime = offset - casts[i].Offset
				continue
			}
			// No begincast to pair with: either an inherently instant spell, or
			// one whose cast bar started before the pull.
			casts = append(casts, Cast{
				Offset:    offset,
				End:       offset,
				AbilityID: e.AbilityGameID,
				Name:      name(e.AbilityGameID),
				Precast:   castable[e.AbilityGameID] && offset <= precastWindow,
			})
		}
	}

	for i := range casts {
		casts[i].Cooldown = personalCooldowns[casts[i].AbilityID]
	}

	// A precast landed at a known moment but began before the log starts. Its
	// bar is reconstructed from how long the same spell took elsewhere in this
	// fight, which puts the start before the pull where it belongs.
	for i := range casts {
		if !casts[i].Precast {
			continue
		}
		if estimate := medianCastTime(casts, casts[i].AbilityID); estimate > 0 {
			casts[i].Offset = casts[i].End - estimate
			casts[i].CastTime = estimate
			casts[i].Estimated = true
		}
	}

	sort.SliceStable(casts, func(i, j int) bool { return casts[i].Offset < casts[j].Offset })

	// A cast beginning strictly inside another cast's bar was woven into it.
	// A woven cast never becomes the enclosing cast itself, so two instants
	// fired inside one Fireball are both attributed to the Fireball.
	var castingUntil time.Duration
	for i := range casts {
		switch {
		case casts[i].Offset > 0 && casts[i].Offset < castingUntil:
			casts[i].DuringCast = true
		case casts[i].CastTime > 0:
			castingUntil = casts[i].End
		}
	}

	// A repeat landing on the tail of its own cast bar needs no second label.
	previous := -1
	for i := range casts {
		if casts[i].DuringCast {
			continue // woven casts sit in their own lane and never collide
		}
		if previous >= 0 &&
			casts[previous].AbilityID == casts[i].AbilityID &&
			casts[i].CastTime <= time.Millisecond &&
			casts[i].Offset-casts[previous].End <= labelCollision {
			casts[i].RepeatsPrevious = true
		}
		previous = i
	}

	// Track the furthest point reached so far rather than the previous cast's
	// end: off-GCD instants (Fire Blast during a Pyroblast) start before the
	// cast they overlap has finished, and must not read as a fresh pause.
	var reached time.Duration
	for i := range casts {
		gap := max(casts[i].Offset-reached, 0)
		casts[i].Gap = gap
		if casts[i].End > reached {
			reached = casts[i].End
		}
		for _, l := range lusts {
			if casts[i].Offset >= l.Start && casts[i].Offset <= l.End {
				casts[i].DuringLust = true
				break
			}
		}
	}
	return casts
}

// medianCastTime is the typical time this spell took to cast in the fight,
// ignoring instants. It returns zero when the spell was never hard cast.
func medianCastTime(casts []Cast, abilityID int) time.Duration {
	var seen []time.Duration
	for _, c := range casts {
		if c.AbilityID == abilityID && c.CastTime > 0 {
			seen = append(seen, c.CastTime)
		}
	}
	if len(seen) == 0 {
		return 0
	}
	slices.Sort(seen)
	return seen[len(seen)/2]
}

// lustWindows turns buff apply/remove events into merged raid-wide windows.
// Buffs that landed on only a handful of players are discarded: the same spell
// IDs are sometimes reused for personal effects.
func lustWindows(events []event, fight Fight, names, actors map[int]string) []RaidWindow {
	sort.Slice(events, func(i, j int) bool { return events[i].Timestamp < events[j].Timestamp })

	type key struct{ ability, target int }
	type interval struct{ start, end float64 }

	opened := map[key]float64{}
	intervals := map[int][]interval{}
	targets := map[int]map[int]bool{}
	sources := map[int]int{}

	for _, e := range events {
		k := key{e.AbilityGameID, e.TargetID}
		switch e.Type {
		case "applybuff", "refreshbuff":
			if _, open := opened[k]; !open {
				opened[k] = e.Timestamp
			}
			if targets[e.AbilityGameID] == nil {
				targets[e.AbilityGameID] = map[int]bool{}
			}
			targets[e.AbilityGameID][e.TargetID] = true
			if _, ok := sources[e.AbilityGameID]; !ok && e.SourceID != 0 {
				sources[e.AbilityGameID] = e.SourceID
			}
		case "removebuff":
			if start, open := opened[k]; open {
				delete(opened, k)
				intervals[e.AbilityGameID] = append(intervals[e.AbilityGameID], interval{start, e.Timestamp})
			}
		}
	}
	// A buff still up when the fight ended never gets a removebuff.
	for k, start := range opened {
		intervals[k.ability] = append(intervals[k.ability], interval{start, fight.EndTime})
	}

	var windows []RaidWindow
	for abilityID, list := range intervals {
		if len(targets[abilityID]) < raidLustMinTargets {
			continue
		}
		sort.Slice(list, func(i, j int) bool { return list[i].start < list[j].start })

		// Everyone receives the buff at once, so overlapping per-player
		// intervals collapse into one window per cast.
		merged := []interval{list[0]}
		for _, iv := range list[1:] {
			last := &merged[len(merged)-1]
			if iv.start <= last.end {
				if iv.end > last.end {
					last.end = iv.end
				}
				continue
			}
			merged = append(merged, iv)
		}

		name := names[abilityID]
		if name == "" {
			name = fmt.Sprintf("Spell %d", abilityID)
		}
		for _, iv := range merged {
			w := RaidWindow{
				AbilityID: abilityID,
				Name:      name,
				Source:    actors[sources[abilityID]],
				Start:     time.Duration(iv.start-fight.StartTime) * time.Millisecond,
				End:       time.Duration(iv.end-fight.StartTime) * time.Millisecond,
				Targets:   len(targets[abilityID]),
			}
			windows = append(windows, w)
		}
	}
	sort.Slice(windows, func(i, j int) bool { return windows[i].Start < windows[j].Start })
	return windows
}

// phaseTransition is the moment a fight entered a phase, in report time.
type phaseTransition struct {
	ID        int
	StartTime float64
}

// phaseLabel is the encounter-level naming for a phase ID.
type phaseLabel struct {
	Name           string
	IsIntermission bool
}

// buildPhases turns a fight's phase transitions into positioned sections. Each
// phase runs until the next one starts, and the last runs to the end of the
// pull. Encounters with no phase data yield no sections.
func buildPhases(transitions []phaseTransition, labels map[int]phaseLabel, fight Fight) []Phase {
	if len(transitions) == 0 {
		return nil
	}
	sort.Slice(transitions, func(i, j int) bool { return transitions[i].StartTime < transitions[j].StartTime })

	duration := fight.Duration()
	phases := make([]Phase, 0, len(transitions))
	for i, t := range transitions {
		start := time.Duration(t.StartTime-fight.StartTime) * time.Millisecond
		end := fight.Duration()
		if i+1 < len(transitions) {
			end = time.Duration(transitions[i+1].StartTime-fight.StartTime) * time.Millisecond
		}
		// A transition can be logged fractionally before the pull, and the
		// last phase must not overhang the end of the fight.
		start = clamp(start, 0, duration)
		end = clamp(end, start, duration)

		label := labels[t.ID]
		name := label.Name
		if name == "" {
			name = fmt.Sprintf("Phase %d", t.ID)
		}
		phase := Phase{
			ID:             t.ID,
			Name:           name,
			IsIntermission: label.IsIntermission,
			Start:          start,
			End:            end,
		}
		phases = append(phases, phase)
	}
	return phases
}

func clamp(v, low, high time.Duration) time.Duration {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}

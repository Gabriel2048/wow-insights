package warcraftlogs

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"wowinsight/internal/knowledge"
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

// What is spec-shaped — which auras explain a cast, which abilities are the
// player's own cooldowns, which spells are judged — is not declared here. It
// arrives as a knowledge.Knowledge with the request, and the zero value is a
// spec nobody has authored: those lanes stay empty and the query asks for
// nothing extra. The tables below are class-agnostic and stay here.

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

// raidCooldownWindows turns raid cooldown buffs into windows. Unlike lust, no
// minimum number of targets applies: the curated list is the filter, and a
// channelled cooldown only ever buffs its caster.
func raidCooldownWindows(events []event, fight Fight, actors map[int]string) []RaidWindow {
	events = sortedByTime(events)

	type key struct{ ability, target int }
	type open struct {
		start  float64
		source int
	}
	opened := map[key]open{}
	seen := map[key]bool{}
	intervals := map[int][]buffInterval{}

	for _, e := range events {
		if _, tracked := raidCooldownAuras[e.AbilityGameID]; !tracked {
			continue
		}
		k := key{e.AbilityGameID, e.TargetID}
		switch e.Type {
		case "applybuff", "refreshbuff":
			if _, up := opened[k]; !up {
				opened[k] = open{e.Timestamp, e.SourceID}
			}
		case "removebuff":
			o, up := opened[k]
			switch {
			case up:
			case !seen[k]:
				// The buff went up before the pull, so its applybuff is
				// outside the query. The API does not synthesise one at the
				// boundary (checked against a real report), so the window is
				// opened here at the fight's start instead of being dropped.
				// Only a removebuff that is the first thing seen for this
				// buff and player means that; a later one with nothing open
				// is a duplicate.
				o = open{fight.StartTime, e.SourceID}
			default:
				seen[k] = true
				continue
			}
			delete(opened, k)
			intervals[e.AbilityGameID] = append(intervals[e.AbilityGameID],
				buffInterval{start: o.start, end: e.Timestamp, closed: true, source: o.source, targets: map[int]bool{e.TargetID: true}})
		}
		seen[k] = true
	}
	for k, o := range opened {
		intervals[k.ability] = append(intervals[k.ability],
			buffInterval{start: o.start, end: fight.EndTime, source: o.source, targets: map[int]bool{k.target: true}})
	}

	var windows []RaidWindow
	for _, abilityID := range slices.Sorted(maps.Keys(intervals)) {
		merged := mergeBuffIntervals(intervals[abilityID], defaultRaidCooldownLength)
		for _, iv := range merged {
			if iv.end-iv.start < raidCooldownMinWindow {
				continue // a flicker, not a use
			}
			windows = append(windows, RaidWindow{
				AbilityID: abilityID,
				Name:      raidCooldownAuras[abilityID],
				Source:    actors[iv.source],
				Start:     time.Duration(iv.start-fight.StartTime) * time.Millisecond,
				End:       time.Duration(iv.end-fight.StartTime) * time.Millisecond,
				Targets:   len(iv.targets),
			})
		}
	}
	sortRaidWindows(windows)
	return windows
}

// buffInterval is one player's run of one buff, before merging.
type buffInterval struct {
	start, end float64
	// closed records whether a removebuff was actually seen. Players who die,
	// or leave the log, can leave a buff open; extending those to the end of
	// the fight would stretch an 8s cooldown across minutes.
	closed  bool
	source  int          // who cast it
	targets map[int]bool // who received it
}

// mergeBuffIntervals turns one ability's per-player intervals into uses. An
// interval left open by a player who died or dropped out of the log is
// capped at how long the buff usually lasts — the median of the intervals
// that did close, or fallback when none did. Overlapping intervals from the
// same caster are one use: players wander in and out of a ground effect like
// Anti-Magic Zone, so a re-entry belongs to the placement it happened
// inside. Overlapping intervals from different casters are two uses — the
// raid used two cooldowns, and folding them would show one.
func mergeBuffIntervals(list []buffInterval, fallback float64) []buffInterval {
	if len(list) == 0 {
		return nil
	}
	var lengths []float64
	for _, iv := range list {
		if iv.closed {
			lengths = append(lengths, iv.end-iv.start)
		}
	}
	typical := fallback
	if len(lengths) > 0 {
		slices.Sort(lengths)
		typical = lengths[len(lengths)/2]
	}
	for i := range list {
		if !list[i].closed && list[i].start+typical < list[i].end {
			list[i].end = list[i].start + typical
		}
	}

	slices.SortStableFunc(list, func(a, b buffInterval) int {
		return cmp.Or(cmp.Compare(a.start, b.start), cmp.Compare(a.source, b.source))
	})
	var merged []buffInterval
	for _, iv := range list {
		// Find the latest open use by this caster to fold into.
		folded := false
		for j := len(merged) - 1; j >= 0; j-- {
			last := &merged[j]
			if last.source != iv.source {
				continue
			}
			if iv.start <= last.end {
				last.end = max(last.end, iv.end)
				for t := range iv.targets {
					last.targets[t] = true
				}
				folded = true
			}
			break
		}
		if !folded {
			copied := iv
			copied.targets = map[int]bool{}
			for t := range iv.targets {
				copied.targets[t] = true
			}
			merged = append(merged, copied)
		}
	}
	slices.SortStableFunc(merged, func(a, b buffInterval) int {
		return cmp.Or(cmp.Compare(a.start, b.start), cmp.Compare(a.source, b.source))
	})
	return merged
}

// sortRaidWindows orders windows by start, then ability, so two that start
// together always come out the same way round.
func sortRaidWindows(windows []RaidWindow) {
	slices.SortStableFunc(windows, func(a, b RaidWindow) int {
		return cmp.Or(cmp.Compare(a.Start, b.Start), cmp.Compare(a.AbilityID, b.AbilityID))
	})
}

// raidLustMinTargets is how many players a haste buff must land on before it
// counts as a raid lust. Some abilities in this list also exist as personal
// effects that reuse the same spell ID; requiring a raid-wide application
// keeps those out of the timeline.
const raidLustMinTargets = 5

// defaultLustLength caps an unclosed lust when no player's closed, which is
// what a wipe under Bloodlust looks like. Every lust in the game lasts 40s.
const defaultLustLength = 40000 // milliseconds

// labelCollision is how close a repeat of the same spell has to be to the end
// of the previous cast before their labels overlap on the timeline.
const labelCollision = 50 * time.Millisecond

// precastWindow is how soon after the pull a cast with no begincast in the
// log can still be the precast — the bar begun before the fight, landing on
// it. Only the first such cast qualifies. An instant always brings its
// begincast (in the same millisecond), so this never mistakes one; the window
// only has to allow for an off-GCD instant landing a few hundred milliseconds
// before the precast does.
const precastWindow = 1500 * time.Millisecond

// instantTolerance is how far apart a begincast and its cast may be and still
// be one instant. The two events of a proc-instant usually share a millisecond
// but sometimes straddle one, and a median that counted those 1ms "casts"
// would call the spell's typical cast time one millisecond.
const instantTolerance = 50 * time.Millisecond

// maxCastBar is longer than any cast bar in the game. A begincast older than
// this when a cast of the same spell arrives is an abandoned bar, not the
// start of this cast; pairing them would draw a bar across a minute of fight
// and swallow every gap under it.
const maxCastBar = 10 * time.Second

// unknownCastBar stands in for a spell's typical cast time when it was never
// completed in the fight, to bound how long an abandoned bar of it is drawn.
const unknownCastBar = time.Second

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

	// HadBegincast distinguishes a spell the game considers castable but which
	// resolved instantly (a proc) from one that is inherently instant.
	HadBegincast bool

	// Cancelled marks a cast that started and never landed, because it was
	// interrupted, moved out of, or replaced. End is when the bar is judged
	// to have been abandoned; Wasted() is how long it ran.
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

	// ProcMissing marks a hard cast of a spell the spec judges that had no
	// aura justifying it: a cast that should not have been made.
	ProcMissing bool

	// ProcExpired names an aura that was up when the cast began but had run
	// out before it landed. The damage bonus applies on impact, so the cast
	// gained nothing and the proc was wasted.
	ProcExpired string
}

// Wasted is how long an abandoned cast bar ran before it was given up;
// zero for a cast that landed.
func (c Cast) Wasted() time.Duration {
	if !c.Cancelled {
		return 0
	}
	return c.End - c.Offset
}

// resolvedInstantly reports whether a cast with a bar took no measurable time
// on it: a proc.
func (c Cast) resolvedInstantly() bool { return c.CastTime <= instantTolerance }

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
	AbilityID int
	Name      string
	Source    string // who cast it, when known
	Start     time.Duration
	End       time.Duration
	Targets   int
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
}

// Duration is how long the cooldown lasted.
func (c CooldownWindow) Duration() time.Duration { return c.End - c.Start }

// Timestamp renders the moment it went up as m:ss.
func (c CooldownWindow) Timestamp() string { return formatOffset(c.Start) }

// cooldownWindows pairs apply and remove events into the spans a cooldown was
// up for. One still running when the fight ends is closed at the end.
func cooldownWindows(events []event, fight Fight, names map[int]string, know knowledge.Knowledge) []CooldownWindow {
	events = sortedByTime(events)

	relative := func(t float64) time.Duration {
		return time.Duration(t-fight.StartTime) * time.Millisecond
	}
	open := map[int]float64{}
	seen := map[int]bool{}
	intervals := map[int][]buffInterval{}
	for _, e := range events {
		if !know.IsCooldown(e.AbilityGameID) {
			continue
		}
		switch e.Type {
		case "applybuff", "refreshbuff":
			if _, up := open[e.AbilityGameID]; !up {
				open[e.AbilityGameID] = e.Timestamp
			}
		case "removebuff":
			start, up := open[e.AbilityGameID]
			switch {
			case up:
			case !seen[e.AbilityGameID]:
				// Put up before the pull — a Blazing Barrier at the ready is
				// the common case — so the applybuff is outside the query.
				start = fight.StartTime
			default:
				seen[e.AbilityGameID] = true
				continue
			}
			delete(open, e.AbilityGameID)
			intervals[e.AbilityGameID] = append(intervals[e.AbilityGameID], buffInterval{start: start, end: e.Timestamp, closed: true})
		}
		seen[e.AbilityGameID] = true
	}
	for id, start := range open {
		intervals[id] = append(intervals[id], buffInterval{start: start, end: fight.EndTime})
	}

	var windows []CooldownWindow
	for _, id := range slices.Sorted(maps.Keys(intervals)) {
		name := names[id]
		if name == "" {
			name = fmt.Sprintf("Spell %d", id)
		}
		// One player's own buffs never overlap themselves, so the merge only
		// caps the unclosed ones.
		for _, iv := range mergeBuffIntervals(intervals[id], defaultRaidCooldownLength) {
			windows = append(windows, CooldownWindow{
				AbilityID: id, Name: name, Start: relative(iv.start), End: relative(iv.end),
			})
		}
	}
	slices.SortStableFunc(windows, func(a, b CooldownWindow) int {
		return cmp.Or(cmp.Compare(a.Start, b.Start), cmp.Compare(a.AbilityID, b.AbilityID))
	})
	return windows
}

// Phase is a named section of an encounter, positioned within one pull.
type Phase struct {
	ID             int
	Name           string
	IsIntermission bool
	Start          time.Duration
	End            time.Duration
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
	// Subject is whose timeline this is.
	Subject Subject

	// Incomplete names the parts of the document the API reported errors on,
	// when it answered partially. Empty when everything arrived. The lanes
	// built from a missing field are simply empty, and a page must say why.
	Incomplete []string
	// Truncated names the event streams that had more than one page and
	// were cut at it — every stream but casts is deliberately not paged, and
	// casts stops at its caps. A lane built from a cut stream ends early,
	// and a page must say so rather than show the boss falling silent.
	Truncated []string

	Casts     []Cast
	Lusts     []RaidWindow
	Phases    []Phase
	BossCasts []BossCast
	Cooldowns []CooldownWindow
	RaidCDs   []RaidWindow
	DPS       *DPSGraph

	// Taken is the damage the player received, on the same bucket grid as DPS.
	Taken *DPSGraph

	// Duration is the length of the fight itself. Where anything is drawn
	// against it is internal/view's decision, not this package's: every
	// time here is absolute and relative to the pull, and there is no
	// position, percentage or path anywhere in the analysis.
	Duration time.Duration
}

// Subject is what a Timeline is about: whose casts, in which pull, in which
// report. The analysis used to carry none of this — the identity existed
// only as arguments to the method that built it and was discarded on
// return — so a []Timeline could not be labelled, sorted or attributed, and
// there was nothing to key a cache on. Subject is comparable, so it can be
// a map key as it is. That is the point of it being its own struct rather
// than Fight: Fight holds *bool and *int, and a struct containing it as a
// key would compare pointers and silently never hit.
type Subject struct {
	ReportCode string
	FightID    int
	ActorID    int
	// Spec is the specialisation whose tables the analysis used — zero for a
	// spec nobody has authored, even though the player has one. The analysis
	// is spec-shaped, so two timelines of the same actor under different
	// tables are different subjects.
	Spec knowledge.SpecID
}

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

// auraWindow is a period during which one aura was up.
type auraWindow struct {
	name       string
	start, end time.Duration
}

// auraWindows turns buff events into per-aura intervals, relative to the
// fight. Stack events are ignored: only whether the aura was up matters.
func auraWindows(events []event, fight Fight, know knowledge.Knowledge) []auraWindow {
	events = sortedByTime(events)

	relative := func(t float64) time.Duration {
		return time.Duration(t-fight.StartTime) * time.Millisecond
	}
	open := map[int]time.Duration{}
	seen := map[int]bool{}
	var windows []auraWindow
	for _, e := range events {
		name, tracked := know.ProcAura(e.AbilityGameID)
		if !tracked {
			continue
		}
		switch e.Type {
		case "applybuff", "refreshbuff":
			if _, up := open[e.AbilityGameID]; !up {
				open[e.AbilityGameID] = relative(e.Timestamp)
			}
		case "removebuff":
			start, up := open[e.AbilityGameID]
			switch {
			case up:
			case !seen[e.AbilityGameID]:
				start = 0 // up before the pull; the applybuff is outside the query
			default:
				seen[e.AbilityGameID] = true
				continue
			}
			delete(open, e.AbilityGameID)
			windows = append(windows, auraWindow{name, start, relative(e.Timestamp)})
		}
		seen[e.AbilityGameID] = true
	}
	for _, id := range slices.Sorted(maps.Keys(open)) {
		name, _ := know.ProcAura(id)
		windows = append(windows, auraWindow{name, open[id], fight.Duration()})
	}
	return windows
}

// classifyProcs records which aura explains each cast of a spell the spec has
// a rule for, and flags the hard casts that had none. Only those spells are
// judged: for any other, an aura being up says nothing about why it was cast.
// Several auras can be up at once; the rule says which matter for an instant
// cast and which for a hard cast, and in what order they are reported.
func classifyProcs(casts []Cast, windows []auraWindow, know knowledge.Knowledge) {
	for i := range casts {
		rule, judged := know.Rule(casts[i].AbilityID)
		if !judged || casts[i].Cancelled {
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

		order := rule.HardCast
		if casts[i].resolvedInstantly() {
			order = rule.Instant
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
		// A hard cast with nothing behind it should not have been made — unless
		// it is the precast, which is hard cast on purpose so that it lands
		// with the pull. It still gets its verdict above; it is not a mistake.
		if casts[i].Proc == "" && casts[i].ProcExpired == "" && !casts[i].resolvedInstantly() && !casts[i].Precast {
			casts[i].ProcMissing = true
		}
	}
}

// timelineReport is the report payload of the timeline query. It is named
// because buildTimeline takes it as a parameter; the structs nested inside it
// are left anonymous, because nothing takes those.
type timelineReport struct {
	// incomplete names the fields the API reported errors on, when the
	// document arrived partially; truncated names the streams that had more
	// pages than were fetched. Not JSON fields; set by fetchTimeline.
	incomplete []string
	truncated  []string

	Casts     eventPage        `json:"casts"`
	Lust      eventPage        `json:"lust"`
	Procs     eventPage        `json:"procs"`
	Cooldowns eventPage        `json:"cooldowns"`
	RaidCDs   eventPage        `json:"raidCDs"`
	Damage    dpsGraphResponse `json:"damage"`
	Taken     dpsGraphResponse `json:"taken"`
	BossCasts eventPage        `json:"bossCasts"`
	// MasterData is fetched by its own report-scoped query and attached here
	// by fetchTimeline, so buildTimeline sees one document as before.
	MasterData masterData `json:"-"`
	Fights     []struct {
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
}

// masterData is the report's ability, player and NPC tables — the same for
// every fight in it.
type masterData struct {
	Abilities []ability `json:"abilities"`
	Actors    []Actor   `json:"actors"`
	NPCs      []Actor   `json:"npcs"`
}

// bossIDs is the game ids of the encounter's own NPCs, in order, for the
// boss cast filter. The environment is typed as a boss and carries game id
// 0; buildBossCasts drops it by its negative actor id, so it is dropped here
// too, or the filter would let its hundred casts back in.
func (m masterData) bossIDs() []int {
	var ids []int
	for _, n := range m.NPCs {
		if n.SubType == bossSubType && n.ID > 0 && n.GameID > 0 {
			ids = append(ids, n.GameID)
		}
	}
	slices.Sort(ids)
	return ids
}

// masterDataResponse is the envelope the master data query returns.
type masterDataResponse struct {
	ReportData struct {
		Report *struct {
			MasterData masterData `json:"masterData"`
		} `json:"report"`
	} `json:"reportData"`
}

// fetchMasterData runs the report-scoped master data query.
// fetchMasterData fetches the report's abilities, actors and NPCs. The
// master data is the only source of every name on the page and of the boss
// filter, so an error on it is an error; an error on one part of it is a
// gap, named in incomplete so the page can say so.
func (c *Client) fetchMasterData(ctx context.Context, code string) (master masterData, incomplete []string, err error) {
	var data masterDataResponse
	err = c.query(ctx, masterDataOp, map[string]any{"code": code}, &data)
	if data.ReportData.Report == nil {
		return masterData{}, nil, notFound(err, code)
	}
	if apiErr, ok := Partial(err); ok {
		if slices.Contains(apiErr.Fields(), "masterData") {
			return masterData{}, nil, err
		}
		incomplete, err = apiErr.Fields(), nil
	}
	if err != nil {
		return masterData{}, nil, err
	}
	return data.ReportData.Report.MasterData, incomplete, nil
}

// timelineResponse is the envelope the timeline query returns.
type timelineResponse struct {
	ReportData struct {
		Report *timelineReport `json:"report"`
	} `json:"reportData"`
}

// castPageResponse is the envelope one further page of cast events returns.
// Its Report is a pointer so that a null report on page two is an error
// naming the page, not a zero value whose nil cursor ends the loop with the
// rest of the casts silently dropped.
type castPageResponse struct {
	ReportData struct {
		Report *struct {
			Casts eventPage `json:"casts"`
		} `json:"report"`
	} `json:"reportData"`
}

// The bounds on following cast pages. No fight in a real report comes within
// an order of magnitude of one page (the busiest stream on a 20-minute kill
// held 653 events against a 10,000 cut), so these are guards against a
// cursor that never advances or a stream that never ends, not capacity.
const (
	maxCastPages  = 10
	maxCastEvents = 50000
)

// timelineVars is castVars plus the four filters, each present only when its
// set is not empty. Each filter is its own named variable, so the document
// says which lane gets which — a positional Sprintf, which this replaced,
// mis-filled two lanes with each other's data if two arguments were swapped,
// and said nothing.
func timelineVars(code string, fight Fight, sourceID int, bosses []int, know knowledge.Knowledge) map[string]any {
	vars := castVars(code, fight, sourceID, fight.StartTime)
	filterVariable(vars, "lust", lustAbilityIDs)
	// The two spec-shaped streams are gated as well as filtered: with no
	// tables there is nothing to match, so the fields are skipped outright.
	filterVariable(vars, "procs", know.ProcAuraIDs())
	filterVariable(vars, "cooldowns", know.CooldownIDs())
	vars["withProcs"] = len(know.ProcAuraIDs()) > 0
	vars["withCooldowns"] = len(know.CooldownIDs()) > 0
	filterVariable(vars, "raidCDs", slices.Sorted(maps.Keys(raidCooldownAuras)))
	// The boss lane keeps only the encounter's own NPCs, so the query asks
	// for only those: half of an enemy cast stream is adds.
	if expr, ok := sourceFilter(bosses); ok {
		vars["bosses"] = expr
	}
	return vars
}

// castVars builds the variables both cast queries take. Each call gets a fresh
// map rather than sharing and mutating one across pages: the request bodies are
// identical either way, since encoding/json sorts map keys, but a map rewritten
// mid-loop is a trap for whoever adds the next query.
func castVars(code string, fight Fight, sourceID int, start float64) map[string]any {
	return map[string]any{
		"code": code, "id": fight.ID, "source": sourceID,
		"start": start, "end": fight.EndTime,
	}
}

// Timeline fetches the cast timeline for one player in one fight, along with
// the raid lust windows that ran during it. know is the player's spec tables;
// the zero value analyses an unauthored spec, which still yields the casts,
// pauses, phases, boss casts, lust and raid cooldowns, and asks the API for
// no procs or cooldowns at all.
func (c *Client) Timeline(ctx context.Context, code string, fight Fight, sourceID int, know knowledge.Knowledge) (*Timeline, error) {
	report, casts, err := c.fetchTimeline(ctx, code, fight, sourceID, know)
	if err != nil {
		return nil, err
	}
	timeline := buildTimeline(report, casts, fight, know)
	timeline.Subject = Subject{ReportCode: code, FightID: fight.ID, ActorID: sourceID, Spec: know.Spec}
	return timeline, nil
}

// fetchTimeline runs the timeline query and follows the cast pagination. The
// casts come back separately from the report because the report's own Casts
// field holds only the first page, whereas the slice is every page.
func (c *Client) fetchTimeline(ctx context.Context, code string, fight Fight, sourceID int, know knowledge.Knowledge) (*timelineReport, []event, error) {
	master, incomplete, err := c.fetchMasterData(ctx, code)
	if err != nil {
		return nil, nil, err
	}

	var data timelineResponse
	err = c.query(ctx, timelineOp, timelineVars(code, fight, sourceID, master.bossIDs(), know), &data)
	report := data.ReportData.Report
	// The document asks for eleven independent fields, and GraphQL may
	// answer with ten of them and an error on the eleventh. That is a
	// timeline with a gap, not no timeline: the points were spent, the
	// fields that arrived are built, and the ones that did not are named on
	// the result so the page can say so. A response that carried errors is
	// never worth caching — see docs/decisions/2026-09-11-error-taxonomy.md.
	if apiErr, ok := Partial(err); ok && report != nil {
		for _, f := range apiErr.Fields() {
			if !slices.Contains(incomplete, f) {
				incomplete = append(incomplete, f)
			}
		}
		err = nil
	}
	if report == nil {
		return nil, nil, notFound(err, code)
	}
	if err != nil {
		return nil, nil, err
	}
	report.incomplete = incomplete
	report.MasterData = master

	// Every stream but casts is one page by design; a cursor on one of them
	// is a stream that was cut, and the page says so rather than hiding it.
	for _, stream := range []struct {
		name string
		page eventPage
	}{
		{"lust", report.Lust}, {"procs", report.Procs}, {"cooldowns", report.Cooldowns},
		{"raidCDs", report.RaidCDs}, {"bossCasts", report.BossCasts},
	} {
		if stream.page.NextPageTimestamp != nil {
			report.truncated = append(report.truncated, stream.name)
		}
	}

	casts := report.Casts.Data
	// The events API pages; a long fight can exceed one page of casts. The
	// loop is bounded three ways: a cursor that does not advance ends it, and
	// so do the page and event caps, each leaving the stream marked as cut.
	previous := fight.StartTime
	for pages, next := 0, report.Casts.NextPageTimestamp; next != nil; pages++ {
		if *next <= previous || pages >= maxCastPages || len(casts) >= maxCastEvents {
			report.truncated = append(report.truncated, "casts")
			break
		}
		previous = *next
		page, err := c.fetchCastPage(ctx, code, fight, sourceID, *next, pages+2)
		if err != nil {
			return nil, nil, err
		}
		casts = append(casts, page.Data...)
		next = page.NextPageTimestamp
	}
	return report, casts, nil
}

// fetchCastPage fetches one further page of cast events, beginning at the
// cursor the previous page returned. n is the page number, for the error.
func (c *Client) fetchCastPage(ctx context.Context, code string, fight Fight, sourceID int, start float64, n int) (eventPage, error) {
	var page castPageResponse
	if err := c.query(ctx, castPageOp, castVars(code, fight, sourceID, start), &page); err != nil {
		return eventPage{}, err
	}
	if page.ReportData.Report == nil {
		return eventPage{}, fmt.Errorf("%w: page %d of casts carried no report", ErrUpstream, n)
	}
	return page.ReportData.Report.Casts, nil
}

// buildTimeline assembles a decoded response into a Timeline. It cannot fail:
// nothing after decoding can. What comes out carries times, never positions;
// internal/view draws it against whatever axis the page chooses.
func buildTimeline(report *timelineReport, casts []event, fight Fight, know knowledge.Knowledge) *Timeline {
	names := make(map[int]string, len(report.MasterData.Abilities))
	for _, a := range report.MasterData.Abilities {
		names[a.GameID] = a.Name
	}
	actors := make(map[int]string, len(report.MasterData.Actors))
	for _, a := range report.MasterData.Actors {
		actors[a.ID] = a.Name
	}

	timeline := &Timeline{Duration: fight.Duration(), Incomplete: report.incomplete, Truncated: report.truncated}
	timeline.Lusts = lustWindows(report.Lust.Data, fight, names, actors)
	timeline.Casts = buildCasts(casts, fight, names, timeline.Lusts, know)
	classifyProcs(timeline.Casts, auraWindows(report.Procs.Data, fight, know), know)
	timeline.DPS = buildDPS(report.Damage, fight)
	timeline.Taken = buildDPS(report.Taken, fight)

	npcs := make(map[int]Actor, len(report.MasterData.NPCs))
	for _, npc := range report.MasterData.NPCs {
		npcs[npc.ID] = npc
	}
	timeline.BossCasts = buildBossCasts(report.BossCasts.Data, npcs, names, fight)
	timeline.Cooldowns = cooldownWindows(report.Cooldowns.Data, fight, names, know)
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
	return timeline
}

// buildCasts converts raw cast events into fight-relative casts. Warcraft Logs
// emits a begincast and a cast for anything with a cast bar, so the two are
// paired into a single Cast carrying its cast time; a begincast that never
// completes is kept as a cancelled cast.
func buildCasts(events []event, fight Fight, names map[int]string, lusts []RaidWindow, know knowledge.Knowledge) []Cast {
	events = sortedByTime(events)

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
	type pendingCast struct {
		index int     // the row opened by the begincast
		at    float64 // when
	}
	pending := map[int]pendingCast{} // ability -> its unfinished bar

	// Events are read in the order the game produced them, and that order
	// carries meaning even inside one millisecond: a begincast followed by a
	// cast on the same tick is an instant (a proc), while a cast followed by
	// a begincast of the same spell is a hard cast landing and the next one
	// beginning — chain-casting. In a real log every cast-first pair sits one
	// cast time after its begincast; treating the two orders as equivalent
	// would turn every chain-cast into a phantom bar.
	for _, e := range events {
		offset := relative(e.Timestamp)
		switch e.Type {
		case "begincast":
			// Assumed cancelled until a matching cast arrives. A bar still
			// pending from before was abandoned, and stays cancelled.
			casts = append(casts, Cast{
				Offset:       offset,
				End:          offset,
				AbilityID:    e.AbilityGameID,
				Name:         name(e.AbilityGameID),
				HadBegincast: true,
				Cancelled:    true,
			})
			pending[e.AbilityGameID] = pendingCast{len(casts) - 1, e.Timestamp}
		case "cast":
			if p, open := pending[e.AbilityGameID]; open {
				delete(pending, e.AbilityGameID)
				// A bar older than any cast in the game was abandoned, not
				// completed by this; pairing them would draw a bar across a
				// minute of fight and swallow every gap under it.
				if time.Duration(e.Timestamp-p.at)*time.Millisecond <= maxCastBar {
					casts[p.index].Cancelled = false
					casts[p.index].End = offset
					casts[p.index].CastTime = offset - casts[p.index].Offset
					continue
				}
			}
			// No bar to complete: an inherently instant spell, or the precast.
			casts = append(casts, Cast{
				Offset:    offset,
				End:       offset,
				AbilityID: e.AbilityGameID,
				Name:      name(e.AbilityGameID),
			})
		}
	}

	for i := range casts {
		casts[i].Cooldown = know.IsCooldown(casts[i].AbilityID)
	}

	// The precast: the first cast of a castable spell with no bar in the log,
	// if it landed within the window. A bar cannot have begun before the
	// previous cast finished, so nothing after the first can be one.
	for i := range casts {
		if casts[i].HadBegincast || !castable[casts[i].AbilityID] {
			continue
		}
		if casts[i].Offset <= precastWindow {
			casts[i].Precast = true
			// Its bar began before the log starts, and is reconstructed from
			// how long the same spell took elsewhere in this fight — from
			// observed bars only, never from a value this loop wrote.
			if estimate := medianCastTime(casts, casts[i].AbilityID); estimate > 0 {
				casts[i].Offset = casts[i].End - estimate
				casts[i].CastTime = estimate
				casts[i].Estimated = true
			}
		}
		break
	}

	slices.SortStableFunc(casts, func(a, b Cast) int { return cmp.Compare(a.Offset, b.Offset) })

	// An abandoned bar ran until the player did something else or until it
	// would have finished, whichever came first. That is time spent, not
	// idle, and the gap loop below treats it so.
	typical := medianCastTime(casts, 0)
	if typical == 0 {
		typical = unknownCastBar
	}
	for i := range casts {
		if !casts[i].Cancelled {
			continue
		}
		bar := medianCastTime(casts, casts[i].AbilityID)
		if bar == 0 {
			bar = typical
		}
		end := min(casts[i].Offset+bar, fight.Duration())
		if i+1 < len(casts) && casts[i+1].Offset < end {
			end = casts[i+1].Offset
		}
		casts[i].End = end
	}

	// A cast beginning strictly inside another cast's bar was woven into it.
	// A woven cast never becomes the enclosing cast itself, so two instants
	// fired inside one Fireball are both attributed to the Fireball.
	var castingUntil time.Duration
	for i := range casts {
		switch {
		case casts[i].Offset > 0 && casts[i].Offset < castingUntil:
			casts[i].DuringCast = true
		case casts[i].CastTime > instantTolerance:
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
			casts[i].resolvedInstantly() &&
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

// sortedByTime returns a copy of the events in timestamp order, stable so
// that events sharing a millisecond keep the order the API sent. A copy,
// because the caller's slice is not this function's to reorder: the decoded
// response is shared by every builder, and will be shared across requests
// once #2 caches it.
func sortedByTime(events []event) []event {
	events = slices.Clone(events)
	slices.SortStableFunc(events, func(a, b event) int { return cmp.Compare(a.Timestamp, b.Timestamp) })
	return events
}

// medianCastTime is the typical time this spell took to cast in the fight,
// from bars that were actually logged: not instants, not abandoned bars, and
// not a bar this file reconstructed. It returns zero when the spell was never
// hard cast. An abilityID of 0 asks across every spell.
func medianCastTime(casts []Cast, abilityID int) time.Duration {
	var seen []time.Duration
	for _, c := range casts {
		if (abilityID == 0 || c.AbilityID == abilityID) && !c.Cancelled && !c.Estimated && c.CastTime > instantTolerance {
			seen = append(seen, c.CastTime)
		}
	}
	if len(seen) == 0 {
		return 0
	}
	slices.Sort(seen)
	return seen[len(seen)/2]
}

// lustWindows finds the raid-wide haste buffs — Bloodlust, Heroism, Time Warp
// and their kin — and turns each use into one window. Only an ability that
// landed on at least raidLustMinTargets players counts: some of the ids also
// exist as personal effects.
func lustWindows(events []event, fight Fight, names, actors map[int]string) []RaidWindow {
	events = sortedByTime(events)

	type key struct{ ability, target int }
	type open struct {
		start  float64
		source int
	}
	opened := map[key]open{}
	seen := map[key]bool{}
	intervals := map[int][]buffInterval{}
	targets := map[int]map[int]bool{}

	for _, e := range events {
		k := key{e.AbilityGameID, e.TargetID}
		if targets[e.AbilityGameID] == nil {
			targets[e.AbilityGameID] = map[int]bool{}
		}
		targets[e.AbilityGameID][e.TargetID] = true
		switch e.Type {
		case "applybuff", "refreshbuff":
			if _, up := opened[k]; !up {
				opened[k] = open{e.Timestamp, e.SourceID}
			}
		case "removebuff":
			o, up := opened[k]
			switch {
			case up:
			case !seen[k]:
				o = open{fight.StartTime, e.SourceID} // up before the pull; see raidCooldownWindows
			default:
				seen[k] = true
				continue
			}
			delete(opened, k)
			intervals[e.AbilityGameID] = append(intervals[e.AbilityGameID],
				buffInterval{start: o.start, end: e.Timestamp, closed: true, source: o.source, targets: map[int]bool{e.TargetID: true}})
		}
		seen[k] = true
	}
	// A buff still up when the fight ended never gets a removebuff — and
	// neither does one on a player who died under it, which on a wipe is
	// most of the raid. Those are capped like any other unclosed buff.
	for k, o := range opened {
		intervals[k.ability] = append(intervals[k.ability],
			buffInterval{start: o.start, end: fight.EndTime, source: o.source, targets: map[int]bool{k.target: true}})
	}

	var windows []RaidWindow
	for _, abilityID := range slices.Sorted(maps.Keys(intervals)) {
		if len(targets[abilityID]) < raidLustMinTargets {
			continue
		}
		name := names[abilityID]
		if name == "" {
			name = fmt.Sprintf("Spell %d", abilityID)
		}
		// Everyone receives the buff at once, so overlapping per-player
		// intervals collapse into one window per cast.
		for _, iv := range mergeBuffIntervals(intervals[abilityID], defaultLustLength) {
			windows = append(windows, RaidWindow{
				AbilityID: abilityID,
				Name:      name,
				Source:    actors[iv.source],
				Start:     time.Duration(iv.start-fight.StartTime) * time.Millisecond,
				End:       time.Duration(iv.end-fight.StartTime) * time.Millisecond,
				Targets:   len(iv.targets),
			})
		}
	}
	sortRaidWindows(windows)
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
	transitions = slices.Clone(transitions)
	slices.SortStableFunc(transitions, func(a, b phaseTransition) int { return cmp.Compare(a.StartTime, b.StartTime) })

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
		start = min(max(start, 0), duration)
		end = min(max(end, start), duration)

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

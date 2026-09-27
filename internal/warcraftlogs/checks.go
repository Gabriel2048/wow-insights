package warcraftlogs

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"wowinsight/internal/knowledge"
)

// A Check is one question this page knows how to ask of a pull, and the
// number that came back. It is not a finding and must never become one: a
// finding says a player could have done something differently, a Check says
// only what was counted.
//
// **It exists because a quiet page is unreadable without it.** The coaching
// page said "Nothing to flag" on a 19th-percentile parse ending in a death,
// and there was no way to tell from the page whether that meant the rules had
// looked and found nothing or had never run. Both are real states and they
// are opposite answers. Zero findings and four Checks is an answer a reader
// can audit; zero findings alone is a blank screen.
//
// Nothing here carries a verdict. Measured states counts — "155 Hot Streaks,
// 154 spent" — and never "good proc discipline". Whether a pull was played
// well is the reader's conclusion to draw from numbers, and the page sits the
// percentile on the same screen so the two can disagree in front of them.
type Check struct {
	// RuleID is the rule that ran, and is the same id its findings carry.
	RuleID string
	// Question is what was asked, in the player's words.
	Question string
	// Measured is what came back, stated without a verdict.
	Measured string
	// Evidence is the working, the same rows a Finding carries.
	Evidence []Evidence
	// At points into the timeline when there is one moment worth opening.
	// Zero for a check that counts the whole fight.
	At time.Duration
	// Asked is false when the question could not be put to this pull at all —
	// a spec nobody has authored, no cast bars to measure a global cooldown
	// from, a stream the query truncated. Printing that is the point: a
	// question nobody asked must not read as a question that came back clean.
	Asked bool
	// Unasked says why, when Asked is false.
	Unasked string
	// Found is how many findings this check produced. Usually zero.
	Found int
	// Note is what the number cannot tell the reader, said next to it. A
	// comparison that hides its own blind spot is one a reader will trust
	// further than it deserves.
	Note string
}

// Timestamp renders At as m:ss, the way every other time on the page reads.
func (c Check) Timestamp() string { return formatOffset(c.At) }

// AtMS is At in milliseconds, for the link into the timeline.
func (c Check) AtMS() int64 { return c.At.Milliseconds() }

// Analysis is everything this package may say about one pull: the claims, and
// the questions behind them whether or not they produced a claim.
type Analysis struct {
	Findings []Finding
	Checks   []Check
}

// PlayerContext is what the analysis needs about the player rather than about
// the pull. It is separate for the same reason Timeline.Attributed takes a
// death: a Timeline is one pull's, and whose pull it was arrives from whoever
// fetched the roster.
//
// DiedAt is carried alongside ActedUntil rather than derived from it because
// ActedUntil is clamped to the fight's length, which makes a death in the last
// second indistinguishable from no death at all.
type PlayerContext struct {
	ActedUntil time.Duration
	DiedAt     time.Duration

	// Damage and ActiveTime are the player's own, from the damage table, for
	// the comparison with other players. ActiveTime is the table's
	// activeTime: time spent dealing damage, whatever the reason for the rest.
	Damage     float64
	ActiveTime time.Duration
	Duration   time.Duration

	// Cohorts are the other players this pull is compared against, as the
	// caller fetched them. A cohort that could not be fetched still appears,
	// with the reason, so the page says what it did not ask rather than
	// leaving a gap that reads as nothing to say.
	Cohorts []CohortResult
}

// CohortResult is one cohort as the caller got it: the cohort, or why not.
type CohortResult struct {
	Kind    CohortKind
	Cohort  *Cohort
	Unasked string
}

// pauseCheck counts time the player was not occupied, and claims nothing
// about it.
//
// **There is deliberately no pause finding, and that is the measured answer
// rather than a gap.** Every pause in both committed recordings has a benign
// explanation the log cannot see: two are the halves of one Alter Time, two
// sit within two seconds of a stage transition, one is a Slow Fall followed
// by a Shimmer — the player was in the air. On a live pull, three of eight
// pauses fell inside an intermission, including the longest.
//
// No threshold rescues it. Set high enough to miss the Alter Time window it
// fires on one pause in eleven minutes of log, and that one is a phase
// transition. The question a rule would have to answer is "could the player
// have been casting?", and that needs to know whether the boss was there —
// which has no event in this API and waits on the encounter guides.
func pauseCheck(t *Timeline, who PlayerContext) (Check, []Finding) {
	c := Check{RuleID: "pauses", Question: "Time not casting, beyond the global cooldown"}
	if !t.GCD.Modelled {
		c.Unasked = "this pull had too few cast bars to measure a global cooldown from, and without one there is no telling a pause from a normal wait"
		return c, nil
	}
	c.Asked = true

	pauses := t.Attributed(who.DiedAt)
	var total, longest time.Duration
	var at time.Duration
	var dead int
	for _, p := range pauses {
		total += p.Duration
		if p.Reason == PauseDead {
			dead++
			continue
		}
		if p.Duration > longest {
			longest, at = p.Duration, p.Start
		}
	}
	c.At = at
	c.Measured = fmt.Sprintf("%s, %s not casting in a %s pull", plural(len(pauses), "pause"), roundSeconds(total), formatOffset(t.Duration))
	c.Evidence = []Evidence{
		{Label: "pauses", Value: fmt.Sprintf("%d", len(pauses))},
		{Label: "time not casting", Value: roundSeconds(total)},
		{Label: "longest", Value: roundSeconds(longest)},
		// The model's own number, at the precision that makes it checkable:
		// "1s" is not something a reader can compare against their own haste
		// and "1.09s" is, and this number is what the whole check rests on.
		{Label: "global cooldown", Value: preciseSeconds(t.GCD.Median)},
		{Label: "measured from", Value: plural(t.GCD.Samples, "cast bar")},
	}
	if dead > 0 {
		c.Evidence = append(c.Evidence, Evidence{Label: "of those, dead for", Value: fmt.Sprintf("%d", dead)})
	}
	return c, nil
}

// procCheck counts what became of the spec's proc auras.
//
// It publishes generated and spent and never a waste count. See ProcLedger
// for why: the unspent number was wrong by seven on a pull where nothing was
// wrong, because the spell the player spent them on was not in the tables.
func procCheck(t *Timeline, know knowledge.Knowledge) (Check, []Finding) {
	c := Check{RuleID: "procs", Question: "Procs, and what was spent on them"}
	if len(know.ProcAuras) == 0 {
		c.Unasked = "nothing here knows which auras matter for this specialisation yet"
		return c, nil
	}
	if len(t.Procs) == 0 {
		c.Unasked = "none of this specialisation's procs came up in this pull"
		return c, nil
	}
	c.Asked = true

	var parts []string
	for _, l := range t.Procs {
		parts = append(parts, fmt.Sprintf("%d %s (%d spent)", l.Generated, l.Name, l.Spent))
		c.Evidence = append(c.Evidence, Evidence{
			Label: l.Name,
			Value: fmt.Sprintf("%d up, %d spent", l.Generated, l.Spent),
		})
		if l.StillUp > 0 {
			c.Evidence = append(c.Evidence, Evidence{
				Label: l.Name + " still up at the end",
				Value: fmt.Sprintf("%d", l.StillUp),
			})
		}
	}
	c.Measured = strings.Join(parts, "; ")
	return c, nil
}

// hardCastCheck counts hard casts of a judged spell with no aura behind them.
//
// It is a check and not a finding on one instance of evidence: across both
// recordings it fires once, on a Pyroblast whose Hyperthermia window closed
// about a tenth of a second before the cast began. A rule whose only instance
// in eleven minutes of log is a hundred milliseconds from being classified
// the other way is a coin toss with a sentence attached.
func hardCastCheck(t *Timeline, know knowledge.Knowledge) (Check, []Finding) {
	c := Check{RuleID: "proc-no-aura", Question: "Hard casts with nothing behind them"}
	if len(know.CastRules) == 0 {
		c.Unasked = "nothing here knows which of this specialisation's casts need a proc behind them"
		return c, nil
	}
	c.Asked = true

	var at time.Duration
	var names []string
	for _, cast := range t.Casts {
		if cast.ProcMissing {
			if len(names) == 0 {
				at = cast.Offset
			}
			names = append(names, cast.Name)
		}
	}
	c.At = at
	c.Measured = plural(len(names), "hard cast")
	if len(names) > 0 {
		c.Measured += ", the first at " + formatOffset(at)
		c.Evidence = []Evidence{
			{Label: "hard casts with no proc", Value: fmt.Sprintf("%d", len(names))},
			{Label: "first at", Value: formatOffset(at)},
		}
	}
	return c, nil
}

// joinWords renders a list the way a sentence does.
func joinWords(parts []string) string {
	switch len(parts) {
	case 0:
		return "none"
	case 1:
		return parts[0]
	}
	out := ""
	for i, p := range parts {
		switch {
		case i == 0:
			out = p
		case i == len(parts)-1:
			out += " and " + p
		default:
			out += ", " + p
		}
	}
	return out
}

// cohortCheck compares the player's DPS with one cohort's.
//
// **The comparison is on DPS while active**, damage over the time spent
// dealing it, alongside plain DPS. The owner carried orbs on this tier's
// Coiled Altar and put the objection plainly: comparing someone with a player
// who was not assigned the mechanic is unreasonable. Carrying an orb lowers
// DPS and leaves DPS while active alone, so the second number is the fair one.
// It is not a free pass — active time does not know why a player was inactive
// — and the note on the check says so.
//
// It claims nothing. A median and a spread are stated, and the reader decides
// what a gap means; findings built on the comparison are #65's.
func cohortCheck(kind CohortKind, who PlayerContext) (Check, []Finding) {
	c := Check{RuleID: "vs-item-level", Question: "DPS against the best at your item level"}
	if kind == TopPerformers {
		c = Check{RuleID: "vs-top", Question: "DPS against the top performers on this boss"}
	}
	var result *CohortResult
	for i := range who.Cohorts {
		if who.Cohorts[i].Kind == kind {
			result = &who.Cohorts[i]
		}
	}
	switch {
	case result == nil:
		c.Unasked = "nothing asked for other players' pulls of this boss"
		return c, nil
	case result.Cohort == nil:
		c.Unasked = result.Unasked
		return c, nil
	case len(result.Cohort.Peers) == 0:
		c.Unasked = "no other player's pull of this boss could be read"
		return c, nil
	case who.ActiveTime <= 0 || who.Duration <= 0:
		c.Unasked = "your own damage table has no active time to compare with"
		return c, nil
	}
	c.Asked = true
	cohort := result.Cohort

	yours := who.Damage / who.ActiveTime.Seconds()
	var active, overall []float64
	for _, p := range cohort.Peers {
		if a := p.ActiveDPS(); a > 0 {
			active = append(active, a)
		}
		if o := p.PullDPS(); o > 0 {
			overall = append(overall, o)
		}
	}
	if len(active) == 0 {
		c.Asked = false
		c.Unasked = "none of the peers' damage tables had an active time to compare with"
		return c, nil
	}
	slices.Sort(active)
	slices.Sort(overall)

	them := fmt.Sprintf("the best %d", len(cohort.Peers))
	if kind == SameItemLevel && cohort.MinItemLevel > 0 {
		levels := fmt.Sprint(cohort.MinItemLevel)
		if cohort.MaxItemLevel > cohort.MinItemLevel {
			levels = fmt.Sprintf("%d–%d", cohort.MinItemLevel, cohort.MaxItemLevel)
		}
		them += " at item level " + levels
	} else if kind == TopPerformers {
		them = fmt.Sprintf("the top %d", len(cohort.Peers))
	}
	c.Measured = fmt.Sprintf("yours %s while active; %s: median %s", thousands(yours), them, thousands(median(active)))

	c.Evidence = []Evidence{
		{Label: "yours, while active", Value: thousands(yours)},
		{Label: "yours, over the pull", Value: thousands(who.Damage / who.Duration.Seconds())},
		{Label: "theirs, while active", Value: fmt.Sprintf("median %s, %s to %s", thousands(median(active)), thousands(active[0]), thousands(active[len(active)-1]))},
		{Label: "theirs, over the pull", Value: fmt.Sprintf("median %s", thousands(median(overall)))},
		{Label: "compared against", Value: plural(len(cohort.Peers), "player")},
	}
	if cohort.Skipped > 0 {
		c.Evidence = append(c.Evidence, Evidence{Label: "could not be read", Value: fmt.Sprintf("%d", cohort.Skipped)})
	}
	c.Note = "While active counts only the time spent dealing damage, so a mechanic you were assigned does not count against you. It also cannot tell a mechanic from standing still — the pauses on the timeline are what say which."
	return c, nil
}

// median is the middle of a sorted set.
func median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// thousands renders a DPS figure the way damage meters do: 189k.
func thousands(v float64) string {
	if v < 1000 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.0fk", v/1000)
}

package warcraftlogs

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"time"

	"wowinsight/internal/knowledge"
)

// Severity orders findings by how much a player should care. It is not a
// score: three minor findings do not add up to a major one, and the page
// sorts by it rather than summing it.
type Severity int

// The severities, worst first when sorted.
const (
	// Major is a finding worth changing how you play the pull.
	Major Severity = iota
	// Minor is worth knowing and not worth rearranging a night over.
	Minor
)

// String names the severity for a template and a log line.
func (s Severity) String() string {
	if s == Major {
		return "major"
	}
	return "minor"
}

// Evidence is the arithmetic behind a finding, kept separate from the
// sentence so a page can show the working and a later change can reword the
// claim without touching what it rests on. Every number here came out of the
// log; none of it is a judgement.
type Evidence struct {
	Label string
	Value string
}

// Finding is one thing the log says a player could have done differently.
//
// Every one of these answers three questions, in this order, because that is
// the order a player asks them: what happened, why it is bad, and what should
// have happened instead. Title is the first. Detail is the second and third,
// and names a concrete moment and a concrete alternative rather than an
// average — "it was ready at 3:19 and you used it at 3:51" is something you
// can go and look at; "37s of cumulative drift measured against your fastest
// gap" is the arithmetic talking to itself.
//
// It carries a time.Duration and never a percentage, a row or a position:
// where a finding is drawn is internal/view's business, and the page links
// to At by turning it into a timeline offset itself.
//
// RuleID names the rule that produced it, not the instance. It exists so
// that a later change can let a language model reorder, merge, suppress and
// word these findings while having nowhere to put one it invented: every
// sentence on the page has to trace back to a RuleID some deterministic rule
// here emitted.
type Finding struct {
	RuleID   string
	Severity Severity
	// Title is the claim, in a few words.
	Title string
	// Detail is the claim in a sentence, with its numbers in it.
	Detail string
	// At is when in the pull the finding points at, relative to the pull.
	At time.Duration
	// Evidence is the working behind the claim.
	Evidence []Evidence
}

// Timestamp renders At as m:ss, the way every other time on the page reads.
func (f Finding) Timestamp() string { return formatOffset(f.At) }

// AtMS is At in milliseconds, for the link that takes the timeline to this
// moment. It is a number, not a position: the page decides where that is.
func (f Finding) AtMS() int64 { return f.At.Milliseconds() }

// The rules, named so a finding can be traced to the code that made it.
//
// Cooldown *drift* — uses creeping later across a pull — is deliberately not
// among them. It was written, measured against seven real pulls, and removed.
// Drift as a raw number is mostly a player holding a cooldown on purpose: for
// a phase, an add spawn, lust, an amplifier. Suppressing the holds that
// happen to land on a phase boundary took 65% of the measured drift with them
// and left 4-12s a pull, below any threshold worth reporting, so the rule
// fired on none of the seven. Leaving the holds in meant telling a player who
// held Combustion seven seconds into an intermission — which all six top
// parses on that encounter also did — that they had drifted.
//
// The question is "was this hold deliberate?", and neither version of the
// rule can answer it; the data can, by comparison with players who did the
// same fight. It belongs to whatever can make that comparison, not to a
// constant here.
const (
	ruleCooldownLate = "cooldown-late-first-use"
	ruleCooldownTail = "cooldown-unused-tail"
)

// lateFirstUse is how long into a pull a judged cooldown can go unused before
// it is worth saying so. A cooldown held past this at the start is almost
// always a cooldown that was forgotten rather than saved.
const lateFirstUse = 20 * time.Second

// Findings is everything the analysis can say about one player's pull. It is
// deterministic and it is the only thing allowed to produce a finding.
//
// A spec with no judged cooldowns produces none, which is the zero value's
// behaviour and is why an unauthored spec is safe to analyse.
func Findings(t *Timeline, know knowledge.Knowledge, actedUntil time.Duration) []Finding {
	if t == nil {
		return nil
	}
	// Nothing may be asked of a player after the pull stopped being theirs to
	// play: a cooldown that came back while they were dead is not one they
	// declined to press.
	if actedUntil <= 0 || actedUntil > t.Duration {
		actedUntil = t.Duration
	}
	found := cooldownFindings(t, know, actedUntil)
	// Worst first, then earliest, so the order is total and two runs over
	// one pull cannot disagree.
	slices.SortStableFunc(found, func(a, b Finding) int {
		return cmp.Or(cmp.Compare(a.Severity, b.Severity), cmp.Compare(a.At, b.At))
	})
	return found
}

// cooldownFindings judges how well the player spaced the cooldowns their spec
// is held to.
//
// The cooldown it measures against is **the player's own shortest gap between
// uses**, not a number from a table. Talents shorten cooldowns and the combat
// log does not say which the player took, so a table would be wrong for
// anyone talented differently — and inferring the cooldown from the gaps is
// circular in exactly the case this rule exists for, because a player who
// drifts on every single use looks identical to one whose cooldown is simply
// longer.
//
// Using their own shortest gap resolves that in the only direction that is
// safe: it is a lower bound on their real cooldown, so the rule can only ever
// *under*-report drift. It will miss a player who is late every time. It will
// never tell one who is not that they were.
func cooldownFindings(t *Timeline, know knowledge.Knowledge, actedUntil time.Duration) []Finding {
	var found []Finding
	for _, ability := range slices.Sorted(maps.Keys(know.JudgedCooldowns)) {
		rule, _ := know.Judged(ability)
		var used []time.Duration
		for _, c := range t.Casts {
			if c.AbilityID == ability && !c.Cancelled {
				used = append(used, c.Offset)
			}
		}
		if len(used) == 0 {
			continue
		}
		if f, ok := lateOpener(rule, used[0]); ok {
			found = append(found, f)
		}
		if len(used) >= 2 {
			gaps := make([]time.Duration, 0, len(used)-1)
			for i := 1; i < len(used); i++ {
				gaps = append(gaps, used[i]-used[i-1])
			}
			if f, ok := unusedTail(rule, used, observedCooldown(rule, gaps), actedUntil); ok {
				found = append(found, f)
			}
		}
	}
	return found
}

// lateOpener reports a judged cooldown that went unused well into the pull.
func lateOpener(rule knowledge.JudgedCooldown, first time.Duration) (Finding, bool) {
	if first <= lateFirstUse {
		return Finding{}, false
	}
	return Finding{
		RuleID:   ruleCooldownLate,
		Severity: Major,
		Title:    fmt.Sprintf("%s went unused for the first %s", rule.Name, roundSeconds(first)),
		Detail: fmt.Sprintf("Your first %s was at %s. It should go out in the opener, while the raid's damage buffs are still up and the boss is fresh — and starting late pushes every later use back with it, which usually costs a whole one by the end of the pull.",
			rule.Name, formatOffset(first)),
		At: 0,
		Evidence: []Evidence{
			{Label: "first used", Value: formatOffset(first)},
			{Label: "should have been", Value: "in the opener"},
		},
	}, true
}

// unusedTail reports a pull that carried on well past the last use of a
// cooldown. It is kept apart from drift on purpose: drift is uses creeping
// later, this is uses that never came, and a sentence blaming the second on
// the first explains neither. On the pull this was built against they are
// both present and have different causes — one bad gap in the middle, and
// two minutes at the end with the cooldown sitting ready.
func unusedTail(rule knowledge.JudgedCooldown, used []time.Duration, floor, fight time.Duration) (Finding, bool) {
	last := used[len(used)-1]
	more := roomFor(last+floor, floor, fight)
	if more < 1 {
		return Finding{}, false
	}
	readyAt := last + floor
	return Finding{
		RuleID:   ruleCooldownTail,
		Severity: Major,
		Title:    fmt.Sprintf("%s came back at %s and was never used again", rule.Name, formatOffset(readyAt)),
		Detail: fmt.Sprintf("Your last %s was at %s, so it came back around %s with %s of the pull still to run. It was never pressed again. That is %s of your strongest damage window left on the table.",
			rule.Name, formatOffset(last), formatOffset(readyAt), roundSeconds(fight-readyAt), plural(more, "full use")),
		At: readyAt,
		Evidence: []Evidence{
			{Label: "last used", Value: formatOffset(last)},
			{Label: "ready again at", Value: formatOffset(readyAt)},
			{Label: "pull still to run", Value: roundSeconds(fight - readyAt)},
			{Label: "uses left unspent", Value: fmt.Sprintf("%d", more)},
		},
	}, true
}

// usefulTail is how much pull a cooldown needs left in front of it to be
// worth pressing at all. A damage cooldown fired three seconds before the
// boss dies does nothing, and counting it as a use the player "had room for"
// is a true sentence that misleads — which is the one thing a coaching page
// cannot afford.
const usefulTail = 10 * time.Second

// roomFor is how many uses the pull had room for from the first one onwards,
// at the pace the player demonstrated. A use must land strictly before the
// end, with usefulTail to spare: without the strict bound a player who used
// the cooldown perfectly every time is told they missed the one that would
// have come down exactly as the boss died.
func roomFor(first, every, fight time.Duration) int {
	n := 0
	for at := first; at+usefulTail <= fight; at += every {
		n++
	}
	return n
}

// observedCooldown is the cooldown the player demonstrated, bounded by what
// the game could plausibly give them. The shortest gap is the estimate; Base
// stops an ability that was reset, or that banked a second charge, from being
// read as a two-second cooldown and turning one pull into forty missed uses.
func observedCooldown(rule knowledge.JudgedCooldown, gaps []time.Duration) time.Duration {
	base := time.Duration(rule.Base) * time.Second
	floor := slices.Min(gaps)
	if base <= 0 {
		return floor
	}
	// No talent in the game halves a cooldown twice over, so a gap under
	// half the untalented cooldown is a reset, not a rotation.
	if floor < base/2 {
		floor = base / 2
	}
	return min(floor, base)
}

// roundSeconds renders a duration the way a player would say it.
func roundSeconds(d time.Duration) string {
	return fmt.Sprintf("%.0fs", d.Round(time.Second).Seconds())
}

// plural writes a count with its noun, e.g. "1 use", "2 uses".
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

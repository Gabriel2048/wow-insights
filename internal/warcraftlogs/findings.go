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
const (
	ruleCooldownDrift = "cooldown-drift"
	ruleCooldownLate  = "cooldown-late-first-use"
	ruleCooldownTail  = "cooldown-unused-tail"
)

// driftFloor is how much cumulative drift is worth mentioning at all. Below
// this the finding is measurement noise dressed up as advice: a global
// cooldown of slack on each of six uses is not a mistake, it is playing.
const driftFloor = 15 * time.Second

// phaseHold is how soon after a phase begins a cooldown can land and still
// read as having been saved for it. A player who holds a cooldown through the
// end of one stage and spends it in the opening seconds of the next is doing
// the single most valuable thing they can with it, and calling that drift is
// the page accusing someone of playing well.
//
// PROVISIONAL, and it is worth being plain about why. Nothing here is
// per-encounter — phases arrive with every fight and a new tier costs this
// code nothing — but the twenty seconds is a judgement, and the rule only
// sees holds that happen to coincide with a phase boundary. It does not see
// a cooldown held for an add spawn, for lust, for a damage amplifier that
// goes up mid-phase, or for a burn window somebody called on voice. Writing
// a rule for each of those is the unmaintainable path.
//
// The general question — "was this hold deliberate?" — is one the data
// answers without a rule. Checked against the six top Fire Mage parses on
// the encounter this was built against, all six put a Combustion inside
// twenty seconds of the same intermission, several of them after a longer
// wait than usual. A comparison against peers would have concluded the hold
// was correct with nothing hard-coded at all, and that is what should
// eventually own this decision; the RuleID on every Finding exists so that
// judgement can suppress one without being able to invent one.
const phaseHold = 20 * time.Second

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
		if f, ok := driftFinding(rule, used, actedUntil, t.Phases); ok {
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

// driftFinding reports a cooldown whose uses spread out over the pull.
func driftFinding(rule knowledge.JudgedCooldown, used []time.Duration, fight time.Duration, phases []Phase) (Finding, bool) {
	if len(used) < 3 {
		// Two uses are one gap, and one gap is not a pattern: it is as
		// likely to be the fight's shape as the player's.
		return Finding{}, false
	}
	gaps := make([]time.Duration, 0, len(used)-1)
	for i := 1; i < len(used); i++ {
		gaps = append(gaps, used[i]-used[i-1])
	}
	floor := observedCooldown(rule, gaps)
	if floor <= 0 {
		return Finding{}, false
	}

	// A wait the player spent on purpose is not drift. Holding a cooldown
	// through the end of a stage to open the next one with it — an
	// intermission with a damage amplifier most of all — is correct play,
	// and the phases the analysis already carries are enough to see it.
	var drift time.Duration
	var held int
	worst, at := time.Duration(0), used[0]
	for i, g := range gaps {
		excess := g - floor
		if excess <= 0 {
			continue
		}
		if _, saved := heldForPhase(used[i]+floor, used[i+1], phases); saved {
			held++
			continue
		}
		drift += excess
		if excess > worst {
			worst, at = excess, used[i]
		}
	}
	if drift < driftFloor {
		return Finding{}, false
	}

	// Losing a whole cooldown's worth of time to drift is a different size of
	// mistake from losing a few seconds on each use.
	severity := Minor
	if drift >= floor {
		severity = Major
	}
	// The moment it came off cooldown and was not pressed. This is what the
	// finding points at, because it is where a player should look to see what
	// they were doing instead.
	readyAt := at + floor
	usedAt := at + worst + floor
	return Finding{
		RuleID:   ruleCooldownDrift,
		Severity: severity,
		Title:    fmt.Sprintf("%s sat ready for %s at %s", rule.Name, roundSeconds(worst), formatOffset(readyAt)),
		Detail: fmt.Sprintf("After the %s at %s it was ready again around %s, and you used it at %s — %s of it sitting there ready. Across the pull that added up to %s. %s is your biggest damage window, so every second it spends ready and unpressed is damage you do not do.",
			rule.Name, formatOffset(at), formatOffset(readyAt), formatOffset(usedAt),
			roundSeconds(worst), roundSeconds(drift), rule.Name),
		At: readyAt,
		Evidence: []Evidence{
			{Label: "ready at", Value: formatOffset(readyAt)},
			{Label: "used at", Value: formatOffset(usedAt)},
			{Label: "your usual gap", Value: roundSeconds(floor)},
			{Label: "total time sitting ready", Value: roundSeconds(drift)},
		},
	}, true
}

// heldForPhase reports whether a cooldown that came back at ready and was
// used at used was being saved for a phase that began in between. The phase
// has to start during the wait and the cooldown has to land in its opening
// seconds: a stage that began twenty seconds before the cooldown was even
// ready explains nothing, and neither does one the player pressed into a
// minute late.
func heldForPhase(ready, used time.Duration, phases []Phase) (Phase, bool) {
	var best Phase
	found := false
	for _, p := range phases {
		if p.Start <= ready || p.Start > used {
			continue
		}
		if used-p.Start > phaseHold {
			continue
		}
		// An intermission is the strongest reason to hold, so prefer it when
		// two phases somehow qualify.
		if !found || (p.IsIntermission && !best.IsIntermission) {
			best, found = p, true
		}
	}
	return best, found
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

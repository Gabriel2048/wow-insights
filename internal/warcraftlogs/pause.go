package warcraftlogs

import (
	"cmp"
	"slices"
	"time"

	"wowinsight/internal/knowledge"
)

// A pause is time the player could have been doing something and was not.
//
// Getting that to mean anything requires modelling the global cooldown, which
// is the thing `docs/decisions/2026-09-09-timeline-heuristics.md` declined to
// do and `docs/decisions/2026-09-20-model-the-global-cooldown.md` reverses.
// Without it the page reported 480 pauses totalling 278 s on a 431 s fight,
// beside a damage uptime of 97.1% — two numbers on one screen that cannot
// both be true. With it, six.
//
// **The global cooldown runs alongside the cast bar, not after it.** That is
// measured rather than assumed, because the obvious model is the wrong one: on
// the recorded kill a quarter of the waits following a hard cast are under
// 30 ms, which is impossible if a fresh cooldown began when the bar ended.
// A cast therefore occupies the player over [Offset, max(End, Offset+GCD)],
// and everything not covered by the union of those spans is a pause.

const (
	// gcdBase is the untalented global cooldown, which haste divides.
	gcdBase = 1500 * time.Millisecond
	// gcdFloor is where the game stops shortening it however much haste is
	// stacked. It is reached on the recorded kill: inside Time Warp the cast
	// bars imply 674 ms, so for about twenty seconds the model charges more
	// busy time than the estimator computed. That is the game's rule and not
	// an error, but it means pauses are systematically under-reported during
	// heavy haste — which is the worst moment to miss one.
	gcdFloor = 750 * time.Millisecond

	// hasteWindow is how many recent cast bars the estimate is taken over.
	// Haste moves with lust, procs and trinkets, so a fight-wide average is
	// wrong for most of the fight.
	//
	// The size is measurably not load-bearing — every window from three casts
	// to fifteen seconds gives the same pause count on both recordings — so
	// this is chosen on behaviour at the edges rather than on output. Five
	// trails a lust landing by about 8.5 s where three trails by 1.6 s, and
	// takes that lag deliberately: three has less lag and more variance, and
	// it is variance, not lag, that manufactures phantom pauses. The lag also
	// costs nothing where it happens, because a player under a haste buff is
	// casting, and a wrong global cooldown inside a run of casting changes no
	// pause.
	hasteWindow = 5
	// minHasteSamples is the fewest cast bars this will estimate from. Below
	// three, one mis-measured bar is the entire model.
	minHasteSamples = 3

	// deadBand is how much longer than the global cooldown a pause must run
	// before it is reported, as a multiple of that cooldown.
	//
	// Busy already charges one global per cast, so a computed pause is time
	// during which the cooldown had expired and nothing was pressed: one
	// whole wasted global is the smallest unit of something missed, and the
	// band adds another on top. Measured, the three pauses that a band of one
	// keeps and two drops exceed it by 0.09 s, 0.61 s and 0.87 s — the
	// estimator's own error printed as an accusation. Two is also far steadier
	// against a bad table: a 15% error in the base cast times moves the
	// reported count by a third at one global and by one at two.
	deadBand = 2
)

// GCDModel is what the analysis worked out about the player's global cooldown,
// and whether it worked anything out at all.
//
// **Modelled is gated on having measured something, not on the spec having a
// table.** The two are not the same and the difference is not hypothetical:
// the Holy Paladin in the recorded raid cast 546 times in 431 seconds with
// zero cast bars, so an authored Paladin table would still yield no sample.
// A Fire Mage in a heavy-movement pull can approach the same thing.
type GCDModel struct {
	// Modelled is false when nothing could be measured. Every consumer must
	// check it: the zero value of the rest of this struct is a global
	// cooldown of zero, which would make every moment of the fight a pause.
	Modelled bool
	// Median is the typical global cooldown over the pull, for a page that
	// wants to show its working.
	Median time.Duration
	// Samples is how many cast bars the estimate came from. It belongs on the
	// page: a reader who knows their own haste can falsify the whole model
	// from this and Median together, which is what replaces the threshold
	// slider this change removes.
	Samples int
}

// PauseReason is what can be said about why a pause happened, which today is
// very little. The zero value is the honest answer and the usual one.
type PauseReason string

// The reasons. Deliberately few: death attribution is #53, boss immunity
// needs the encounter guides in #76, and movement is not derivable at all.
// Nothing here may be phrased as a fault — a pause is not yet a mistake, and
// turning one into a finding is #57's job, behind #61's judgement layer.
const (
	// PauseUnexplained is a pause nothing can be said about. Most of them.
	PauseUnexplained PauseReason = ""
	// PauseAfterCancelledCast follows a cast bar the player abandoned.
	PauseAfterCancelledCast PauseReason = "after an abandoned cast"
	// PauseToFightEnd runs to the end of the pull. Without a death to explain
	// it that means the player stopped before the boss did — which is a real
	// thing to know, and not the same as being dead.
	PauseToFightEnd PauseReason = "to the end of the pull"
	// PauseDead is a pause the player was dead for. It is the largest single
	// unexplained band there is: on the recorded wipe, 36 s of the 52 s
	// reported. A page that does not say it is accusing a corpse of standing
	// still.
	PauseDead PauseReason = "dead"
)

// Pause is one stretch of a pull where the player was not occupied.
type Pause struct {
	Start    time.Duration
	End      time.Duration
	Duration time.Duration
	// GCD is the global cooldown in force where this pause began. It is
	// carried per pause rather than read off the model because the page shows
	// it: a pause is only meaningful against the cooldown it is measured
	// against, and printing both is what lets a reader check the arithmetic.
	GCD    time.Duration
	Reason PauseReason
}

// Timestamp renders the start as m:ss, the way every other time on the page
// reads.
func (p Pause) Timestamp() string { return formatOffset(p.Start) }

// EndLabel renders the end as m:ss, so a row can say where the pause ran to.
func (p Pause) EndLabel() string { return formatOffset(p.End) }

// AtMS is the start in milliseconds, for a link into the timeline.
func (p Pause) AtMS() int64 { return p.Start.Milliseconds() }

// hasteSample is one cast bar, and what it says about haste at that moment.
type hasteSample struct {
	at    time.Duration
	haste float64
}

// buildPauses models the global cooldown and returns what is left over.
func buildPauses(casts []Cast, fight Fight, know knowledge.Knowledge) ([]Pause, GCDModel) {
	samples := hasteSamples(casts, know)
	if len(samples) < minHasteSamples {
		return nil, GCDModel{}
	}

	// Two passes, not a streaming accumulator. A single forward pass has no
	// estimate before the first cast bar, and the opener is where a Fire Mage
	// casts fewest of them: measured, that is eleven phantom pauses in the
	// first forty-five seconds. Everything before the first sample is
	// estimated from the first few, which is the opener's own haste rather
	// than the fight median — the median includes the lust.
	gcdAt := func(at time.Duration) time.Duration {
		return gcdFrom(hasteAt(samples, at))
	}

	busy := busySpans(casts, fight, know, gcdAt, samples)
	var pauses []Pause
	for _, span := range complement(busy, fight.Duration()) {
		gcd := gcdAt(span.start)
		length := span.end - span.start
		if length <= deadBand*gcd {
			continue
		}
		pauses = append(pauses, Pause{
			Start:    span.start,
			End:      span.end,
			Duration: length,
			GCD:      gcd,
			Reason:   reasonFor(span, casts, fight),
		})
	}

	model := GCDModel{Modelled: true, Samples: len(samples)}
	all := make([]time.Duration, 0, len(samples))
	for _, s := range samples {
		all = append(all, gcdFrom(s.haste))
	}
	model.Median = medianDuration(all)
	return pauses, model
}

// hasteSamples reads every cast bar the spec can measure.
//
// The predicate is deliberately the same one medianCastTime uses. If the two
// drifted, the precast estimate and the haste estimate would disagree about
// what a cast bar is, which is the kind of divergence nobody finds for months.
func hasteSamples(casts []Cast, know knowledge.Knowledge) []hasteSample {
	var out []hasteSample
	for _, c := range casts {
		base, ok := know.BaseCast(c.AbilityID)
		switch {
		case !ok, base.Channel:
			// A channel has no bar to measure: the log reports it as an
			// instant, which is exactly why the table has to say so.
			continue
		case !c.HadBegincast, c.Cancelled, c.Estimated, c.CastTime <= instantTolerance:
			// An abandoned bar measures when the player moved. An estimated
			// one was derived from medianCastTime, so feeding it back makes
			// the fight's own median its own evidence.
			continue
		}
		out = append(out, hasteSample{at: c.Offset, haste: float64(base.Base) / float64(c.CastTime)})
	}
	slices.SortStableFunc(out, func(a, b hasteSample) int { return cmp.Compare(a.at, b.at) })
	return out
}

// hasteAt is the median of the last few samples at or before a moment, and of
// the first few before any have been taken.
func hasteAt(samples []hasteSample, at time.Duration) float64 {
	end := 0
	for end < len(samples) && samples[end].at <= at {
		end++
	}
	var window []hasteSample
	if end == 0 {
		window = samples[:min(hasteWindow, len(samples))]
	} else {
		window = samples[max(0, end-hasteWindow):end]
	}
	values := make([]float64, len(window))
	for i, s := range window {
		values[i] = s.haste
	}
	slices.Sort(values)
	return values[len(values)/2]
}

// gcdFrom turns a haste multiplier into the global cooldown it produces.
func gcdFrom(haste float64) time.Duration {
	if haste <= 0 {
		return gcdBase
	}
	return max(gcdFloor, time.Duration(float64(gcdBase)/haste))
}

// span is a half-open interval of the pull.
type span struct{ start, end time.Duration }

// busySpans is every stretch the player was occupied, merged.
func busySpans(casts []Cast, fight Fight, know knowledge.Knowledge, gcdAt func(time.Duration) time.Duration, samples []hasteSample) []span {
	duration := fight.Duration()
	spans := make([]span, 0, len(casts))
	for _, c := range casts {
		start := max(c.Offset, 0) // a precast began before the pull did
		end := max(c.End, c.Offset+gcdAt(c.Offset))
		// A channel takes time the log does not report, so its length comes
		// from the table, hasted like everything else.
		if base, ok := know.BaseCast(c.AbilityID); ok && base.Channel {
			hasted := time.Duration(float64(base.Base) / hasteAt(samples, c.Offset))
			end = max(end, c.Offset+hasted)
		}
		end = min(end, duration)
		if end > start {
			spans = append(spans, span{start, end})
		}
	}
	// Sorted here rather than trusting the caller: max(End, Offset+GCD) is
	// not monotonic in Offset, so a list ordered by start stays ordered by
	// start but not by end, and the merge below wants the former.
	slices.SortFunc(spans, func(a, b span) int { return cmp.Compare(a.start, b.start) })

	merged := spans[:0]
	for _, s := range spans {
		if n := len(merged); n > 0 && s.start <= merged[n-1].end {
			merged[n-1].end = max(merged[n-1].end, s.end)
			continue
		}
		merged = append(merged, s)
	}
	return merged
}

// complement is everything the busy spans do not cover, over the whole pull.
func complement(busy []span, duration time.Duration) []span {
	var out []span
	at := time.Duration(0)
	for _, s := range busy {
		if s.start > at {
			out = append(out, span{at, s.start})
		}
		at = max(at, s.end)
	}
	if at < duration {
		out = append(out, span{at, duration})
	}
	return out
}

// reasonFor says the little that can be said about a pause.
func reasonFor(s span, casts []Cast, fight Fight) PauseReason {
	if s.end >= fight.Duration() {
		return PauseToFightEnd
	}
	for _, c := range casts {
		if c.Cancelled && abs(c.End-s.start) <= time.Millisecond {
			return PauseAfterCancelledCast
		}
	}
	return PauseUnexplained
}

func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// medianDuration is the middle value of a set, or zero when there is none.
func medianDuration(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	slices.Sort(values)
	return values[len(values)/2]
}

// Attributed returns the pauses with the ones a player's death explains
// relabelled, given the moment they died — zero when they did not.
//
// **It returns a new slice and mutates nothing.** The pauses on a Timeline are
// shared: internal/warcraftlogs.Cache hands the same *Timeline to every
// request for the same subject, so relabelling one in place would be a race
// between two browsers and would outlive the request that did it.
//
// It is a method taking the player's context rather than something buildPauses
// does, because a death is a fact about the player and a Timeline is one
// pull's — the same split Findings already has, where the analysis takes
// actedUntil from whoever knows it. The deaths table is fetched by the Fight
// query and not the Timeline one, so this is also the only shape that needs no
// second query and no change to the client seam.
func (t *Timeline) Attributed(diedAt time.Duration) []Pause {
	if diedAt <= 0 || len(t.Pauses) == 0 {
		return t.Pauses
	}
	out := make([]Pause, len(t.Pauses))
	copy(out, t.Pauses)
	for i := range out {
		// The pause ran past the moment the player died, so being dead is at
		// least part of why it happened. A pause that began before the death
		// keeps the whole span — the page prints both times, and a reader can
		// see which part of it the player was alive for.
		if out[i].End > diedAt {
			out[i].Reason = PauseDead
		}
	}
	return out
}

// PauseBefore is the reported pause that ends where this cast begins, if there
// is one. It is what lets the cast list show a pause in line, between the cast
// that stopped and the one that resumed, rather than only on the timeline.
//
// A pause ends exactly where the next busy span starts, and a busy span starts
// at a cast's offset, so the match is exact rather than nearest — a tolerance
// here would attach a pause to whichever cast happened to be closest and
// quietly mislabel which one resumed.
// It returns a pointer rather than a value and a bool because a template is
// the main caller and html/template refuses a two-value method whose second
// value is not an error. Nil is what {{with}} understands.
func (t *Timeline) PauseBefore(c Cast) *Pause {
	for _, p := range t.Pauses {
		if p.End == c.Offset {
			return &p
		}
	}
	return nil
}

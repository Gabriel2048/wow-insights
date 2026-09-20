# The global cooldown is modelled, and a pause is what is left over

- **Status:** accepted
- **Issue:** [#52](https://github.com/Gabriel2048/wow-insights/issues/52)
- **Date:** 2026-09-20
- **Supersedes:** the idle decision in
  [2026-09-09-timeline-heuristics.md](2026-09-09-timeline-heuristics.md)

## Context

`2026-09-09-timeline-heuristics.md` decided that idle is drawn where it happened, above a
threshold the viewer sets, and that the global cooldown is not modelled — with the page
saying so, because at low thresholds most of what a viewer sees is a normal GCD wait
rather than a mistake.

That was the right call on the evidence available. It refuses to invent a number, shows
the raw data, and hands the judgement to the person reading it. The threshold slider and
the disclaimers in `track.html` and `casttable.html` are its compensating control, and
they were honest ones.

**Its premise has since been measured and is false.** The global cooldown is derivable
from the log alone, with no extra API call. On the recorded kill, three independent
estimators agree within about 4%:

| Signal | Base | Observed median | Implied haste |
| --- | --- | --- | --- |
| Scorch cast bar | 1.50 s | 1064 ms (n=37) | ×1.410 |
| Fireball cast bar | 1.75 s | 1276 ms (n=52) | ×1.371 |
| Gap histogram mode | — | 1050 ms | 24 of 419 samples (5.7%) |

The GCD is hasted by exactly the multiplier cast bars are, with a 0.75 s floor, so
`GCD = max(750ms, 1500ms / haste)` — 1064–1094 ms here.

Meanwhile the page contradicts itself in front of the user. It reports **480 pauses
totalling 278 s across a 431 s fight**, which reads as 64.5% idle, beside a damage uptime
tile derived from the damage table's `activeTime` — 419,175 ms of 431,472 — which reads as
97.1%. #72 made those two numbers *mean* different things explicitly, which helped a
reader who stops to think about it. It did not make 480 pauses usable.

## Decision

**The global cooldown is modelled**, from the player's own cast bars. Haste is observed
cast time over base cast time, estimated over recent casts rather than averaged across the
fight, because haste moves with lust, procs and trinkets.

**A pause is the complement of a union of busy intervals**, where busy is every cast bar
plus the estimated GCD that follows it. That is what makes the number mean something: what
is left over is time the player could have been doing something and was not.

**Base cast times are a table on `knowledge.Knowledge`, and the zero value stays safe.** A
spec nobody has authored gets an explicit *unmodelled* state and the page says the GCD is
not modelled for it — rather than a wrong number, and rather than silence. This is the
same contract every other spec-shaped table here already honours.

**A pause within one GCD of the threshold is not reported at all.** The estimate has error
— about 4% across three estimators on one fight, and unknown on a spec nobody has measured
— and a page that turns that error into an accusation has done something worse than saying
nothing. The dead band is the price of modelling at all.

**`Cast.Gap` keeps its current meaning and is not redefined.** It is wall-clock time
between casts, it is used elsewhere, and its two existing tests stay green. The pause model
is built beside it, not on top of it.

## Consequences

The threshold slider goes. A viewer no longer tunes a number to find the interesting
pauses, because the interesting pauses are what the page now reports. That is a real loss
of control and the reason this needed a decision rather than a commit: the slider let a
sceptical reader check the model by moving it, and there is no longer a model to check
that way.

**A pause is not yet a mistake, and the page must not say it is.** This attributes a pause
to the GCD and to nothing else. Death attribution is [#53](https://github.com/Gabriel2048/wow-insights/issues/53).
Boss immunity is not derivable from the API at all — there is no targetability event, and
`ReportFight` has no `downtimeTransitions` field, verified by enumerating all 43 of them on
2026-09-20 — so it waits on the encounter guides in
[#76](https://github.com/Gabriel2048/wow-insights/issues/76). Until those land, a pause
during an intermission looks exactly like a pause during a burn phase.

This is why the change stops at the timeline. Turning a pause into a *finding* on the
coaching page is [#57](https://github.com/Gabriel2048/wow-insights/issues/57), and by then
[#61](https://github.com/Gabriel2048/wow-insights/issues/61)'s judgement layer exists to
set one aside. Making the number honest and making an accusation from it are separate
changes on purpose.

A spec with a wrong base cast time in its table reports phantom pauses. The dead band
absorbs small errors; a badly wrong table is a defect, and the test that catches it is that
the recorded kill must report on the order of ten pauses rather than hundreds.

## What the superseded record still gets right

Its argument stands and is worth keeping: **do not invent a number you cannot measure, and
say so on the page when you have not.** This record does not reverse that principle. It
reverses one application of it, because the measurement arrived.

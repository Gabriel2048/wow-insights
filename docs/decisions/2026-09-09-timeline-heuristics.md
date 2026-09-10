# Timeline heuristics

- **Status:** accepted, partly superseded (see *Known wrong* below)
- **Issue:** [#1](https://github.com/Gabriel2048/wow-insights/issues/1)
- **Date:** 2026-09-09

## Context

Warcraft Logs exposes a raw event stream. A timeline that draws it faithfully is
unreadable: a single pull emits hundreds of cast events, one boss ability fires seventy
times, and every raid buff arrives once per player. Turning that into something a player
can read means making judgement calls about what to merge, what to drop and what to
infer. Those calls are not recoverable from the code alone, so they are recorded here.

## Decisions

**Casts are pairs, not events.** Warcraft Logs emits `begincast` and `cast` for anything
with a cast bar. They are paired into one entry carrying its cast time, so a hard cast is
one row rather than two. In the fight this was built against, 774 events became 480
casts. An unpaired `begincast` is kept as a cancelled cast.

**Idle is drawn where it happened**, above a threshold the viewer sets (default 1500 ms),
rather than being summed into a single statistic. The number still includes the global
cooldown, which is not modelled — the page says so, because at low thresholds most of
what you see is a normal GCD wait rather than a mistake.

**Boss abilities are filtered and burst-merged.** Only the encounter's own NPCs are kept;
adds and the environment cast constantly and say nothing about the fight's structure.
Repeats of one ability within 3 s collapse to a single marker with a count, chained off
the previous cast rather than the start of the group so a long run of filler stays one
marker. Without this, seventy-eight Dread Bolt markers bury the mechanics worth reacting
to.

**A haste buff counts as a raid lust only if it lands on at least five players.** Several
of those spell IDs are reused for personal effects, and requiring a raid-wide application
is what keeps those out of the timeline.

**The raid cooldown list is curated, not detected.** Some raid cooldowns land on the whole
raid (Rallying Cry, Anti-Magic Zone); others are channelled and leave their buff only on
the caster (Tranquility, Divine Hymn). Counting buff targets would throw the second kind
away, so the list is explicit. Single-target externals such as Ironbark are deliberately
absent: they are not raid-wide. A cooldown not on the list is simply not shown.

**Every lane shares one x-domain**, including a pre-pull lead-in so a spell begun before
the pull is drawn as a bar crossing the pull line. Positions are computed in exactly one
place so the casts, phases, cooldown windows and DPS curve cannot drift apart.

## Consequences

Each of these trades completeness for readability, and each can be wrong in a way the
page does not admit to. The curated raid cooldown list is the clearest example: a
cooldown that is missing looks exactly like a cooldown that was never used.

## Known wrong

Later analysis confirmed several of these heuristics are defective in their edge cases.
They are not re-litigated here; they are tracked:

- Cast pairing is unbounded, so a stale `begincast` pairs with a much later cast and
  produces a phantom multi-minute cast bar that zeroes every idle gap after it. The
  5 s precast window also relabels ordinary opener procs as precasts. → **#13**
- Boss markers cannot express an interrupted cast, and the burst merge is keyed on
  ability alone, so two NPCs casting the same spell collapse into one marker. Lust and
  personal-cooldown windows never cap an unclosed buff. → **#14**
- The proc rules and the personal cooldown list are Fire Mage specific and reachable
  through no parameter. → **#16**

## Where the details live

The constants and their reasoning are in the code, next to the thing they govern —
`bossBurstWindow` in `internal/warcraftlogs/boss.go`, `raidLustMinTargets` and
`raidCooldownAuras` in `internal/warcraftlogs/timeline.go`. This record exists for the
decisions that span files; it does not duplicate those comments.

# The analysis carries no geometry; the view draws it against an axis

- **Status:** accepted
- **Issue:** [#17](https://github.com/Gabriel2048/wow-insights/issues/17)
- **Date:** 2026-09-11

## Context

Six analysis types carried drawing coordinates — `Percent`, `Row`, `StartPercent`,
`WidthPercent`, SVG path strings — all relative to one pull: `percent(offset) =
100 * (offset + LeadIn) / Total` with `Total` the pull's own drawn span. A 40% mark on a
three-minute wipe and a 40% mark on a six-minute kill were the same number and different
moments. #3 exists to compare a pull against others, so the primary coordinate on every
analysis type was unusable for the feature it had to serve. `layout()` was unexported and
reachable only from `(*Client).Timeline`, which is also why #10 could not land a positioned
golden render: a test in `main` could only ever obtain a timeline with everything at
`left: 0.000%`.

Alongside: five lanes were five near-identical structs with two copies of the same first-fit
packing (one tested, one not); `Fight` was a wire DTO, a domain entity and a Wowhead helper in
one struct; the two pages each carried their own document shell with 900 lines of CSS and a
300-line script inline; and the analysis result carried no identity — whose casts, which pull —
so nothing could key a cache on it.

## Decision

**`internal/warcraftlogs` produces times relative to the pull and nothing else.**
`internal/view` turns a timeline into positions against `Options{LeadIn, Total}` — an axis
the page chooses — so the same analysis can be laid out at two scales, and two pulls can
share one axis. The analysis does not import the view; "no geometry in the domain" is a
fact the compiler checks, not a convention. `Timeline` carries a `Subject{ReportCode,
FightID, ActorID, Spec}`, comparable, so a cache can key on it as it is (and not on `Fight`,
which holds pointers and would compare them).

**One lane.** Raid cooldowns, personal cooldowns, phases and lust windows are each a
`view.Lane` of `view.Bar`s, packed by one plain, non-generic `packRows`, drawn by one
template block (`bars`). The boss markers keep their own shape: a marker with a link and a
separate bar is a different thing on screen, and pretending otherwise would have cost the
markup its meaning. A generic `packRows[P interface{ *E; laner }]` was deliberately not
written: the unified `Bar` makes it moot.

**`Fight` in three.** `fightWire` and `reportWire` carry the JSON tags and live with the
queries; `Fight` and `Report` are the domain values with none, mapped by a Go struct
conversion that stops compiling the day the wire grows a field the domain does not want;
`view.Fight` carries the Wowhead tooltip parameter. `DifficultyName()` stays on the domain —
"Heroic" is what Warcraft Logs calls difficulty 4, a fact, and the architecture test caught
two dev tools importing the drawing package to print it when the first cut moved it.

**One layout, partials, per-page sets.** `layout.html` is the document shell; a page defines
`title` and `content`; `track`, `casttable` and `playerstats` are partials. Each page is its
own template set cloned from the layout and partials, because one set can hold only one
`content`. The trap this creates is recorded in `AGENTS.md`: a partials file also registers
an almost-empty template under its own name, so it must never be executed by filename.

**Static files, hashed.** The stylesheet and the script are embedded and served at
`/static/<hash>/<name>` with `Cache-Control: immutable`; a stale hash is a 404. No page
carries an inline `<style>` or `<script>`, which is what makes #7's Content-Security-Policy
possible without a nonce. The stylesheet's palette goes through `:root` tokens (the 23
colours that recurred; 31 one-offs stay literal) and declares `color-scheme: dark`.

**The display methods stayed on the domain types.** The issue moved them to the view;
they were left where they are. `CastLabel()`, `Timestamp()`, `Label()` format facts about a
cast — "precast", "1.5s", "0:45" — and the domain tests pinning those strings are the
regression suite. Moving the methods would have meant rewriting every one of those
assertions for no behaviour gain.

## Consequences

- A positioned golden render exists (`TestGoldenRenderOfTheRecordedKill`) and fails on a
  50 ms drift of the axis, naming what moved.
- `TestBuildTimelinePositionsEverything`, which pinned "every timeline from the client is
  laid out", is gone: that property was reversed on purpose. Its replacement,
  `TestLayoutPositionsEveryLane`, pins that `view.Layout` positions everything.
- The JS-contract test reads the script from the static files rather than the rendered
  page, because the script no longer ships in the page.
- `upstream_calls` was already gone; nothing else on the access line changed.
- The full nine-package restructure the issue names as out of scope stays out of scope. The
  builders were pure before this and are pure after it; that is the property that makes the
  rest cheap whenever it is done.

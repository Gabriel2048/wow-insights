# Recorded API responses, replayed under the real client

- **Status:** accepted
- **Issue:** [#10](https://github.com/Gabriel2048/wow-insights/issues/10)
- **Date:** 2026-09-11

## Context

Nothing in this application can be looked at without Warcraft Logs credentials, and
agents do not have them: `.env` is gitignored, correctly. Before this decision a machine
with no `.env` got a 502 for every fight page, so an agent changing the timeline could
not see a timeline. The hand-written fixtures in `fake_test.go` do not close that gap —
they carry positions set by hand, because `layout()` is unexported and runs only inside
`(*Client).Timeline`.

## Decision

A human records one report's fights once, with `go run ./cmd/dev/record`, and commits
the redacted responses under `testdata/`. `go run ./cmd/dev/serve-recorded` then serves
them with no credentials. (Both commands were renamed by #29, which also took the
offline mode out of the shipped binary; the decisions below are unchanged.) Three choices inside that:

**The replay is an `http.RoundTripper`, not a fake client.** `serve-recorded` installs it
through `WithHTTPClient` under a real `*warcraftlogs.Client`. Every offline page therefore
comes out of `Query → buildFightDetail → buildTimeline → layout()` — production code with
only the wire swapped, the cast pagination included. A fake `logsClient` in package
`main` was the obvious alternative and cannot do this: it can hand back a `Timeline`, but
not a laid-out one, so the page it produced would be one no user ever sees.

**Files are raw, compact JSON, keyed by the GraphQL variables.** One file per exchange —
`report.json`, `fight-<id>.json`, `timeline-<id>-<source>-<start>.json` — with the
response body as the entire content. The one risk in this change is "does a committed
file hold a real name?", so a reviewer, `grep`, GitHub's blob view and secret scanning
all have to be able to read it; gzip would defeat every one of them and save nothing,
since git compresses blobs itself. Compact rather than indented so a re-record is not a
60,000-line diff. Keyed by variables and never by query text, because keying by text
would make every edit to a query a re-record, and a re-record needs credentials that the
person editing the query usually does not have. Anything the recording does not hold is
an error; the transport never falls through to the network.

**Redaction is by value, over the whole tree.** The tables and death windows come back as
untyped JSON, so the fields that can carry a name cannot be enumerated. The recorder
instead reads the roster from the typed, complete places — `masterData.actors` and the
report's owner and code — assigns pseudonyms deterministically (sorted names, one per
class: `Testmage`, `Testpriest`, `Testmage2`; servers become `Testrealm`), and replaces
each real value wherever it occurs as a whole word, case-insensitively. Whole-word is
what leaves `Ashen Call` alone when a priest is named Ash. GUIDs are zeroed by key, and
pet names — chosen by their owners, and often a pun on the owner — are replaced by key
too, because a pet called Echo cannot be replaced by value without taking the spell. The
report title is replaced outright. The mapping is never written; before writing, the
recorder scans its own output for every real value and refuses to write if one survives,
saying what kind of value it was and never which.

## Consequences

- The recording is the one artefact an agent cannot produce. `TestFixtureRendersAFightPage`
  skips when `testdata/` is absent and its message is the command to run.
- A query that grows a field the recording lacks still renders, with that field empty.
  The tests on the recording assert on what would go missing, and a re-record is the fix.
- One directory holds one report. The recorder refuses to write a second report into it.
- A player whose name is an ordinary word — Fire, Frost — would be replaced inside
  ability names too. The recorder does not detect this; a reviewer reading the diff
  would. It has not happened; if it does, the answer is a narrower rule,
  decided then.
- The positioned golden render that #10 ruled out is now cheap: a real client over the
  replay yields a laid-out `Timeline` in package `main`. It still waits for #17, by
  choice rather than constraint.

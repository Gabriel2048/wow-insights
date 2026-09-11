# Errors are classified once, and upstream text never reaches a page

- **Status:** accepted
- **Issue:** [#12](https://github.com/Gabriel2048/wow-insights/issues/12)
- **Date:** 2026-09-11

## Context

Every failure below `ErrNoCredentials` was a `fmt.Errorf` string built by interpolating
the upstream response body, and those strings were rendered: onto the index page as the
form's error, and into a 502 body on the fight route. A nonexistent fight was a 502. A
failed timeline fetch rendered an empty area indistinguishable from a player who cast
nothing. A template failing halfway had already sent a 200 and half a page. One failing
field in the eleven-field timeline document discarded the ten that arrived.

## Decision

**The client returns a flat set of sentinels** — `ErrReportNotFound`, `ErrFightNotFound`,
`ErrRateLimited`, `ErrBadCredentials`, `ErrUpstream`, alongside the existing
`ErrNoCredentials` and `ErrNotAReportURL` — wrapped with `%w`. `errors.Is` walks a wrap
chain, not a type tree, so there is no base error to catch and none is offered; a caller
asks about the one thing it can act on.

**What the API said lives on `*APIError`'s fields and never in `Error()`.** In Go the
error string is the payload: an upstream body in it is one `err.Error()` away from a
user's screen, which is exactly how the leak happened. `Status`, `Body` (capped), the
GraphQL `Messages` and `Paths`, and `RetryAfter` are there for the log; `Error()` says
"the API returned HTTP 503" and nothing more. `errors.As` reaches the fields where a
caller needs one — the retry time, for the rate-limit sentence.

**The HTTP layer classifies once.** `classify(err)` in `internal/web` maps an error onto a
status, one fixed sentence, and a log level: a missing fight is a 404 at INFO, a spent
budget a 503 at WARNING with the minutes to wait, an outage a 502 at ERROR, a deadline a
504, a client that went away nothing at all. The full detail goes to the log with the
request id through one function. Handlers never build a message from an error.

**Every page is rendered into a buffer and committed only on success**, with
`Content-Type` and `Content-Length` set. This is what makes a clean 500 reachable at all —
`html/template` streams — and it is what the recovery middleware relies on to be able to
write a status. `writeJSON` marshals before it writes for the same reason.

**A page that partly failed says so.** `fightPageData.Notices` carries the sentences for
what went wrong without stopping the page: the timeline could not be loaded, the player
in the link could not be resolved, part of the document did not arrive. #2 and #3 add
more independently-failing sources to this page; the slot exists before they do.

**A partial GraphQL document is used, not discarded.** GraphQL permits a 200 carrying
both `data` and `errors`. `Query` decodes the data before reporting the errors, and
`Timeline` builds the fields that arrived and names the ones that did not on
`Timeline.Incomplete`. The companion rule, for #2: **a response that carried GraphQL
errors is never cached.** It is a document with a hole in it, and a cache would serve the
hole for an hour.

## Consequences

- An error-rate alert on the 5xx count now means upstream failures. Typos are 404s.
- The exact wording Warcraft Logs uses for a spent budget inside a 200 is not
  documented; `ErrRateLimited` matches "rate limit" and "too many requests" in a GraphQL
  message and any HTTP 429. A 429 is the documented path and is handled by status alone.
- Wrong credentials (`ErrBadCredentials`, from a 400/401/403 on the token endpoint) are a
  503, like missing ones — a deployment fault, not an outage — and the first real request
  after a bad rollout says so in the log at ERROR.
- Two tests that had pinned the old behaviour so that this change would be visible
  (`TestIndexCurrentlyShowsTheUpstreamErrorVerbatim`, `TestQueryReportsErrorsInsideA200`'s
  message-in-`Error()` assertion) were rewritten to pin the new one, and are named in the
  pull request.

# AGENTS.md

The contract for coding agents working in this repository. Read it before your first
edit. `CLAUDE.md` points here; there is no second copy.

**Start here.** `./scripts/check.sh` is the gate; run it before every push. `ARCHITECTURE.md`
is the map; read it before the code. The board and the issue → branch → pull request
lifecycle are in `.claude/skills/work-items/SKILL.md`. Never push to `main`, never merge.

## The gate

Run before every push. CI runs the same list, with the same step names, and is a
required check on `main`.

```
./scripts/check.sh
```

The first run compiles the linter (about a minute); `-race` needs a C compiler; govulncheck
needs the network. **When govulncheck goes red with no change of yours** — a new advisory
against the pinned toolchain — open a work item "Bump toolchain to go1.26.N", change only
the `toolchain` line in `go.mod`, run `go fix ./...` in the same pull request, say so on the
blocked pull request and rebase it once the bump lands. If the Go minor changed, the linter
floor noted in `.github/workflows/ci.yml` may need to move too.

## Running it

Go 1.26 — `go.mod` pins the exact toolchain and fetches it on first use — and a Warcraft
Logs API client from <https://www.warcraftlogs.com/api/clients/>.

```
cp .env.example .env      # then fill in the two values
go run .                  # http://localhost:8080, or PORT=9999 go run .
```

Without both credentials the binary exits at startup naming what is missing — it does
not start and fail every request. Ctrl-C or SIGTERM drains in-flight requests, then
exits 0; a second Ctrl-C ends it at once.

`GET /healthz` is the only health endpoint and touches nothing. There is deliberately no
endpoint that asks Warcraft Logs whether it is up: the app stays up and says so when it
is not, and the 502 rate in the access log is the signal.

**Without credentials**, which is the situation an agent is in:

```
go run ./cmd/dev/serve-recorded   # serves the recorded report; the log lists the URLs
```

`testdata/` holds real API responses, recorded once by a human with
`go run ./cmd/dev/record` and redacted. Every page comes through the real client with only
the network replaced, so the timeline you see is laid out by the same code that lays it
out in production. Anything not in the recording is an error, never a live request. The
recording-backed tests skip when `testdata/` is absent locally and fail in CI, where it is
committed. The redaction renames and never renumbers, so the actor ids in
`testdata/masterdata.json` are the real ones and are what `-players` takes: when you need a
re-record, name the fight id and the actor ids. See
`docs/decisions/2026-09-11-recorded-fixtures.md`.

## Non-negotiables

**Never push to `main`. Never merge a pull request.** The ruleset blocks the first; the
second is the repository owner's call, always. Your work ends at `gh pr create`.

**No real player data, anywhere.** No real character names, guild names, servers or
Warcraft Logs report codes in tests, fixtures, doc comments or user-facing strings. Use
`ExampleReport123` and `Testmage`. `testdata/` is the one place real API responses live, and the recorder that
writes it (`cmd/dev/record`) redacts every name, server, owner and code before writing, and
refuses to write if one survives. Never edit a recording by hand, and never commit one
the recorder did not produce.

**Adding a dependency is a decision, not a step.** The module has no `require` block
today and that is worth keeping — but it is a preference, not a law. A dependency that is
the idiomatic answer in the Go ecosystem, or that a production Go service would normally
carry, is a legitimate thing to add. Propose it in the pull request, saying what it
replaces and why the standard library is not enough, and let the owner decide before it
lands. Never add one silently inside a larger change.

**Templates live under `internal/web/templates/`, static files under `internal/web/static/`.**
Both are embedded at compile time: a static file anywhere else is a 404 at runtime, and a
template anywhere else fails `ParseTemplates` at startup. A page defines `page` and
`content` and is listed in `pages`; `internal/web/templates.go` says how the layout and
partials are cloned per page, and why a partials file is never executed by its filename.
No page carries an inline `<style>`, `<script>` or `on*=` attribute; #7's
Content-Security-Policy depends on that. Positioning is inline `style=` attributes, so that
policy will need `'unsafe-inline'` for styles and nothing else.

## Go idioms this project expects

Not style: `gofmt`, `go vet` and the linter own that, and `go fix` rewrites what it can.
These are the calls tooling cannot make.

- **The consumer declares the interface, not the implementer.** Do not ship an interface
  next to its only implementation. Declare a small, unexported interface in the package
  that needs the substitution; the concrete type satisfies it structurally, with no edit
  to the package defining it. Accept interfaces, return structs.
- **Errors are values.** Wrap with `%w`, compare with `errors.Is`, extract with
  `errors.As`. `errors.Is` walks a wrap chain, not a type tree, so keep the sentinel set
  flat rather than hierarchical. Never put text you would not show a user inside
  `Error()` — that string is the payload; put the detail on a field. The client's
  sentinels and `*APIError` are the pattern, `classify` in `internal/web` is the one
  place an error becomes a status and a sentence, and a handler never builds a message
  from `err.Error()`. See `docs/decisions/2026-09-11-error-taxonomy.md`.
- **Functional options, not a family of constructors.** There is no overloading and no
  optional parameters: `New(id, secret string, opts ...Option)`.
- **Struct equality is field-wise, and a pointer field compares by address.** `Fight`
  holds `*bool` and `*int`, so two values that look equal are not, and it cannot be used
  as a map key. Build such keys explicitly.
- **Tests live in the package they test.** A same-package `_test.go` reaches unexported
  identifiers; that is the intended seam for testing internals, not a workaround.
- **Plain Go otherwise:** the standard library's shapes (`slices`, `maps`, `cmp.Or`) over
  hand-rolled ones, `context.Context` first and honoured, `main` with one exit point since
  `os.Exit` runs no deferred function.
- **The analysis carries no geometry.** `internal/warcraftlogs` produces times relative
  to the pull; `internal/view` turns them into positions against an axis the page chooses.
  A percentage, a row or an SVG path on an analysis type is a defect: the analysis does
  not import the view, so the compiler catches the import, and a reviewer catches the
  field. Every lane on a page is a `view.Lane` of `view.Bar`s, packed by one function and
  drawn by one template block; a new lane is a `Lane`, not a struct, a packer and a loop.
- **A GraphQL document is a named `operation` in `operations.go`, never built by
  concatenation.** Anything that varies is a declared variable. A filter is a nullable
  `String` left out of the variables map when its set is empty — which omits the
  *argument*, not the field; a field that must not be fetched at all is gated with
  `@include(if:)` on a Boolean. A test holds every document to this.

## What tests are for

Two jobs, and most tests here do both.

**Documenting behaviour.** A test is the most reliable description of what this code does,
because it is the only description that fails when it goes stale. Name the test after the
behaviour rather than the function — `TestPrecastOnlyNearThePull`, not `TestBuildCasts3`.
Put the real-world case in a doc comment above it: which log, which moment, what a player
would see. Put the *reason* in the failure message, so the invariant is learnable from the
output alone:

```go
t.Fatalf("got %d windows, want 1 (the personal buff must be excluded)", len(windows))
```

**Preserving behaviour.** Much of this code is heuristics whose reasoning lives only in a
comment. A test pinning today's output is what makes a refactor safe to attempt at all.
Prefer pinning an observable *property* — "phases tile the fight with no gaps", "idle never
exceeds the elapsed span" — over a magic number: the property survives a legitimate change,
the number does not.

**Where a pinned behaviour turns out to be wrong.** Three cases:

1. **Known wrong, not yet fixed.** There is no way to push that here. A defect you find
   and cannot fix in the same change gets an issue, not a skipped test: a skipped test is
   a broken test with a note attached, and Go has no strict xfail — a `t.Skip` does not
   fail when it starts passing, so it would never tell anyone the note was stale.
   `t.Skip` is for a precondition the environment lacks — the committed recording being
   absent on a developer machine — and for nothing else; a fuzz target rejecting an input
   is the other legitimate skip.
2. **A refactor turned a test red and you believe the test is what is wrong.** Start from
   the opposite assumption: a refactor is not meant to change behaviour, so a red test
   means you broke something until you can say why it does not. Red proves something
   changed — it proves nothing about *which side* is wrong.

   If you cannot argue the old expectation was wrong *without pointing at your new
   output*, the code is what needs fixing and there is nothing to discuss. "The code
   returns this now" restates the failure, it does not justify it.

   If you can, **put that argument to the owner before you change anything, and wait.** Say
   which assertion is red, what it was protecting, and why that expectation was wrong on
   its own terms. Changing a passing assertion is the owner's call, not a step in your task.

   Once agreed, make the change and record the reasoning in the pull request, so the
   decision is readable from `git log` in a clone rather than only from a chat.
3. **Never edit a passing assertion until it matches new output.** The hand-written
   fixtures are the regression suite and each names the phenomenon it protects. An
   assertion quietly rewritten destroys the only record of what the code used to do.

**They are still Go tests.** Straight-line unless a table genuinely reads better; this
suite is mostly straight-line by choice. `t.Fatalf` for a precondition that makes the
following assertions meaningless, `t.Errorf` for each assertion. `t.Helper()` in helpers.
Floats compared with a tolerance, never `==`. **Fixtures are functions returning fresh
values, never package-level vars**: the builders return slices they go on to mutate, tests
mutate what they are handed, and the suite runs with `-shuffle=on`, so a shared fixture
would be silently changed by whichever test ran first. And a pin on formatting, whitespace,
attribute order, a stylesheet token or a line of the script is not a behaviour pin; it goes
red on an honest change and protects nothing.

## Logging

`log/slog`, never `log`. Inside a handler use `s.logger(r)`, which carries the request id;
the middleware writes the one access line per request, so a handler logs only what went
wrong, at the level `classify` in `internal/web/errors.go` assigns. The shipped binary
writes JSON spelt for Cloud Logging; the dev binaries write text.

## The one third-party thing that actually executes

`internal/web/templates/fight.html` loads `https://wow.zamimg.com/js/tooltips.js` — unversioned, no
SRI, on every fight page view. "No Go dependencies" is true of the module and false of
the product. It is the one script here running with full origin privileges, and it
constrains any Content-Security-Policy work in #7.

## Credentials

`.env.example` names the variables; `internal/config` reads them, with a real export
winning over the file.

Never echo, log, commit or paste a value. Secret-scanning push protection is enabled, but
it only recognises known credential formats — a backstop, not a permission.

## Where the code lives

`ARCHITECTURE.md` is the map: what the system talks to, how the packages relate and where
the two seams are, and what happens on a fight page request. **Read it before the code.**
If the two disagree, do not decide which is wrong: raise it with the owner, showing both,
and wait — a drift can be the document rotting or the code taking a turn nobody agreed
to, and only the owner knows which.

**A structural change is argued to the owner before it is built** — a new package, a new
edge between packages, a new external system or third-party script, a new entry point (a
binary, a route), or a changed seam. Say what changes in the map and why, then wait.
Adding a function or a type inside a package is not structural; it needs no check-in,
only the diagram edit if there is one.

The package view is verified by `architecture_test.go` against the real import graph, so
it cannot go stale silently. The other views can, and the pull request template asks.

## Decision records

`docs/decisions/` holds the decisions that span files, or that were later reversed. The
directory is small — read it before settling on an approach, and check that what you are
about to build does not quietly contradict one.

Contradicting one **is allowed**. These are records of what was decided and why, not law:
a decision made under different constraints can be the wrong one now, and the roadmap
issues exist precisely because several of them are already known to be wrong. What is not
allowed is contradicting one *by accident*, or silently. If your change goes against an
accepted record:

- **raise it with the owner before you build on it.** Reversing an accepted decision is the owner's
  call, not something to hand over as a finished pull request. Say what the record
  decided, what has changed since, and why it no longer holds — then wait;
- once agreed, add a new record that supersedes it, carrying the reasoning that makes the
  reversal correct now;
- set the old record's **Status** to `superseded by YYYY-MM-DD-slug.md`, and leave its
  argument standing. The wrong turn, and why it looked right, is the useful half of the
  trail — an ADR that gets quietly edited into agreement with the present is worth
  nothing;
- name the record and the reversal in the pull request, so it survives into the commit
  message on `main` rather than living only in a chat log.

Write a new record when a decision spans files, constrains future work, or would
otherwise be re-litigated by the next person who runs into it. Not for choices the code
already states plainly — that is what doc comments are for.

## Constraints to design around

These are the shapes today's code has. New work should not deepen them.

- **Fire Mage is the reference spec, not the only one.** Build against it — it is what
  the owner plays and what the app is tested on — but never in a way that makes another
  spec expensive. Everything spec-shaped lives in `internal/knowledge`, one file per spec
  and one row in its table, and reaches the analysis as a `knowledge.Knowledge` parameter;
  the zero value is a spec nobody has authored and must stay safe to analyse with. Never
  put a spell id, aura name or spec name in `internal/warcraftlogs`, `internal/view` or a
  template; `lustAbilityIDs` and `raidCooldownAuras` stay in the analysis because they
  are class-agnostic. Adding a spec must not touch `timeline.go`.
- **No persistence.** Nothing is stored between requests, so every page view re-queries
  Warcraft Logs against one hourly points budget shared by all users. The cache is a
  roadmap item (#2 and #4 both describe it), keyed on `Subject` plus a version of the
  knowledge tables; do not invent an ad-hoc one.
- **A `Timeline` is one pull's.** `view.Options` can draw two on one axis, which is what
  #3 needs; nothing yet does.

## Work items

The board is GitHub Project #4. The lifecycle and helper live in
`.claude/skills/work-items/SKILL.md`. The short version:

```
.claude/skills/work-items/scripts/wi.sh branch 12   # linked branch named 12-<slug>
./scripts/check.sh                                  # before every push
gh pr create                                        # body carries "Closes #12" — then stop
```

**One work item, one branch, one pull request.** Work that does not fit one reviewable
pull request is split into child issues, never into several pull requests; a parent has
no pull request of its own, and a follow-up on a merged issue is a new issue. `wi.sh
branch` enforces what it can. The rule and its reasons are in
`docs/decisions/2026-09-11-one-work-item-one-pull-request.md`; the skill says how to
work it.

Branches are `{issue-number}-{slug}` and are always created through
`gh issue develop --name`, which is what establishes GitHub's linked-branch relation.
`git checkout -b` produces the same name with no link. See
`docs/decisions/2026-09-10-work-item-and-branch-conventions.md`.

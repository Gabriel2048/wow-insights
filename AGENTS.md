# AGENTS.md

The contract for coding agents working in this repository. Read it before your first
edit. `CLAUDE.md` points here; there is no second copy.

## The gate

Run before every push. CI runs the same list, with the same step names, and is a
required check on `main`.

```
./scripts/check.sh
```

## Running it

Go 1.26 or newer, and a Warcraft Logs API client from
<https://www.warcraftlogs.com/api/clients/>.

```
cp .env.example .env      # then fill in the two values
go run .                  # http://localhost:8080
```

`GET /health/wcl` spends one API point to confirm the credentials work and reports the
hourly points budget.

**Without credentials**, which is the situation an agent is in:

```
go run ./cmd/dev/serve-recorded   # serves the recorded report; the log lists the URLs
```

`testdata/` holds real API responses, recorded once by a human with
`go run ./cmd/dev/record` and redacted. Every page comes through the real client with only
the network replaced, so the timeline you see is laid out by the same code that lays it
out in production. Anything not in the recording is an error, never a live request. If
`testdata/` is missing, `TestFixtureRendersAFightPage` skips and its message says what to
run. See `docs/decisions/2026-09-11-recorded-fixtures.md`.

## Non-negotiables

**Never push to `main`. Never merge a pull request.** The ruleset blocks the first; the
second is the repository owner's call, always. Your work ends at `gh pr create`.

**No real player data, anywhere.** No real character names, guild names, servers or
Warcraft Logs report codes in tests, fixtures, doc comments or user-facing strings. Use
`ExampleReport123` and `Testmage`. The initial commit was rewritten on 2026-09-10 to
remove them. `testdata/` is the one place real API responses live, and the recorder that
writes it (`cmd/dev/record`) redacts every name, server, owner and code before writing, and
refuses to write if one survives. Never edit a recording by hand, and never commit one
the recorder did not produce.

**Adding a dependency is a decision, not a step.** The module has no `require` block
today and that is worth keeping — but it is a preference, not a law. A dependency that is
the idiomatic answer in the Go ecosystem, or that a production Go service would normally
carry, is a legitimate thing to add. Propose it in the pull request, saying what it
replaces and why the standard library is not enough, and let the owner decide before it
lands. Never add one silently inside a larger change.

**Templates must live under `internal/web/templates/`.** `internal/web/server.go` embeds
them with `//go:embed templates/*.html`, resolved at compile time. A template outside that
directory is simply not in the binary — the failure is a blank page at runtime, not a
build error.

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
  `Error()` — that string is the payload; put the detail on a field.
- **`log.Fatal` and `os.Exit` run no deferred function.** Shutdown, flushes and cleanup
  are all skipped. `main` should have exactly one exit point.
- **Functional options, not a family of constructors.** There is no overloading and no
  optional parameters: `New(id, secret string, opts ...Option)`.
- **Struct equality is field-wise, and a pointer field compares by address.** `Fight`
  holds `*bool` and `*int`, so two values that look equal are not, and it cannot be used
  as a map key. Build such keys explicitly.
- **Tests live in the package they test.** A same-package `_test.go` reaches unexported
  identifiers; that is the intended seam for testing internals, not a workaround.
- **Prefer the standard library's own shapes** — `slices` and `maps` over hand-written
  comparators, `cmp.Or` over an if-chain. `go fix` catches some of this, not all of it.
- **`context.Context` is the first parameter and must be honoured.** Anything doing I/O
  takes one and passes it down; never stash it in a struct.

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

**Where a pinned behaviour turns out to be wrong.** Three cases, and only the first is the
default:

1. **Known wrong, not yet fixed — a temporary exception, due for removal.** Write the test
   asserting the *intended* behaviour and skip it, naming the issue. Fixing the defect is
   then a one-line deletion and the test turns green on its own:
   ```go
   t.Skip("known wrong: an instant Pyroblast in the opening 5s must be labelled instant, not precast. Fixed by #13.")
   ```
   `grep -rn 't.Skip'` is the complete inventory, and CI prints the skip list beside the
   coverage line on every run. Go has no strict xfail — a skipped test does **not** fail
   when it starts passing — so the message format is what keeps the inventory honest.

   **This case exists only because #13 and #14 carry defects found before there was time
   to fix them.** A skipped test is a broken test with a note attached, and broken tests
   are not pushed here. When both of those issues have landed there should be no `t.Skip`
   left in the repository, and **whichever of them merges last deletes this numbered item
   and the CI step that prints the skip list.** If you are reading this and
   `grep -rn 't.Skip'` finds nothing, that deletion is overdue.
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
Floats compared with a tolerance, never `==`. And **fixtures are functions returning fresh
values, never package-level vars** — `buildCasts`, `auraWindows` and `buildPhases` sort
their input slice in place, and the suite runs with `-shuffle=on`, so a shared fixture would
be silently mutated by whichever test ran first.

## The one third-party thing that actually executes

`templates/fight.html` loads `https://wow.zamimg.com/js/tooltips.js` — unversioned, no
SRI, on every fight page view. "No Go dependencies" is true of the module and false of
the product. It is the one script here running with full origin privileges, and it
constrains any Content-Security-Policy work in #7.

## Credentials

`.env` is gitignored and read from the process working directory. Keys are
`WARCRAFTLOGS_CLIENT_ID` and `WARCRAFTLOGS_CLIENT_SECRET` (the `ClientId` /
`ClientSecret` spellings Warcraft Logs' own client page uses are accepted too).

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
  spec expensive. `procAuras`, `personalCooldowns`, `pyroblastID` and the proc orderings
  in `timeline.go` are package-level globals reachable through no parameter, which is why
  any other spec renders empty proc and cooldown lanes today. Do not add to that pile:
  anything spec-dependent must arrive as a parameter. #16 turns the existing globals into
  data.
- **No persistence.** Nothing is stored between requests, so every page view re-queries
  Warcraft Logs against a single hourly points budget (measured: 3,600) shared by all
  users. #2 owns the cache; do not invent an ad-hoc one.
- **Presentation lives inside the domain.** Analysis types carry `Percent` and `Row`
  fields and pre-rendered SVG strings, all relative to a single pull, which is why they
  cannot yet be compared across pulls. #17 splits them.

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

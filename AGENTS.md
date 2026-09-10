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

## Non-negotiables

**Never push to `main`. Never merge a pull request.** The ruleset blocks the first; the
second is the repository owner's call, always. Your work ends at `gh pr create`.

**No real player data, anywhere.** No real character names, guild names, servers or
Warcraft Logs report codes in tests, fixtures, doc comments or user-facing strings. Use
`ExampleReport123` and `Testmage`. The initial commit was rewritten on 2026-09-10 to
remove them. This is also the rule the fixture recorder in #10 must satisfy: one real API
response carries twenty raiders' names, servers and item levels.

**Adding a dependency is a decision, not a step.** The module has no `require` block
today and that is worth keeping — but it is a preference, not a law. A dependency that is
the idiomatic answer in the Go ecosystem, or that a production Go service would normally
carry, is a legitimate thing to add. Propose it in the pull request, saying what it
replaces and why the standard library is not enough, and let the owner decide before it
lands. Never add one silently inside a larger change.

**Templates must live under `templates/`.** `main.go` embeds them with
`//go:embed templates/*.html`, resolved at compile time. A template outside that
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

The domain is `internal/warcraftlogs`, roughly one file per concern, with the HTTP layer
and template funcs in `main.go` at the root. `timeline.go` is by a wide margin the
largest file: it holds cast pairing, aura and cooldown windows, phases and the layout
pass together, and it is not self-navigating. Its doc comments carry the reasoning behind
each heuristic — read them before changing a builder.

## Decision records

`docs/decisions/` holds the decisions that span files, or that were later reversed. The
directory is small — read it before settling on an approach, and check that what you are
about to build does not quietly contradict one.

Contradicting one **is allowed**. These are records of what was decided and why, not law:
a decision made under different constraints can be the wrong one now, and the roadmap
issues exist precisely because several of them are already known to be wrong. What is not
allowed is contradicting one *by accident*, or silently. If your change goes against an
accepted record:

- **raise it with the owner before you build on it.** Reversing an accepted decision
  is theirs, not something to hand over as a finished pull request. Say what the record
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

Branches are `{issue-number}-{slug}` and are always created through
`gh issue develop --name`, which is what establishes GitHub's linked-branch relation.
`git checkout -b` produces the same name with no link. See
`docs/decisions/2026-09-10-work-item-and-branch-conventions.md`.

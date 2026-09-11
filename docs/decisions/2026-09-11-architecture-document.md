# An architecture document, with the package view verified

- **Status:** accepted
- **Issue:** [#27](https://github.com/Gabriel2048/wow-insights/issues/27)
- **Date:** 2026-09-11

## Context

Every session that opens this repository — a person or an agent — spends its first
minutes rebuilding the same map: five packages, one interface, two transports, one path
through them. #27 asks for that map to be written down once, kept next to the code, and
kept true. The hard part is the last clause. A diagram that is maintained by asking
people to maintain it is true for a few pull requests and then quietly not.

## Decision

**`ARCHITECTURE.md` at the repository root**, the widely known convention, so it is found
by name next to `AGENTS.md`. `AGENTS.md`'s *Where the code lives* section becomes a pointer.

**Mermaid inside Markdown.** It renders on GitHub and in most editors with nothing
installed; the source is plain text, so an agent reads it directly, a review diffs it,
and a test can parse it. The alternatives — ASCII art, or a C4/D2/PlantUML model — either
cannot be parsed or need a tool nobody else here runs.

**Three views, each answering one question**: what the system talks to; how the packages
relate and where the seams are; what happens on a fight page request. Not more. At
8,000 lines a fourth diagram is a diagram nobody opens.

**Hand-drawn, with the package view verified by a test.** A generated diagram is `go list`
output with arrows; it cannot say which edge is a seam and which is a smell, and the
annotations are the value. So the graph is drawn by hand, and `architecture_test.go`
derives the real in-module import graph with `go/build` and fails when the drawing's
solid edges disagree with it, in either direction. Dotted edges are exempt and are for
the relations that are not imports — the replay transport under the client, the embedded
templates. The two prose views have no such check; the pull request template asks
whether the document was updated or the change was not structural.

**Structural changes are argued to the owner before they are built.** Structural means a
new package, a new edge between packages, a new external system or third-party script, a
new entry point, or a changed seam. Anything inside a package is not. The line is drawn
there because those are the changes that alter the map a reader relies on, and because a
gate on every diagram edit would fire constantly on the sequence view and be ignored.

## Consequences

- A new package or import edge cannot land without touching `ARCHITECTURE.md`; the gate
  says which edge, and the fix is one line of Mermaid.
- The sequence and context views can still rot. The compensating control is a reader who
  notices and the template checkbox; that is weaker, and known to be.
- The document repeats nothing from `AGENTS.md` or the decision records; it points at
  them. The constraints to design around stay in `AGENTS.md`, the reasoning behind each
  seam stays in its record.

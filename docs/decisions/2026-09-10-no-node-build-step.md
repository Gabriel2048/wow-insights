# No node build step for the browser code

- **Status:** accepted
- **Issue:** [#9](https://github.com/Gabriel2048/wow-insights/issues/9)
- **Date:** 2026-09-10

## Context

`templates/fight.html` carries roughly 900 lines of CSS and 300 lines of ES5-style
JavaScript inline. That is enough browser code for TypeScript, a bundler, a linter and a
minifier to look obviously worthwhile — and the first agent to hit a hard browser problem
in #2 or #6 will reasonably propose exactly that. Deciding it once, here, is cheaper than
re-arguing it in a pull request review.

## Decision

No npm, no node, no bundler, no TypeScript. The browser code stays hand-written and is
served from `templates/` (later `static/`, see #17).

## Why

The deployment target for #5 and #7 is **a single static binary**. `go:embed` resolves at
compile time, so assets have to exist on disk when `go build` runs. A node step leaves
only two options, and both break that target:

1. **Commit the generated artefacts.** Now the repository holds build output, reviews
   include minified diffs, and the source and the shipped file can silently disagree.
2. **Make `go build` depend on node.** Now the "single binary" story requires a
   JavaScript toolchain on every builder — the CI runner, the container build, a fresh
   clone, and any agent session. The zero-dependency invariant that CI enforces for Go
   would be quietly false for the product.

The code that would benefit is also not growing. #2's coaching output is server-rendered
HTML, not client logic. The one genuine complaint — that the JavaScript is a single
opaque IIFE with no test coverage of its contract with the server — is addressed instead
by the rendered-output test in #10, which asserts the DOM ids the script resolves are
actually emitted.

## Consequences

- No type checking on the browser code. The DOM-id contract test in #10 is the
  compensating control, and it is weaker.
- No minification. The fight page is large and repetitive; gzip in #11 recovers most of
  what a minifier would, on machine-generated markup that compresses roughly 8-10x.
- If this is ever revisited, revisit it against the single-binary goal, not against the
  ergonomics of the JavaScript.

## Related

- #17 moves the CSS and JS out of the template into `static/` with a content-hash asset
  route. That is a change of *location*, not of this decision.

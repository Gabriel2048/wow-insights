# One work item, one pull request

- **Status:** accepted
- **Issue:** [#23](https://github.com/Gabriel2048/wow-insights/issues/23)
- **Date:** 2026-09-11
- **Supersedes:** the review follow-up consequence of
  `2026-09-10-work-item-and-branch-conventions.md`; the rest of that record stands.

## Context

#10 was planned as three pull requests and the flow was not built for it. Four things
broke at once, and they were the same thing:

- **The board closed the issue on the first merge.** `gh issue develop` creates a branch
  GitHub records as *connected* to the issue, and the project runs an **Auto-close
  issue** workflow that fires when a connected pull request merges. It does not read the
  commit keyword: PR A said `Refs #10`, not `Closes`, and #10 was closed and moved to
  Done at the same second the merge landed (verified 2026-09-10). Every later PR needed
  a manual reopen and a manual status reset.
- **The board could not represent the state.** One PR merged and two to go is neither In
  Progress nor Done in any way the board can show.
- **The branch convention broke.** `{issue-number}-{slug}` assumes one branch per issue;
  the second PR needed an invented slug.
- **`wi.sh branch` could not help.** It derives the slug from the title, so it can only
  make the first branch for an issue.

The tooling, the board and the conventions all assume one work item, one pull request.
The assumption is right; the plan was wrong.

## Decision

**One work item produces exactly one branch and exactly one pull request, and that pull
request closes it.**

**When work does not fit one reviewable pull request, split the issue, not the pull
request.** An issue that cannot be described as a single mergeable change is not a work
item; it is a parent, and it gets children that are.

**Parents and children.** An issue with sub-issues is a parent. A parent has no branch and
no pull request; only leaves do. Both live on the board, side by side: the parent is the
roadmap row, the children are what is happening. A parent is closed when all of its
children are — by the owner, on their own judgement, after the agent that finished the
last child says so. The parent's closure is a decision about whether the whole did what
it set out to do, which is not the same as every part having merged.

**Follow-ups are new issues.** A review fix on an already-merged issue is a new issue
whose title names the one it follows (`Follow-up to #8: name every CI step for what it
checks`), with its own branch and its own pull request. This reverses the 2026-09-10
convention of reusing the closed issue's number with `Refs`: that was a second pull
request on one issue, which is the thing this record forbids, and the board had no row
for the work while it was happening.

**Sizing.** Two constraints, and where they collide the second wins because it is the one
the tooling enforces:

- An issue must be substantial enough to be worth tracking, so the board is not a task
  list.
- An issue must be small enough to land in one pull request a solo maintainer can review
  in one sitting.

The test: **if the acceptance criteria cannot be stated without the word "then", it is
probably two issues.**

**Enforced where it can be.** `wi.sh branch` refuses a parent, a closed issue, an issue
that already has a linked branch and an issue that already has an open pull request,
saying in each case what to do instead. `wi.sh status <n> Done` on the last child says
the parent is ready for the owner. `wi.sh list` marks parents with their progress and
children with their parent.

## #10, the item that prompted this

#10 kept all three of its pull requests. Splitting it mid-flight to satisfy a rule that
did not yet exist would have scattered one piece of work across two numbers, and its
body, its three pull request bodies and its board history all describe a single job. The
cost was two reopen-and-reset cycles, both known and bounded. It is the last item to
span more than one pull request.

## Consequences

- The board's **Auto-close issue** workflow stays. It does the right thing for a
  single-PR issue, which is now every issue.
- The board grows more rows, and smaller ones. That is the intended trade: a truthful
  board over a tidy one.
- A parent's children can be added at any time, including after the parent was written
  as one item and turned out to be several. Splitting is the normal move, not a failure.
- The one-line "PR: <url>" comment on an issue is no longer needed for linking — the
  pull request's `Closes #n` and the linked branch do that — but the implementation
  summary after merge still is, because it is what the next reader of the issue sees.

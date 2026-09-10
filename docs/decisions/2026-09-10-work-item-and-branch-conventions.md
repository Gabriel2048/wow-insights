# Work item and branch conventions

- **Status:** accepted
- **Issue:** [#9](https://github.com/Gabriel2048/wow-insights/issues/9)
- **Date:** 2026-09-10

## Context

Three branch-naming conventions were competing:

1. `{WorkItemNumber}-{WorkItemTitle}`, proposed in [#5](https://github.com/Gabriel2048/wow-insights/issues/5).
2. `wi-<number>-<short-slug>`, prescribed by the work-items skill that was actually in
   use — and machine-local, so it did not arrive with a clone.
3. Whatever an agent invented when it had neither in context.

The live one lost to the roadmap. Worse, the skill created branches with
`git checkout -b`, which produces the right *name* but no GitHub linked-branch relation,
so the board's `Linked pull requests` field stayed empty and an issue's page never showed
the branch working on it.

## Decision

**Branch names are `{issue-number}-{slug}`.** The slug is derived from the issue title,
lowercased, non-alphanumerics collapsed to hyphens, truncated to 60 characters.

**Branches are always created with `gh issue develop --name`**, never `git checkout -b`.
That command is what creates the linked-branch relation; the name alone does not.

The convention is executable rather than prose:

```
.claude/skills/work-items/scripts/wi.sh branch 12
```

**The skill lives in the repository**, at `.claude/skills/work-items/`, so it arrives with
a clone. A user-level or machine-local copy is invisible to a cloud session, a fresh
worktree, or anyone else.

**Decision records are named `YYYY-MM-DD-slug.md`.** #9's original wording said "named by
the issue number that produced it", which breaks as soon as one issue produces two
decisions — as #9 itself did, producing this record and the node one. A sequential
`NNNN-` counter was the first replacement and was rejected too: the padding width is an
arbitrary guess, and the number carries no information. A date does. For an app tracking
a game that changes every patch, *when* something was decided is the first thing you want
to know about whether it still holds. Each record also names its issue in the header.

## Consequences

- A branch created any other way is a deviation, and the missing link on the issue page
  is how you notice.
- `gh issue develop` requires the issue to exist first. That is the intended order: issue,
  then branch, then pull request.
- Review follow-ups on a closed issue reuse that issue's number (`8-ci-step-names`) with
  `Refs #8` rather than `Closes`. A tracking issue for a ten-minute follow-up costs more
  than it carries.

## The lifecycle

1. Issue exists on the board and moves to **In Progress**.
2. `wi.sh branch <n>` — linked branch, checked out.
3. Commit. `./scripts/check.sh` before every push.
4. `gh pr create`, body carrying `Closes #<n>`. **Then stop** — merging is the owner's.
5. CI must be green; the `ci` check is required and there is no bypass.
6. The owner squash-merges. Repository settings put the PR body into the commit message
   on `main`, which is what makes a completed issue readable from `git log` in a clone
   with no network.
7. An implementation summary is commented on the issue, and the board moves to **Done**.

Step 6 is the reason the pull request body is written for a reader rather than a
reviewer: it becomes the permanent record.

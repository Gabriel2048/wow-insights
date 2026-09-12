---
name: work-items
description: Read, comment on, and progress work items on this repository's GitHub Project board, and drive the issue → branch → pull request lifecycle. Use when the user mentions work items, "the board", picking up a task, triaging issues, or opening a PR for a tracked task.
---

# Work items

The board is GitHub Project **#4** owned by user **Gabriel2048**
(<https://github.com/users/Gabriel2048/projects/4>). Issues and pull requests live in
**Gabriel2048/wow-insights** — note the hyphen and the trailing `s`; it does not match
the local directory name.

Everything goes through `scripts/wi.sh` (next to this file) or plain `gh`. Projects v2 is
GraphQL-only, so `gh project ...`, never the REST API. Override `WI_OWNER`, `WI_PROJECT`
or `WI_REPO` in the environment to point at a different board.

## Setup

If a command fails on auth or scope, stop and ask the user to run it themselves — those
need a terminal they control:

```
gh auth login -s project      # or: gh auth refresh -s project
```

The `project` scope is required and a default `gh auth login` does not grant it.

## The lifecycle

This is the flow issue #5 asked for, and `main` now enforces the parts it can.

```
S=.claude/skills/work-items/scripts

$S/wi.sh show 12                       # read the body before acting on it
$S/wi.sh branch 12                     # linked branch 12-<slug>, checked out, board -> In Progress
# ...make the change...
./scripts/check.sh                     # the full gate; CI runs the same list
git add -A && git commit
git push -u origin HEAD
gh pr create --title "..." --body "Closes #12

<what changed, why, and what was decided>"
```

**Then stop.** Do not merge. See *Hard rules* below.

After the owner merges, and only then: comment an implementation summary on the issue and
`$S/wi.sh status 12 Done`. The merge itself closes the issue via `Closes #12`. If #12 was
the last open child of a parent, `status` says so — tell the owner; closing the parent
is theirs to do.

**A finished item's branch is deleted.** It served one pull request and is never needed
again: the squash commit on `main` carries the PR body, and the issue carries the
summary. GitHub deletes the remote branch on merge (`deleteBranchOnMerge` is on), and
`wi.sh status 12 Done` deletes the local `12-*` branch, switching to `main` first if you
are on it. It deletes only a branch whose pull request GitHub reports as merged — not
`git branch -d`'s own test, which `main` being squash-only would fail for every branch —
so a mistaken Done cannot lose work. A clone full of merged branches is the sign someone skipped this step.

## One work item, one pull request

**Every work item produces exactly one branch and exactly one pull request, and that pull
request closes it.** This is the rule everything else here assumes, and
`docs/decisions/2026-09-11-one-work-item-one-pull-request.md` is where it is decided and
argued. In practice:

- **Too big for one reviewable pull request? Split the issue, not the pull request.**
  Make the issue a parent by giving it sub-issues, and work the children. A parent has no
  branch and no pull request of its own — `wi.sh branch` refuses one.
- **A parent closes when its children do, and the owner closes it.** Finishing the last
  child, say so on the parent and stop; whether the whole did what it set out to do is a
  judgement, not a count.
- **A follow-up is a new issue.** A review fix on an already-merged issue gets its own
  issue, titled to name the one it follows — `Follow-up to #8: name every CI step for
  what it checks` — with its own branch and pull request. Never reuse a closed issue's
  number; `wi.sh branch` refuses a closed issue.
- **Both parents and children live on the board.** `wi.sh list` shows a parent as
  `parent 3/11` and a child as `child of #5`; `wi.sh children 5` lists them.

The reason the tooling is this strict is worth knowing, because the failure is silent:
the board runs an **Auto-close issue** workflow and `gh issue develop` creates a
*connected* branch, so the issue closes the moment any linked pull request merges,
whatever the body says — `Refs` included. Verified 2026-09-10 on #10. A second pull
request on an issue is therefore work with no row on the board.

## Writing a work item

An issue is a work item when it is one mergeable change a solo maintainer can review in
one sitting, and substantial enough that tracking it is not noise. Where those pull
against each other, the first wins: it is the one the tooling enforces.

**The sizing test: if you cannot state the acceptance criteria without the word "then",
it is probably two issues.** "CI is required on main, *then* the agent contract is
written" is two. Write the second as a child, or as its own item that depends on the
first.

An issue body carries: context (what is wrong or missing, with evidence), scope (the
change, concretely), acceptance criteria (how a reader knows it landed), what is out of
scope, and what it depends on. Sub-issues are added through the issue's own
**Sub-issues** panel; the parent's body then needs no list of them.

## Hard rules

**Never merge a pull request.** Not `gh pr merge`, not `--auto`. It is the owner's call,
every time. Opening the PR is where your work ends.

**Never push to `main`.** A ruleset blocks it with no bypass actors, so an attempt fails
loudly — but do not attempt it.

**Never move an issue to Done before its PR is merged.** Check first:

```
gh pr view <n> --repo Gabriel2048/wow-insights --json state,mergedAt
```

**Verify `.env` is not staged** before every commit. It holds the Warcraft Logs client id
and secret. `.gitignore` covers it, but check the staged set rather than trusting that:

```
git diff --cached --name-only | grep -qx .env && echo "STOP: .env is staged"
```

## Branch naming

`{issue-number}-{slug}`, always created with `gh issue develop --name` — which is what
creates GitHub's linked-branch relation. `git checkout -b` gives the same name with no
link, and the board's `Linked pull requests` field stays empty. `wi.sh branch` does this
correctly and also moves the item to **In Progress**, because starting a branch is the
moment work begins and a second command is a second thing to forget. Use it. See
`docs/decisions/2026-09-10-work-item-and-branch-conventions.md`.

`wi.sh branch` also refuses what the rule above forbids — a parent, a closed issue, an
issue that already has a branch or an open pull request — and says what to do instead.

## Reading the board

```
$S/wi.sh list                 # number, status, type, title
$S/wi.sh list todo            # filter by status; substring match, case-insensitive
$S/wi.sh show 12              # full detail plus body
$S/wi.sh children 5           # a parent's sub-issues and their state
$S/wi.sh fields               # field names and valid Status options
```

`wi.sh show` prints the body but **not** comments, and there is no comments subcommand.
Prior discussion often lives there — including the implementation summaries this flow
writes — so read them before concluding an item is untouched:

```
gh issue view 12 --repo Gabriel2048/wow-insights --comments
```

The Status options are non-standard. As of 2026-09-10:

| Option | Meaning |
| --- | --- |
| `Long Term Goals` | backlog, not scheduled |
| `Todo/ Short Term Goals` | next up (note the space after `/`) |
| `In Progress` | being worked |
| `Done` | finished and merged |

Re-run `wi.sh fields` if anything looks off; the user can rename options at any time.

`<ref>` is an issue number (`12` or `#12`), a project item id (`PVTI_...`), or a title
substring. A substring matching several items errors out with the candidates rather than
guessing — surface that list rather than picking one.

## Commenting

```
$S/wi.sh comment 12 "Investigated — the retry loop drops the context deadline."
```

Long comments go through a quoted heredoc rather than fighting shell quoting, and stay
plain text; the board renders markdown, but backticks and asterisks inside a shell string
are a needless hazard.

Write as the user would: what you found or did, concretely. No preamble, no restating the
title. **State the limits alongside what was built** — an item that reads as finished but
has caveats is worse than one that names them.

Draft items on the board have no backing issue and cannot be commented on; the script says
so. Tell the user it needs converting to an issue rather than silently creating one.

## Linking a commit without closing

Only `Closes`/`Fixes` link a commit to an issue, and those close it. `Refs #12` creates no
entry on the issue timeline at all — verified on 2026-09-09, when a commit carrying
`Refs #1` left no trace. To attach a commit to an item without closing it, comment the URL:

```
$S/wi.sh comment 1 "Pushed: <sha> — https://github.com/Gabriel2048/wow-insights/commit/<sha>"
```

## Conventions

- Read the item body before acting on it. A one-line title is rarely the whole request.
- Report what you actually did. If a status change succeeded and the comment failed, say
  exactly that.
- Opening the pull request is the last step of every work item; merging is never yours.
  Do not push mid-task.
- Check state before assuming an item is open. The user closes items and moves the board
  themselves, sometimes mid-session.

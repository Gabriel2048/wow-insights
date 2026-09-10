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
$S/wi.sh status 12 "In Progress"
$S/wi.sh branch 12                     # linked branch named 12-<slug>, checked out
# ...make the change...
./scripts/check.sh                     # the full gate; CI runs the same list
git add -A && git commit
git push -u origin HEAD
gh pr create --title "..." --body "Closes #12

<what changed, why, and what was decided>"
$S/wi.sh comment 12 "PR: <url>"
```

**Then stop.** Do not merge. See *Hard rules* below.

After the owner merges, and only then: comment an implementation summary on the issue and
`$S/wi.sh status 12 Done`. The merge itself closes the issue via `Closes #12`.

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
correctly; use it. See `docs/decisions/2026-09-10-work-item-and-branch-conventions.md`.

For a review follow-up on an already-closed issue, reuse that issue's number
(`8-ci-step-names`) and write `Refs #8` rather than `Closes`.

## Reading the board

```
$S/wi.sh list                 # number, status, type, title
$S/wi.sh list todo            # filter by status; substring match, case-insensitive
$S/wi.sh show 12              # full detail plus body
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

## Before committing

Git identity is not set globally on this machine, so `git commit` fails outright. Set it
on the repository, never globally, using the GitHub-provided noreply address:

```
git config --local user.name  "<github username>"
git config --local user.email "<id>+<github username>@users.noreply.github.com"
```

## Conventions

- Read the item body before acting on it. A one-line title is rarely the whole request.
- Report what you actually did. If a status change succeeded and the comment failed, say
  exactly that.
- Push and PR creation are outward-facing. Do not push or open a PR unless asked.
- Check state before assuming an item is open. The user closes items and moves the board
  themselves, sometimes mid-session.

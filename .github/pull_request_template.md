Closes #

## What changed

<!-- The change itself, in a few lines. This body becomes the squash commit
     message on main, so write it for someone reading `git log` in a year. -->

## Why, and what was decided

<!-- Decisions a reader could not recover from the diff. If a decision spans
     files or supersedes an earlier one, add docs/decisions/YYYY-MM-DD-slug.md
     and link it here instead of burying it in this box. -->

## Not as the issue said

<!-- Anything the issue asked for that was done differently, dropped, or
     built and then removed on the owner's call, and anything piggybacked
     from outside it. "None" is a fine answer. Keep this heading as is, so
     `git log --grep` finds every deviation under one name. -->

## Verification

- [ ] `./scripts/check.sh` passes locally
- [ ] Checked against a real report/fight in the running app, or explained why not
- [ ] If builder output changed: named every existing assertion that changed, and why the old one was wrong
- [ ] `ARCHITECTURE.md` updated, or the change is not structural (no new package, edge, external system, entry point or seam)

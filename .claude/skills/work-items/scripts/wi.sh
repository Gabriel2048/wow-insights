#!/usr/bin/env bash
# Helper for GitHub Projects v2 board operations.
# Overridable: WI_OWNER, WI_PROJECT, WI_REPO
set -euo pipefail

OWNER="${WI_OWNER:-Gabriel2048}"
PROJECT="${WI_PROJECT:-4}"
REPO="${WI_REPO:-Gabriel2048/wow-insights}"
LIMIT="${WI_LIMIT:-200}"

die() { echo "error: $*" >&2; exit 1; }

preflight() {
  command -v gh >/dev/null 2>&1 || die "gh not installed: https://cli.github.com"
  gh auth status >/dev/null 2>&1 || die "gh not authenticated. Run: gh auth login -s project"
  # 'project' covers read+write; 'read:project' alone cannot set field values.
  gh auth status 2>&1 | grep -q "'project'" \
    || die "gh token lacks the 'project' scope. Run: gh auth refresh -s project"
}

items_json() {
  gh project item-list "$PROJECT" --owner "$OWNER" --format json --limit "$LIMIT"
}

project_id() {
  gh project view "$PROJECT" --owner "$OWNER" --format json | jq -r '.id'
}

# Resolve a user-supplied ref (issue number, PVTI_ id, or title substring) to one item object.
resolve() {
  local ref="$1" wrapped
  wrapped="$(items_json | jq --arg r "$ref" '
    [ .items[]
      | select(
          (.id == $r)
          or ((.content.number // -1 | tostring) == ($r | ltrimstr("#")))
          or (((.title // "") | ascii_downcase) | contains($r | ascii_downcase))
        )
    ] as $m
    | if ($m | length) == 0 then {err: ("no work item matches: " + $r)}
      elif ($m | length) > 1 then
        {err: ("ambiguous ref \"" + $r + "\" matches:\n  " +
               ($m | map("#" + ((.content.number // 0)|tostring) + "  " + .title)
                   | join("\n  ")) +
               "\nRe-run with an issue number.")}
      else {ok: $m[0]} end')"
  if [ "$(jq -r 'has("err")' <<<"$wrapped")" = "true" ]; then
    die "$(jq -r '.err' <<<"$wrapped")"
  fi
  jq -c '.ok' <<<"$wrapped"
}

# The sub-issue and linked-branch facts about one issue, as one JSON object:
# {state, parent, children_total, children_done, branches:[...], prs:[...]}.
# These are GraphQL-only; gh issue view does not expose them.
issue_facts() {
  local num="$1" owner repo
  owner="${REPO%%/*}"; repo="${REPO#*/}"
  gh api graphql -f query='
    query($owner:String!,$repo:String!,$num:Int!){
      repository(owner:$owner,name:$repo){ issue(number:$num){
        state parent{number}
        subIssuesSummary{total completed}
        linkedBranches(first:10){nodes{ref{name}}}
        closedByPullRequestsReferences(first:10){nodes{number state}}
      } } }' -F owner="$owner" -F repo="$repo" -F num="$num" --jq '
    .data.repository.issue | {
      state, parent: (.parent.number // null),
      children_total: .subIssuesSummary.total,
      children_done: .subIssuesSummary.completed,
      branches: [.linkedBranches.nodes[].ref.name],
      prs: [.closedByPullRequestsReferences.nodes[] | select(.state != "MERGED") | .number]
    }' || die "no issue #$num in $REPO"
}

# Parents and their children, for the list view: one query rather than one per
# item, keyed by issue number.
parents_json() {
  local owner repo
  owner="${REPO%%/*}"; repo="${REPO#*/}"
  gh api graphql --paginate -f query='
    query($owner:String!,$repo:String!,$cursor:String){
      repository(owner:$owner,name:$repo){ issues(first:100,after:$cursor,states:[OPEN,CLOSED]){
        pageInfo{hasNextPage endCursor}
        nodes{ number parent{number} subIssuesSummary{total completed} }
      } } }' -F owner="$owner" -F repo="$repo" --jq '
    .data.repository.issues.nodes[] | {number, parent: (.parent.number // null),
      total: .subIssuesSummary.total, done: .subIssuesSummary.completed}' | jq -s 'map({(.number|tostring): .}) | add // {}'
}

cmd_list() {
  local want="${1:-}" parents
  parents="$(parents_json)"
  # A parent shows how many of its children are done; a child names its
  # parent. Both live on the board, so the relation has to be visible here.
  items_json | jq -r --arg s "$want" --argjson p "$parents" '
    .items
    | map(select($s == "" or (((.status // "") | ascii_downcase) | contains($s | ascii_downcase))))
    | if length == 0 then "(no matching work items)"
      else .[] | ($p[(.content.number // 0)|tostring] // {}) as $f | [
        (if .content.number then "#" + (.content.number|tostring) else "draft" end),
        (.status // "-"),
        (if ($f.total // 0) > 0 then "parent \($f.done)/\($f.total)"
         elif $f.parent then "child of #\($f.parent)"
         else (.content.type // "?") end),
        .title
      ] | @tsv end'
}

cmd_show() {
  [ $# -ge 1 ] || die "usage: wi.sh show <ref>"
  resolve "$1" | jq -r '
    "item-id : \(.id)",
    "title   : \(.title)",
    "type    : \(.content.type // "DraftIssue")",
    "number  : \(if .content.number then "#" + (.content.number|tostring) else "(draft — no issue, cannot comment)" end)",
    "status  : \(.status // "-")",
    "assignees: \((.assignees // []) | join(", "))",
    "labels  : \((.labels // []) | join(", "))",
    "url     : \(.content.url // "-")",
    "",
    "--- body ---",
    (.content.body // "(empty)")'
}

cmd_children() {
  [ $# -ge 1 ] || die "usage: wi.sh children <issue-number>"
  local num="${1#\#}" owner repo
  owner="${REPO%%/*}"; repo="${REPO#*/}"
  gh api graphql -f query='
    query($owner:String!,$repo:String!,$num:Int!){
      repository(owner:$owner,name:$repo){ issue(number:$num){
        subIssues(first:100){nodes{number state title}} } } }' \
    -F owner="$owner" -F repo="$repo" -F num="$num" --jq '
    .data.repository.issue.subIssues.nodes
    | if length == 0 then "(no children: #'"$num"' is a leaf)"
      else .[] | ["#" + (.number|tostring), .state, .title] | @tsv end'
}

cmd_fields() {
  gh project field-list "$PROJECT" --owner "$OWNER" --format json | jq -r '
    .fields[] | "\(.name) [\(.type)]" +
    (if .options then "\n  options: " + (.options | map(.name) | join(", ")) else "" end)'
}

cmd_status() {
  [ $# -ge 2 ] || die "usage: wi.sh status <ref> <status-name>"
  local ref="$1" want="$2"
  local item item_id pid fields fid oid
  item="$(resolve "$ref")"
  item_id="$(jq -r '.id' <<<"$item")"
  pid="$(project_id)"
  fields="$(gh project field-list "$PROJECT" --owner "$OWNER" --format json)"
  fid="$(jq -r '.fields[] | select(.name == "Status") | .id' <<<"$fields")"
  [ -n "$fid" ] && [ "$fid" != "null" ] || die "no 'Status' field on project $PROJECT"
  oid="$(jq -r --arg w "$want" '
    .fields[] | select(.name == "Status") | .options[]
    | select((.name | ascii_downcase) == ($w | ascii_downcase)) | .id' <<<"$fields")"
  if [ -z "$oid" ] || [ "$oid" = "null" ]; then
    die "no Status option \"$want\". Available: $(jq -r '.fields[]|select(.name=="Status")|.options|map(.name)|join(", ")' <<<"$fields")"
  fi
  gh project item-edit --id "$item_id" --project-id "$pid" --field-id "$fid" \
    --single-select-option-id "$oid" >/dev/null
  echo "set Status=$want on $(jq -r '.title' <<<"$item")"

  local num facts parent
  num="$(jq -r '.content.number // empty' <<<"$item")"
  if [ -n "$num" ] && [ "$(tr '[:upper:]' '[:lower:]' <<<"$want")" = "done" ]; then
    # A finished item's branch has served its purpose. GitHub deletes the
    # remote one on merge; this is the local half. main is squash-only, so
    # `git branch -d` would refuse every merged branch (its commits are never
    # ancestors of main); the merged pull request is the real evidence.
    if git rev-parse --git-dir >/dev/null 2>&1; then
      git fetch --prune --quiet origin 2>/dev/null || true
      for b in $(git for-each-ref --format='%(refname:short)' "refs/heads/$num-*"); do
        if [ -z "$(gh pr list --repo "$REPO" --state merged --head "$b" --json number --jq '.[0].number')" ]; then
          echo "warning: no merged pull request for local branch $b, left alone" >&2
          continue
        fi
        if [ "$(git branch --show-current)" = "$b" ]; then
          git checkout --quiet main || { echo "warning: could not leave $b (uncommitted changes?), left alone" >&2; continue; }
        fi
        git branch -D "$b" >/dev/null && echo "deleted local branch $b (its pull request is merged)"
      done
    fi

    # Finishing a child may finish its parent. Closing the parent is the
    # owner's call, so this only says so — loudly enough not to be missed.
    facts="$(issue_facts "$num")"
    parent="$(jq -r '.parent // empty' <<<"$facts")"
    if [ -n "$parent" ]; then
      facts="$(issue_facts "$parent")"
      if [ "$(jq -r '.children_done' <<<"$facts")" = "$(jq -r '.children_total' <<<"$facts")" ]; then
        echo "NOTE: every child of #$parent is now closed. Raise it with the owner: closing the parent and moving it to Done is their call, not yours."
      else
        echo "parent #$parent: $(jq -r '"\(.children_done)/\(.children_total)"' <<<"$facts") children done"
      fi
    fi
  fi
}

# Create the linked feature branch for an issue, named {issue-number}-{slug}.
# `gh issue develop` is what establishes GitHub's linked-branch relation; a plain
# `git checkout -b` produces the same name with no link, which is how the board's
# "Linked pull requests" field ends up permanently empty.
cmd_branch() {
  [ $# -ge 1 ] || die "usage: wi.sh branch <issue-number>"
  local num title slug name
  num="${1#\#}"
  case "$num" in ''|*[!0-9]*) die "branch needs an issue number, got: $1" ;; esac
  title="$(gh issue view "$num" --repo "$REPO" --json title --jq '.title')" \
    || die "no issue #$num in $REPO"
  # One work item, one branch, one pull request. A parent gets none of the
  # three; a closed issue gets a new follow-up issue rather than a second
  # branch. See docs/decisions/2026-09-11-one-work-item-one-pull-request.md.
  local facts
  facts="$(issue_facts "$num")"
  if [ "$(jq -r '.children_total' <<<"$facts")" != "0" ]; then
    die "#$num is a parent with $(jq -r '.children_total' <<<"$facts") children and gets no branch or pull request of its own. Branch from a child: wi.sh children $num"
  fi
  if [ "$(jq -r '.state' <<<"$facts")" = "CLOSED" ]; then
    die "#$num is closed. A follow-up is a new issue whose title names this one (\"Follow-up to #$num: ...\"); branch from that instead"
  fi
  if [ "$(jq -r '.branches | length' <<<"$facts")" != "0" ]; then
    die "#$num already has a branch: $(jq -r '.branches | join(", ")' <<<"$facts"). One work item, one branch — check it out, or split the issue"
  fi
  if [ "$(jq -r '.prs | length' <<<"$facts")" != "0" ]; then
    die "#$num already has an open pull request: #$(jq -r '.prs | join(", #")' <<<"$facts"). One work item, one pull request"
  fi
  slug="$(printf '%s' "$title" \
    | tr '[:upper:]' '[:lower:]' \
    | sed -E 's/[^a-z0-9]+/-/g; s/^-+//; s/-+$//' \
    | cut -c1-60 | sed -E 's/-+$//')"
  [ -n "$slug" ] || die "could not derive a slug from the title of #$num"
  name="$num-$slug"
  gh issue develop "$num" --repo "$REPO" --name "$name" --base main --checkout
  echo "branch: $name  (linked to #$num)"
  # Starting a branch is the moment work begins, so move the board here rather
  # than asking a human to remember a second command. Non-fatal: a failed status
  # update must not leave the caller wondering whether the branch was created.
  cmd_status "$num" "In Progress" || echo "warning: branch created, but the board was not moved" >&2
}

cmd_comment() {
  [ $# -ge 2 ] || die "usage: wi.sh comment <ref> <body>"
  local item num url repo
  item="$(resolve "$1")"
  num="$(jq -r '.content.number // empty' <<<"$item")"
  [ -n "$num" ] || die "that item is a draft issue — it has no comment thread. Convert it to an issue on the board first."
  # An item can live in any repo, so trust its own URL over the WI_REPO default.
  url="$(jq -r '.content.url // empty' <<<"$item")"
  repo="$(sed -nE 's#^https://github\.com/([^/]+/[^/]+)/(issues|pull)/.*#\1#p' <<<"$url")"
  gh issue comment "$num" --repo "${repo:-$REPO}" --body "$2"
}

case "${1:-}" in
  list)    preflight; shift; cmd_list "$@" ;;
  show)    preflight; shift; cmd_show "$@" ;;
  status)  preflight; shift; cmd_status "$@" ;;
  comment) preflight; shift; cmd_comment "$@" ;;
  branch)  preflight; shift; cmd_branch "$@" ;;
  children) preflight; shift; cmd_children "$@" ;;
  fields)  preflight; cmd_fields ;;
  raw)     preflight; items_json ;;
  *) cat >&2 <<'USAGE'
usage: wi.sh <command>
  list [status]        list work items, optionally filtered by status
  show <ref>           full detail + body for one item
  status <ref> <name>  set the Status single-select field
  comment <ref> <body> comment on the item's backing issue
  branch <issue-number>  create the linked feature branch, named {number}-{slug}
  children <issue-number>  list an issue's sub-issues and their state
  fields               list project fields and their options
  raw                  raw item JSON
<ref> = issue number (5 or #5), project item id (PVTI_...), or title substring
USAGE
     exit 1 ;;
esac

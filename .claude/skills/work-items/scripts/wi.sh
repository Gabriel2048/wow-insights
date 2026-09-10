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

cmd_list() {
  local want="${1:-}"
  items_json | jq -r --arg s "$want" '
    .items
    | map(select($s == "" or (((.status // "") | ascii_downcase) | contains($s | ascii_downcase))))
    | if length == 0 then "(no matching work items)"
      else .[] | [
        (if .content.number then "#" + (.content.number|tostring) else "draft" end),
        (.status // "-"),
        (.content.type // "?"),
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
  slug="$(printf '%s' "$title" \
    | tr '[:upper:]' '[:lower:]' \
    | sed -E 's/[^a-z0-9]+/-/g; s/^-+//; s/-+$//' \
    | cut -c1-60 | sed -E 's/-+$//')"
  [ -n "$slug" ] || die "could not derive a slug from the title of #$num"
  name="$num-$slug"
  gh issue develop "$num" --repo "$REPO" --name "$name" --base main --checkout
  echo "branch: $name  (linked to #$num)"
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
  fields)  preflight; cmd_fields ;;
  raw)     preflight; items_json ;;
  *) cat >&2 <<'USAGE'
usage: wi.sh <command>
  list [status]        list work items, optionally filtered by status
  show <ref>           full detail + body for one item
  status <ref> <name>  set the Status single-select field
  comment <ref> <body> comment on the item's backing issue
  branch <issue-number>  create the linked feature branch, named {number}-{slug}
  fields               list project fields and their options
  raw                  raw item JSON
<ref> = issue number (5 or #5), project item id (PVTI_...), or title substring
USAGE
     exit 1 ;;
esac

#!/usr/bin/env bash
# sync-rulesets.sh — Sync standardized rulesets + repo settings across all qws941 repos
#
# Usage:
#   ./scripts/sync-rulesets.sh              # Upsert rulesets + repo settings
#   ./scripts/sync-rulesets.sh --delete-all # Zero-base rebuild (delete all → recreate)
#   ./scripts/sync-rulesets.sh --dry-run    # Preview changes without applying
#   ./scripts/sync-rulesets.sh --repo qws941/terraform  # Target single repo
#
# Rulesets (3):
#   1. default-branch-protection — PR reviews, linear history, merge policy
#   2. code-scanning             — CodeQL alerts on all branches
#   3. tag-protection            — Immutable v* release tags
#
# Repo settings: auto-merge, delete branch, squash (PR_TITLE+PR_BODY), no merge commits
#
# Requires: gh (authenticated), jq

set -euo pipefail

readonly ORG="${GITHUB_ORG:-qws941}"
readonly CONFIG_REPOS="${RULESET_CONFIG_REPOS:-.github qws941}"
readonly EXCLUDED_REPOS="${RULESET_EXCLUDED_REPOS:-terraform}"

# Bypass actors: Admin (RepositoryRole 5), OpenAI Codex connector (1144995), OpenCode agent (1549082)
readonly BYPASS_ACTORS='[{"actor_id": 5, "actor_type": "RepositoryRole", "bypass_mode": "always"}, {"actor_id": 1144995, "actor_type": "Integration", "bypass_mode": "always"}, {"actor_id": 1549082, "actor_type": "Integration", "bypass_mode": "always"}]'

DRY_RUN=false
DELETE_ALL=false
SINGLE_REPO=""

declare -i CREATED=0 UPDATED=0 DELETED=0 ERRORS=0

# ── Colors ────────────────────────────────────────────────────────────────────

RED='\033[0;31m' GREEN='\033[0;32m' YELLOW='\033[0;33m' CYAN='\033[0;36m'
BOLD='\033[1m' NC='\033[0m'

# ── Argument Parsing ──────────────────────────────────────────────────────────

while [[ $# -gt 0 ]]; do
  case $1 in
    --dry-run)    DRY_RUN=true; shift ;;
    --repo)       SINGLE_REPO="$2"; shift 2 ;;
    --delete-all) DELETE_ALL=true; shift ;;
    -h|--help)    sed -n '2,/^$/s/^# //p' "$0"; exit 0 ;;
    *)            echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

for cmd in gh jq; do
  command -v "$cmd" &>/dev/null || { echo "${cmd} is required" >&2; exit 1; }
done

# ── Resolve Repos ─────────────────────────────────────────────────────────────

if [[ -n "$SINGLE_REPO" ]]; then
  REPOS=("$SINGLE_REPO")
else
  mapfile -t REPOS < <(gh repo list "$ORG" --no-archived --json nameWithOwner -q '.[].nameWithOwner' --limit 100)
fi

echo -e "${BOLD}Syncing rulesets → ${#REPOS[@]} repositories${NC}"
$DRY_RUN && echo -e "${YELLOW}  mode: dry-run${NC}"
$DELETE_ALL && echo -e "${RED}  mode: delete-all (zero-base rebuild)${NC}"
echo ""

# ── Helpers ───────────────────────────────────────────────────────────────────

is_config_repo() {
  local name="${1#*/}"
  for cr in $CONFIG_REPOS; do [[ "$name" == "$cr" ]] && return 0; done
  return 1
}

is_excluded_repo() {
  local name="${1#*/}"
  for er in $EXCLUDED_REPOS; do [[ "$name" == "$er" ]] && return 0; done
  return 1
}

# Extract required_status_checks from an existing default-branch ruleset.
# Checks both new and legacy names so preserved checks survive zero-base rebuilds.
get_status_checks() {
  local repo="$1"
  for name in "default-branch-protection" "default-branch-rules"; do
    local rid
    rid=$(gh api "repos/$repo/rulesets" \
      --jq ".[] | select(.name==\"$name\") | .id" 2>/dev/null || echo "")
    if [[ -n "$rid" ]]; then
      gh api "repos/$repo/rulesets/$rid" \
        --jq '[.rules[] | select(.type=="required_status_checks")]
              | if length > 0 then .[0] else empty end' 2>/dev/null || echo ""
      return
    fi
  done
  echo ""
}

# Delete every ruleset in a repo. Returns tab-separated "id\tname" list for logging.
nuke_rulesets() {
  local repo="$1"
  local rulesets
  rulesets=$(gh api "repos/$repo/rulesets" \
    --jq '.[] | "\(.id)\t\(.name)"' 2>/dev/null || echo "")
  [[ -z "$rulesets" ]] && return

  while IFS=$'\t' read -r id name; do
    if $DRY_RUN; then
      echo -e "  ${YELLOW}[dry-run]${NC} would delete: $name"
    else
      if gh api -X DELETE "repos/$repo/rulesets/$id" >/dev/null 2>&1; then
        echo -e "  ${RED}[deleted]${NC} $name"
        DELETED=$((DELETED + 1))
      else
        echo -e "  ${RED}[error]${NC}   delete $name failed"
        ERRORS=$((ERRORS + 1))
      fi
    fi
  done <<< "$rulesets"
}

# Create or update a named ruleset.
upsert_ruleset() {
  local repo="$1" name="$2" payload="$3"

  local existing_id
  existing_id=$(gh api "repos/$repo/rulesets" \
    --jq ".[] | select(.name==\"$name\") | .id" 2>/dev/null || echo "")

  if [[ -n "$existing_id" ]]; then
    if $DRY_RUN; then
      echo -e "  ${YELLOW}[dry-run]${NC} would update: $name"
    else
      if gh api -X PUT "repos/$repo/rulesets/$existing_id" \
           --input - <<< "$payload" >/dev/null 2>&1; then
        echo -e "  ${GREEN}[updated]${NC} $name"
        UPDATED=$((UPDATED + 1))
      else
        echo -e "  ${RED}[error]${NC}   update $name failed"
        ERRORS=$((ERRORS + 1))
      fi
    fi
  else
    if $DRY_RUN; then
      echo -e "  ${YELLOW}[dry-run]${NC} would create: $name"
    else
      if gh api -X POST "repos/$repo/rulesets" \
           --input - <<< "$payload" >/dev/null 2>&1; then
        echo -e "  ${GREEN}[created]${NC} $name"
        CREATED=$((CREATED + 1))
      else
        echo -e "  ${RED}[error]${NC}   create $name failed"
        ERRORS=$((ERRORS + 1))
      fi
    fi
  fi
}

apply_repo_settings() {
  local repo="$1"
  local payload
  payload=$(jq -n '{
    allow_auto_merge: true,
    delete_branch_on_merge: true,
    allow_squash_merge: true,
    allow_merge_commit: false,
    allow_rebase_merge: true,
    squash_merge_commit_title: "PR_TITLE",
    squash_merge_commit_message: "PR_BODY"
  }')

  if $DRY_RUN; then
    echo -e "  ${YELLOW}[dry-run]${NC} would update repo settings"
  else
    if gh api -X PATCH "repos/$repo" --input - <<< "$payload" >/dev/null 2>&1; then
      echo -e "  ${GREEN}[updated]${NC} repo settings"
    else
      echo -e "  ${RED}[error]${NC}   repo settings update failed"
      ERRORS=$((ERRORS + 1))
    fi
  fi
}

# ── Payload Builders ──────────────────────────────────────────────────────────

build_default_branch_payload() {
  local approvals="$1" status_checks="$2"

  local rules
  rules=$(jq -n --argjson a "$approvals" '[
    {"type": "update"},
    {"type": "deletion"},
    {"type": "required_linear_history"},
    {"type": "non_fast_forward"},
    {
      "type": "pull_request",
      "parameters": {
        "required_approving_review_count": $a,
        "dismiss_stale_reviews_on_push": true,
        "require_code_owner_review": false,
        "require_last_push_approval": ($a > 0),
        "required_review_thread_resolution": true,
        "allowed_merge_methods": ["squash", "rebase"]
      }
    }
  ]')

  # Append preserved status checks if present
  if [[ -n "$status_checks" ]]; then
    rules=$(echo "$rules" | jq --argjson sc "$status_checks" '. + [$sc]')
  fi

  jq -n --argjson rules "$rules" --argjson bypass "$BYPASS_ACTORS" '{
    name: "default-branch-protection",
    target: "branch",
    enforcement: "active",
    conditions: {ref_name: {include: ["~DEFAULT_BRANCH"], exclude: []}},
    bypass_actors: $bypass,
    rules: $rules
  }'
}

build_code_scanning_payload() {
  jq -n --argjson bypass "$BYPASS_ACTORS" '{
    name: "code-scanning",
    target: "branch",
    enforcement: "active",
    conditions: {ref_name: {include: ["~ALL"], exclude: []}},
    bypass_actors: $bypass,
    rules: [{
      type: "code_scanning",
      parameters: {
        code_scanning_tools: [{
          tool: "CodeQL",
          security_alerts_threshold: "high_or_higher",
          alerts_threshold: "errors"
        }]
      }
    }]
  }'
}

build_tag_protection_payload() {
  jq -n --argjson bypass "$BYPASS_ACTORS" '{
    name: "tag-protection",
    target: "tag",
    enforcement: "active",
    conditions: {ref_name: {include: ["refs/tags/v*"], exclude: []}},
    bypass_actors: $bypass,
    rules: [
      {"type": "creation"},
      {"type": "update"},
      {"type": "deletion"},
      {"type": "non_fast_forward"}
    ]
  }'
}

# ── Main Loop ─────────────────────────────────────────────────────────────────

for repo in "${REPOS[@]}"; do
  echo -e "${BOLD}=== $repo ===${NC}"

  if is_excluded_repo "$repo"; then
    echo -e "  ${YELLOW}⊘ skipped (excluded)${NC}"
    echo ""
    continue
  fi

  approvals=1
  is_config_repo "$repo" && approvals=0

  if $DELETE_ALL; then
    # Save status checks before nuking (survives zero-base rebuild)
    status_checks=$(get_status_checks "$repo")
    nuke_rulesets "$repo"
  else
    status_checks=$(get_status_checks "$repo")
  fi

  # Repo settings
  apply_repo_settings "$repo"

  # 1. Default branch protection
  payload=$(build_default_branch_payload "$approvals" "$status_checks")
  upsert_ruleset "$repo" "default-branch-protection" "$payload"

  # 2. Code scanning (may fail on private repos without GHAS)
  payload=$(build_code_scanning_payload)
  upsert_ruleset "$repo" "code-scanning" "$payload"

  # 3. Tag protection
  payload=$(build_tag_protection_payload)
  upsert_ruleset "$repo" "tag-protection" "$payload"

  echo ""
done

# ── Summary ───────────────────────────────────────────────────────────────────

echo -e "${BOLD}────────────────────────────────────${NC}"
if $DELETE_ALL; then
  echo -e "Deleted: ${RED}${DELETED}${NC}  Created: ${GREEN}${CREATED}${NC}  Updated: ${GREEN}${UPDATED}${NC}  Errors: ${RED}${ERRORS}${NC}"
else
  echo -e "Created: ${GREEN}${CREATED}${NC}  Updated: ${GREEN}${UPDATED}${NC}  Errors: ${RED}${ERRORS}${NC}"
fi
echo -e "${BOLD}────────────────────────────────────${NC}"

[[ $ERRORS -gt 0 ]] && exit 1
exit 0

#!/usr/bin/env bash
# sync-rulesets.sh — Sync standard rulesets + repo settings across all repos
# Usage: ./scripts/sync-rulesets.sh [--dry-run] [--repo OWNER/REPO]
#
# Standard rulesets (3):
#   1. default-branch-rules  — PR requirements, linear history, merge methods
#   2. code-scanning         — CodeQL security/code alerts
#   3. tag-protection        — Protect v* tags from non-admin changes
#
# Repo settings:
#   - Auto-merge enabled, delete branch on merge
#   - Squash merge enabled, merge commits disabled, rebase enabled
#
# Requires: gh CLI (authenticated)

set -euo pipefail

ORG="qws941"
DRY_RUN=false
SINGLE_REPO=""
# Config-only repos get 0 required approvals (no CI, no code to review)
CONFIG_REPOS=".github qws941"

while [[ $# -gt 0 ]]; do
  case $1 in
    --dry-run) DRY_RUN=true; shift ;;
    --repo) SINGLE_REPO="$2"; shift 2 ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

if [[ -n "$SINGLE_REPO" ]]; then
  REPOS=("$SINGLE_REPO")
else
  mapfile -t REPOS < <(gh repo list "$ORG" --no-archived --json nameWithOwner -q '.[].nameWithOwner' --limit 100)
fi

echo "Syncing rulesets to ${#REPOS[@]} repositories..."
echo ""

is_config_repo() {
  local repo_name="${1#*/}"
  for cr in $CONFIG_REPOS; do
    [[ "$repo_name" == "$cr" ]] && return 0
  done
  return 1
}

# Upsert a ruleset — create if missing, update if exists
upsert_ruleset() {
  local repo="$1" name="$2" payload="$3"

  local existing_id
  existing_id=$(gh api "repos/$repo/rulesets" --jq ".[] | select(.name==\"$name\") | .id" 2>/dev/null || echo "")

  if [[ -n "$existing_id" ]]; then
    if $DRY_RUN; then
      echo "  [dry-run] Would update: $name (id: $existing_id)"
    else
      if gh api -X PUT "repos/$repo/rulesets/$existing_id" --input - <<< "$payload" > /dev/null 2>&1; then
        echo "  [updated] $name"
      else
        echo "  [error]   $name — update failed"
      fi
    fi
  else
    if $DRY_RUN; then
      echo "  [dry-run] Would create: $name"
    else
      if gh api -X POST "repos/$repo/rulesets" --input - <<< "$payload" > /dev/null 2>&1; then
        echo "  [created] $name"
      else
        echo "  [error]   $name — create failed (private repo without GHAS?)"
      fi
    fi
  fi
}

# Apply repo-level settings
apply_repo_settings() {
  local repo="$1"

  local settings_payload
  settings_payload=$(cat <<'SETTINGS'
{
  "allow_auto_merge": true,
  "delete_branch_on_merge": true,
  "allow_squash_merge": true,
  "allow_merge_commit": false,
  "allow_rebase_merge": true,
  "squash_merge_commit_title": "PR_TITLE",
  "squash_merge_commit_message": "PR_BODY"
}
SETTINGS
)

  if $DRY_RUN; then
    echo "  [dry-run] Would update repo settings"
  else
    if gh api -X PATCH "repos/$repo" --input - <<< "$settings_payload" > /dev/null 2>&1; then
      echo "  [updated] repo settings"
    else
      echo "  [error]   repo settings — update failed"
    fi
  fi
}

for repo in "${REPOS[@]}"; do
  echo "=== $repo ==="

  # Determine approval count
  if is_config_repo "$repo"; then
    APPROVALS=0
  else
    APPROVALS=1
  fi

  # 1. Repo settings
  apply_repo_settings "$repo"

  # 2. default-branch-rules
  # Preserve existing required_status_checks if present
  existing_checks=$(gh api "repos/$repo/rulesets" \
    --jq '.[] | select(.name=="default-branch-rules") | .id' 2>/dev/null || echo "")

  status_checks_rule=""
  if [[ -n "$existing_checks" ]]; then
    # Fetch existing status checks from current ruleset
    status_checks_rule=$(gh api "repos/$repo/rulesets/$existing_checks" \
      --jq '[.rules[] | select(.type=="required_status_checks")] | if length > 0 then .[0] | tojson else "" end' 2>/dev/null || echo "")
  fi

  # Build rules array
  if [[ -n "$status_checks_rule" && "$status_checks_rule" != '""' && "$status_checks_rule" != "" ]]; then
    rules_json=$(cat <<RULES
[
  {"type": "creation"},
  {"type": "update"},
  {"type": "deletion"},
  {"type": "required_linear_history"},
  {
    "type": "pull_request",
    "parameters": {
      "required_approving_review_count": $APPROVALS,
      "dismiss_stale_reviews_on_push": true,
      "require_code_owner_review": false,
      "require_last_push_approval": false,
      "required_review_thread_resolution": true,
      "allowed_merge_methods": ["squash", "rebase"]
    }
  },
  {"type": "non_fast_forward"},
  $status_checks_rule
]
RULES
)
  else
    rules_json=$(cat <<RULES
[
  {"type": "creation"},
  {"type": "update"},
  {"type": "deletion"},
  {"type": "required_linear_history"},
  {
    "type": "pull_request",
    "parameters": {
      "required_approving_review_count": $APPROVALS,
      "dismiss_stale_reviews_on_push": true,
      "require_code_owner_review": false,
      "require_last_push_approval": false,
      "required_review_thread_resolution": true,
      "allowed_merge_methods": ["squash", "rebase"]
    }
  },
  {"type": "non_fast_forward"}
]
RULES
)
  fi

  default_branch_payload=$(cat <<PAYLOAD
{
  "name": "default-branch-rules",
  "target": "branch",
  "enforcement": "active",
  "conditions": {
    "ref_name": {
      "include": ["~DEFAULT_BRANCH"],
      "exclude": []
    }
  },
  "bypass_actors": [
    {
      "actor_id": 5,
      "actor_type": "RepositoryRole",
      "bypass_mode": "always"
    }
  ],
  "rules": $rules_json
}
PAYLOAD
)
  upsert_ruleset "$repo" "default-branch-rules" "$default_branch_payload"

  # 3. code-scanning (may fail on private repos without GHAS)
  code_scanning_payload=$(cat <<'PAYLOAD'
{
  "name": "code-scanning",
  "target": "branch",
  "enforcement": "active",
  "conditions": {
    "ref_name": {
      "include": ["~ALL"],
      "exclude": []
    }
  },
  "bypass_actors": [
    {
      "actor_id": 5,
      "actor_type": "RepositoryRole",
      "bypass_mode": "always"
    }
  ],
  "rules": [
    {
      "type": "code_scanning",
      "parameters": {
        "code_scanning_tools": [
          {
            "tool": "CodeQL",
            "security_alerts_threshold": "high_or_higher",
            "alerts_threshold": "errors"
          }
        ]
      }
    }
  ]
}
PAYLOAD
)
  upsert_ruleset "$repo" "code-scanning" "$code_scanning_payload"

  # 4. tag-protection (v* tags)
  tag_protection_payload=$(cat <<'PAYLOAD'
{
  "name": "tag-protection",
  "target": "tag",
  "enforcement": "active",
  "conditions": {
    "ref_name": {
      "include": ["refs/tags/v*"],
      "exclude": []
    }
  },
  "bypass_actors": [
    {
      "actor_id": 5,
      "actor_type": "RepositoryRole",
      "bypass_mode": "always"
    }
  ],
  "rules": [
    {"type": "creation"},
    {"type": "update"},
    {"type": "deletion"},
    {"type": "non_fast_forward"}
  ]
}
PAYLOAD
)
  upsert_ruleset "$repo" "tag-protection" "$tag_protection_payload"

  echo ""
done

echo "Done!"

#!/usr/bin/env bash
# sync-labels.sh — Sync standard labels across all org repos
# Usage: ./scripts/sync-labels.sh [--dry-run] [--repo OWNER/REPO]
#
# Requires: gh CLI (authenticated), python3 + pyyaml

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LABELS_FILE="${SCRIPT_DIR}/labels.yml"
ORG="${GITHUB_ORG:-qws941}"
DRY_RUN=false
SINGLE_REPO=""

while [[ $# -gt 0 ]]; do
  case $1 in
    --dry-run) DRY_RUN=true; shift ;;
    --repo) SINGLE_REPO="$2"; shift 2 ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

if [[ ! -f "$LABELS_FILE" ]]; then
  echo "ERROR: labels.yml not found at $LABELS_FILE"
  exit 1
fi

# Parse labels.yml with Python
read_labels() {
  python3 -c "
import yaml, json, sys
with open('$LABELS_FILE') as f:
    labels = yaml.safe_load(f)
print(json.dumps(labels))
"
}

LABELS_JSON=$(read_labels)
LABEL_COUNT=$(echo "$LABELS_JSON" | python3 -c "import json,sys; print(len(json.load(sys.stdin)))")
echo "Loaded $LABEL_COUNT labels from labels.yml"

# Get list of repos
if [[ -n "$SINGLE_REPO" ]]; then
  REPOS=("$SINGLE_REPO")
else
  mapfile -t REPOS < <(gh repo list "$ORG" --no-archived --json nameWithOwner -q '.[].nameWithOwner' --limit 100)
fi

echo "Syncing labels to ${#REPOS[@]} repositories..."
echo ""

for repo in "${REPOS[@]}"; do
  echo "=== $repo ==="

  # Get existing labels
  EXISTING=$(gh label list --repo "$repo" --json name,color,description --limit 200 2>/dev/null || echo '[]')

  echo "$LABELS_JSON" | python3 -c "
import json, sys, subprocess

labels = json.load(sys.stdin)
existing_raw = '''$EXISTING'''
existing = {l['name']: l for l in json.loads(existing_raw)}
dry_run = $([[ "$DRY_RUN" == "true" ]] && echo "True" || echo "False")
repo = '$repo'

for label in labels:
    name = label['name']
    color = label['color']
    desc = label.get('description', '')

    if name in existing:
        ex = existing[name]
        # Compare (GitHub returns color without #)
        if ex.get('color', '').lower() == color.lower() and ex.get('description', '') == desc:
            print(f'  [skip] {name} (unchanged)')
            continue
        action = 'update'
        cmd = ['gh', 'label', 'edit', name, '--repo', repo, '--color', color, '--description', desc]
    else:
        action = 'create'
        cmd = ['gh', 'label', 'create', name, '--repo', repo, '--color', color, '--description', desc]

    if dry_run:
        print(f'  [dry-run] Would {action}: {name}')
    else:
        try:
            subprocess.run(cmd, check=True, capture_output=True, text=True)
            print(f'  [{action}d] {name}')
        except subprocess.CalledProcessError as e:
            print(f'  [error] {action} {name}: {e.stderr.strip()}')
"
  echo ""
done

echo "Done!"

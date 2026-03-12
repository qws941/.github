# PROJECT KNOWLEDGE BASE

## OVERVIEW

`.github/` owns the GitHub-native config surfaces that sync to downstream repos: the sync manifest, PR metadata, release rules, issue forms, and the workflow tree.

## STRUCTURE

```text
.github/
├── workflows/                # workflow policies and automations; child AGENTS applies
├── ISSUE_TEMPLATE/           # four synced issue forms
├── sync.yml                  # canonical sync manifest
├── labeler.yml               # path-based PR labels
├── release-drafter.yml       # changelog categories
├── PULL_REQUEST_TEMPLATE.md  # What/Why/Kind/Changes/Testing/Checklist
├── CODEOWNERS                # local-only, not synced
└── FUNDING.yml               # account-level funding metadata
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Change what syncs downstream | `.github/sync.yml` | Keep repo list sorted and synced files generic |
| Tune PR path labels | `.github/labeler.yml` | Five label buckets map file globs to SSoT labels |
| Tune release note grouping | `.github/release-drafter.yml` | Category-to-changelog mapping |
| Edit issue forms | `.github/ISSUE_TEMPLATE/*.yml` | All four forms sync downstream |
| Change workflow behavior | `.github/workflows/AGENTS.md` | Deeper contract for templates/callers/orchestrators |

## CONVENTIONS

- `.github/sync.yml` is the source of truth for both synced file paths and downstream repo targets.
- `.github/CODEOWNERS` stays local; downstream repos keep their own enforcement rules.
- Issue templates, PR templates, and workflow callers in this subtree must stay reusable across all downstream repos.
- Workflow callers listed in `sync.yml` sync downstream; upstream-only orchestrators such as `sync-files.yml` and `sync-labels.yml` do not.

## ANTI-PATTERNS (THIS SUBTREE)

- Do not add repo-specific assumptions to issue forms, PR templates, labeler rules, or release categories.
- Do not add `_*.yml` reusable workflow templates to the sync manifest.
- Do not treat `.github/CODEOWNERS` like a synced governance file.
- Do not change the sync manifest without checking the affected thin caller or config file still exists.

## NOTES

- `automation-health.yml` is part of the synced caller set and is newer than the previous single-root AGENTS summary.
- `ISSUE_TEMPLATE/` and `profile/` stay parent-covered because they are static, low-change surfaces with no command layer.

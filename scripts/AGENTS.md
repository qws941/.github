# PROJECT KNOWLEDGE BASE

## OVERVIEW

`scripts/` holds the repo's only executable source code: two Go CLIs for repo onboarding and label synchronization, plus the label SSoT data file they operate on.

## STRUCTURE

```text
scripts/
├── labels.yml        # 27 standard labels shared across downstream repos
├── onboard-repo.go   # repo onboarding CLI
└── sync-labels.go    # label sync CLI with worker pool
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Change standard labels | `scripts/labels.yml` | Type, priority, status, size, and automation labels |
| Change label sync behavior | `scripts/sync-labels.go` | Supports `--dry-run`, `--repo`, `--delete`, `--workers` |
| Change onboarding flow | `scripts/onboard-repo.go` | Updates `sync.yml`, syncs labels, creates webhooks, verifies results |
| Change verification output | `scripts/sync-labels.go` `printSummary`, `scripts/onboard-repo.go` `stepVerify` | Human-readable operator output |

## CODE MAP

| Symbol | Location | Role |
|--------|----------|------|
| `main` | `scripts/sync-labels.go` | Parses CLI flags, loads labels, and starts worker fan-out |
| `syncRepo` | `scripts/sync-labels.go` | Reconciles labels for a single target repo |
| `printSummary` | `scripts/sync-labels.go` | Emits per-repo results table |
| `main` | `scripts/onboard-repo.go` | Orchestrates the 5-step onboarding flow |
| `stepSyncYml` | `scripts/onboard-repo.go` | Adds a repo to `.github/sync.yml` |
| `stepVerify` | `scripts/onboard-repo.go` | Confirms sync, labels, hooks, and dependabot state |

## CONVENTIONS

- These CLIs are stdlib-oriented Go programs that shell out to `gh` instead of using a Go GitHub SDK.
- Dry-run support is first-class; prefer preview mode before mutating downstream repos.
- `sync-labels.go` treats `scripts/labels.yml` as the only label source of truth.
- `onboard-repo.go` defaults bare repo names to `qws941/<repo>` and keeps sync manifest edits alphabetical.

## ANTI-PATTERNS (THIS SUBTREE)

- Do not add labels directly in code when they belong in `scripts/labels.yml`.
- Do not remove dry-run or worker-pool behavior from the CLIs.
- Do not add third-party Go dependencies for work already handled by the stdlib plus `gh`.
- Do not change sync-manifest or webhook logic without keeping `stepVerify` aligned.

## COMMANDS

```bash
go run scripts/sync-labels.go --dry-run
go run scripts/sync-labels.go --repo qws941/opencode
go run scripts/sync-labels.go --delete
go run scripts/onboard-repo.go --dry-run qws941/new-repo
```

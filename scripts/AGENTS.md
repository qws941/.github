# PROJECT KNOWLEDGE BASE

## OVERVIEW

`scripts/` holds the repo's only executable source code: three Go CLIs for repo onboarding, label synchronization, and developer git-flow automation, plus the label SSoT data file they operate on.

## STRUCTURE

```text
scripts/
├── git-flow.go       # developer git-flow automation CLI
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
| Automate git-flow lifecycle | `scripts/git-flow.go` | Branch creation, PR, merge, sync, status — supports `--dry-run` |

## CODE MAP

| Symbol | Location | Role |
|--------|----------|------|
| `main` | `scripts/sync-labels.go` | Parses CLI flags, loads labels, and starts worker fan-out |
| `syncRepo` | `scripts/sync-labels.go` | Reconciles labels for a single target repo |
| `printSummary` | `scripts/sync-labels.go` | Emits per-repo results table |
| `main` | `scripts/onboard-repo.go` | Orchestrates the 5-step onboarding flow |
| `stepSyncYml` | `scripts/onboard-repo.go` | Adds a repo to `.github/sync.yml` |
| `stepVerify` | `scripts/onboard-repo.go` | Confirms sync, labels, hooks, and dependabot state |

| `main` | `scripts/git-flow.go` | Dispatches start/pr/finish/status/sync subcommands |
| `cmdStart` | `scripts/git-flow.go` | Creates a validated feature branch from master |
| `cmdPR` | `scripts/git-flow.go` | Pushes and creates a PR with auto-generated title and body |
| `cmdFinish` | `scripts/git-flow.go` | Squash-merges PR after CI, draft, and mergeable gates pass |
| `cmdStatus` | `scripts/git-flow.go` | Read-only branch and PR status summary |
| `cmdSync` | `scripts/git-flow.go` | Rebases feature branch onto origin/master |
| `requireCleanWorktree` | `scripts/git-flow.go` | Guards mutating commands against dirty worktree |
| `requireFeatureBranch` | `scripts/git-flow.go` | Validates current branch is a feature branch matching `branchPattern` |
| `generatedBody` | `scripts/git-flow.go` | Generates PR body with 8 template sections (What/Why/Kind/Changes/Testing/Breaking/Checklist/Issues) |
| `resolveCheckConclusion` | `scripts/git-flow.go` | Normalizes CI check status across CheckRun and StatusContext objects |
| `statusChecksSummary` | `scripts/git-flow.go` | Aggregates CI check results into pass/fail/pending summary |

## CONVENTIONS

- These CLIs are stdlib-oriented Go programs that shell out to `gh` instead of using a Go GitHub SDK.
- Dry-run support is first-class; prefer preview mode before mutating downstream repos.
- `sync-labels.go` treats `scripts/labels.yml` as the only label source of truth.
- `onboard-repo.go` defaults bare repo names to `qws941/<repo>` and keeps sync manifest edits alphabetical.
- `git-flow.go` automates the trunk-based development lifecycle: branch creation → PR → merge → cleanup.
- `git-flow.go` finish gates: PR must be open, not draft, mergeable, and all CI checks passed (pending checks block merge).
## ANTI-PATTERNS (THIS SUBTREE)

- Do not add labels directly in code when they belong in `scripts/labels.yml`.
- Do not remove dry-run or worker-pool behavior from the CLIs.
- Do not add third-party Go dependencies for work already handled by the stdlib plus `gh`.
- Do not change sync-manifest or webhook logic without keeping `stepVerify` aligned.
- Do not bypass git-flow branch-name validation pattern; it enforces the project's branch naming convention.

## COMMANDS

```bash
go run scripts/sync-labels.go --dry-run
go run scripts/sync-labels.go --repo qws941/opencode
go run scripts/sync-labels.go --delete
go run scripts/onboard-repo.go --dry-run qws941/new-repo
go run scripts/git-flow.go status
go run scripts/git-flow.go start --dry-run feat/my-feature
go run scripts/git-flow.go pr --dry-run
go run scripts/git-flow.go finish --dry-run
go run scripts/git-flow.go sync --dry-run
```

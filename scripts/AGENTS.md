# PROJECT KNOWLEDGE BASE

## OVERVIEW

`scripts/` holds the repo's executable source code: six Go CLIs for repo onboarding, label synchronization, git-flow automation, action pin auditing, sync preview, and CODEOWNERS validation, plus the label SSoT data file, test files, and a shared retry package.

## STRUCTURE

```text
scripts/
├── audit-action-pins.go      # action SHA-pin audit CLI with worker pool
├── git-flow.go               # developer git-flow automation CLI
├── go.mod                    # Go module definition
├── go.sum                    # dependency checksums
├── internal/
│   └── retry/                # shared retry-with-backoff package
│       ├── retry.go
│       └── retry_test.go
├── labels.yml                # 27 standard labels shared across downstream repos
├── onboard-repo.go           # repo onboarding CLI
├── onboard-repo_test.go      # tests for onboard-repo (build tag: onboard_repo)
├── preview-sync.go           # sync diff preview CLI
├── sync-labels.go            # label sync CLI with worker pool
├── sync-labels_test.go       # tests for sync-labels (build tag: sync_labels)
└── validate-codeowners.go    # CODEOWNERS validation CLI with worker pool
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Change standard labels | `scripts/labels.yml` | Type, priority, status, size, and automation labels |
| Change label sync behavior | `scripts/sync-labels.go` | Supports `--dry-run`, `--repo`, `--delete`, `--workers` |
| Change onboarding flow | `scripts/onboard-repo.go` | Updates `sync.yml`, syncs labels, creates webhooks, verifies results |
| Audit action SHA pins | `scripts/audit-action-pins.go` | Scans downstream workflows for unpinned actions, opens issues |
| Preview sync changes | `scripts/preview-sync.go` | Compares local files with downstream versions, shows diffs |
| Validate CODEOWNERS | `scripts/validate-codeowners.go` | Checks CODEOWNERS across downstream repos for format and path issues |
| Automate git-flow lifecycle | `scripts/git-flow.go` | Branch creation, PR, merge, sync, status — supports `--dry-run` |
| Change retry behavior | `scripts/internal/retry/retry.go` | Shared exponential-backoff retry used by CLIs |
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
| `main` | `scripts/git-flow.go` | Dispatches start/pr/finish/status/sync subcommands |
| `cmdStart` | `scripts/git-flow.go` | Creates a validated feature branch from master |
| `cmdPR` | `scripts/git-flow.go` | Pushes and creates a PR with auto-generated title and body |
| `cmdFinish` | `scripts/git-flow.go` | Squash-merges PR after CI, draft, and mergeable gates pass |
| `cmdStatus` | `scripts/git-flow.go` | Read-only branch and PR status summary |
| `cmdSync` | `scripts/git-flow.go` | Rebases feature branch onto origin/master |
| `requireCleanWorktree` | `scripts/git-flow.go` | Guards mutating commands against dirty worktree |
| `requireFeatureBranch` | `scripts/git-flow.go` | Validates current branch is a feature branch matching `branchPattern` |
| `generatedTitle` | `scripts/git-flow.go` | Infers PR title from branch name with scope prefix |
| `generatedBody` | `scripts/git-flow.go` | Generates PR body with 8 template sections |
| `resolveCheckConclusion` | `scripts/git-flow.go` | Normalizes CI check status across CheckRun and StatusContext objects |
| `statusChecksSummary` | `scripts/git-flow.go` | Aggregates CI check results into pass/fail/pending summary |
| `main` | `scripts/audit-action-pins.go` | Scans downstream workflows for unpinned/outdated action SHA pins |
| `main` | `scripts/preview-sync.go` | Previews sync diffs between local and downstream file versions |
| `main` | `scripts/validate-codeowners.go` | Validates CODEOWNERS format and paths across downstream repos |

## CONVENTIONS

- These CLIs are stdlib-oriented Go programs that shell out to `gh` instead of using a Go GitHub SDK.
- Dry-run support is first-class; prefer preview mode before mutating downstream repos.
- `sync-labels.go` treats `scripts/labels.yml` as the only label source of truth.
- `onboard-repo.go` defaults bare repo names to `qws941/<repo>` and keeps sync manifest edits alphabetical.
- `git-flow.go` automates the trunk-based development lifecycle: branch creation → PR → merge → cleanup.
- `git-flow.go` finish gates: PR must be open, not draft, mergeable, and all CI checks passed (pending checks block merge).
- Test files use build tags (`//go:build onboard_repo`, `//go:build sync_labels`) to isolate per-CLI tests.
- `audit-action-pins.go` and `validate-codeowners.go` use `//go:build ignore` since they are run directly via `go run`.

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
go run scripts/audit-action-pins.go
go run scripts/preview-sync.go
go run scripts/validate-codeowners.go
```

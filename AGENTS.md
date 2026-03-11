# PROJECT KNOWLEDGE BASE

**Generated:** 2026-03-11
**Commit:** `baa4b43`
**Branch:** `master`

## OVERVIEW

GitHub community-health and automation SSoT for `qws941` repositories. This repo owns synced governance files, thin GitHub Actions callers, reusable workflow templates, and the Go CLIs that keep downstream repos aligned.

## STRUCTURE

```text
./
├── .github/              # synced GitHub-owned surfaces; child AGENTS applies
│   ├── workflows/        # templates, synced callers, upstream-only orchestrators
│   └── ISSUE_TEMPLATE/   # synced issue forms
├── scripts/              # Go CLIs + label SSoT; child AGENTS applies
├── profile/              # GitHub account profile README
├── AGENTS.md
├── CONTRIBUTING.md
├── OWNERS
└── SECURITY.md
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Add or remove synced files | `.github/sync.yml` | Canonical sync manifest and downstream repo list |
| Edit GitHub config surfaces | `.github/AGENTS.md` | Sync rules, forms, labeler, release drafter |
| Edit workflow behavior | `.github/workflows/AGENTS.md` | Template/caller split and upstream-only workflows |
| Update label automation | `scripts/AGENTS.md` | `labels.yml`, `sync-labels.go`, `onboard-repo.go` |
| Change issue forms | `.github/ISSUE_TEMPLATE/*.yml` | Synced to downstream repos; keep generic |
| Update contribution policy | `CONTRIBUTING.md` | Trunk-based dev, commit format, review rules |
| Update security contact/process | `SECURITY.md` | `security@jclee.me`, 48h acknowledgement SLA |
| Edit the profile page | `profile/README.md` | Static account README, no automation |

## CODE MAP

| Symbol / Control Point | Type | Location | Role |
|------------------------|------|----------|------|
| `sync-files.yml` | workflow | `.github/workflows/sync-files.yml` | Pushes synced files directly to downstream repos |
| `downstream-automation-audit` | workflow | `.github/workflows/downstream-automation-audit.yml` | Opens downstream issues when shared automation drifts or degrades |
| `main` | function | `scripts/onboard-repo.go` | Orchestrates repo onboarding steps and verification |
| `stepSyncYml` | function | `scripts/onboard-repo.go` | Adds a target repo to `.github/sync.yml` |
| `main` | function | `scripts/sync-labels.go` | Runs label sync CLI and worker-pool fan-out |
| `syncRepo` | function | `scripts/sync-labels.go` | Creates, updates, or deletes labels per target repo |

## CONVENTIONS

- Thin caller workflows live in `.github/workflows/*.yml` and sync downstream; reusable `_*.yml` templates stay upstream-only.
- Synced files must remain repo-agnostic because `sync-files.yml` pushes them directly with `SKIP_PR: true`.
- GitHub Actions are SHA-pinned with trailing version comments like `# v6.0.2`.
- Operational commands are `go run scripts/...`; there is no application build or test suite in this repo.
- `AGENTS.md` files stay local to this repo and are never part of the sync manifest.

## ANTI-PATTERNS (THIS PROJECT)

- Never add repo-specific content to synced forms, templates, or thin caller workflows.
- Never sync `.github/dependabot.yml`, `.github/CODEOWNERS`, or reusable `_*.yml` workflow templates.
- Never replace pinned action SHAs with mutable tags such as `@v4`.
- Never hardcode secrets, tokens, or infrastructure coordinates in workflows or scripts.
- Never use merge commits or long-lived branches; the contribution policy is trunk-based with squash-first history.

## UNIQUE STYLES

- Personal-account `.github` repo, so reusable workflow references use the double path `qws941/.github/.github/workflows/_*.yml@master`.
- The workflow directory mixes three patterns in one flat tree: reusable templates, synced thin callers, and upstream-only orchestrators/audits.
- Verification is workflow-driven rather than test-driven; the repo intentionally has no test directory.

## COMMANDS

```bash
go run scripts/sync-labels.go --dry-run
go run scripts/sync-labels.go --repo qws941/terraform
go run scripts/sync-labels.go --delete
go run scripts/onboard-repo.go --dry-run qws941/new-repo
```

## NOTES

- Latest observed GitHub Actions run: `Automation Health` succeeded on `master` at `https://github.com/qws941/.github/actions/runs/22940365707`.
- `profile/README.md` still describes this repo as `Shell`; the actual source surface is now mostly YAML + Go.
- Child contracts exist at `.github/AGENTS.md`, `.github/workflows/AGENTS.md`, and `scripts/AGENTS.md`.

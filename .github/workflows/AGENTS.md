# PROJECT KNOWLEDGE BASE

## OVERVIEW

`.github/workflows/` is a flat but high-signal directory: reusable `workflow_call` templates, synced thin callers, and a small set of upstream-only orchestration and audit workflows coexist here.

## STRUCTURE

```text
.github/workflows/
├── _*.yml                          # reusable templates, never synced
├── {caller}.yml                    # thin synced callers for downstream repos
├── sync-files.yml                  # direct-push file sync orchestrator
├── sync-labels.yml                 # label sync orchestrator for scripts/sync-labels.go
└── downstream-automation-audit.yml # downstream drift/health auditor
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Change reusable CI behavior | `.github/workflows/_ci-node.yml`, `.github/workflows/_ci-python.yml` | Parameterized `workflow_call` templates |
| Change PR automation | `.github/workflows/_auto-merge.yml`, `.github/workflows/_auto-approve-runs.yml` | Trusted-actor and recovery logic lives here |
| Change Codex automation | `.github/workflows/_codex-*.yml` | Issue and PR automation templates |
| Change downstream sync behavior | `.github/workflows/sync-files.yml` | Direct push via `repo-file-sync-action` |
| Change downstream audit behavior | `.github/workflows/downstream-automation-audit.yml` | Flags missing, drifted, disabled, stale, or unhealthy callers |
| Change label sync trigger | `.github/workflows/sync-labels.yml` | Runs `go run scripts/sync-labels.go` |

## CONVENTIONS

- `_*.yml` means reusable upstream template with `on: workflow_call`; no underscore means thin caller or standalone upstream workflow.
- Thin callers own triggers and permissions only, then delegate to `uses: qws941/.github/.github/workflows/_*.yml@master`.
- All third-party actions stay SHA-pinned with trailing version comments.
- `sync-files.yml` and `downstream-automation-audit.yml` are upstream-only control planes, not templates and not synced callers.
- Scheduled health workflows are part of the repo contract; audit logic expects downstream caller paths and blob SHAs to match the upstream copies.

## ANTI-PATTERNS (THIS SUBTREE)

- Do not put business logic in a synced thin caller when a reusable template already owns it.
- Do not copy `_*.yml` template logic into downstream repos; callers should keep using `uses:`.
- Do not replace pinned action SHAs with floating tags.
- Do not slip repo-specific labels, secrets, or actor assumptions into synced callers.
- Do not edit caller filenames or add new callers without updating `.github/sync.yml`.

## NOTES

- Current shape: 47 workflows total, including 23 reusable templates and 20 synced callers plus upstream-only orchestrators.
- Representative caller/template pair: `auto-merge.yml` -> `_auto-merge.yml`.
- `downstream-automation-audit.yml` fingerprints findings and opens issues in downstream repos when shared automation degrades.

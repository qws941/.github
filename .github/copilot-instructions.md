# Copilot Instructions

This repository is the **Single Source of Truth (SSoT)** for governance files
across all `qws941` GitHub repositories.

## Context

- Owner: JC Lee (@qws941) — personal GitHub account (not an org)
- Style: Google3-style monorepo conventions (OWNERS files, BUILD.bazel, trunk-based dev)
- Conventions: Conventional Commits (`feat:`, `fix:`, `chore:`, `docs:`, `ci:`)
- License: MIT
- Infrastructure: Terraform + Bazel homelab IaC

## Repository Purpose

Files in this repo are auto-synced to 11+ downstream repos via
`BetaHuhn/repo-file-sync-action`. Changes here propagate everywhere.

## Key Files

| File | Purpose | Synced? |
|------|---------|---------|
| `OWNERS` | K8s-style ownership (approvers + reviewers) | Yes (all repos) |
| `LICENSE` | MIT License | Yes (owned repos, not forks) |
| `.editorconfig` | Editor formatting rules | Yes (all repos) |
| `CONTRIBUTING.md` | Contribution guidelines | No (GitHub auto-inherits) |
| `SECURITY.md` | Security policy | No (GitHub auto-inherits) |
| `CODE_OF_CONDUCT.md` | Contributor Covenant 2.1 | No (GitHub auto-inherits) |
| `.github/sync.yml` | File sync targets config | N/A |
| `.github/labeler.yml` | PR auto-label rules | Yes (all repos) |

## Rules

1. SHA-pin all GitHub Actions with `# vN` version comment
2. Never hardcode IPs — use Terraform variables
3. All PRs need conventional commit titles
4. Synced files must remain generic (no repo-specific content)
5. `dependabot.yml` and `CODEOWNERS` are repo-specific — never sync them

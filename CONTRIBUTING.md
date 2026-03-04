# Contributing

Thank you for your interest in contributing. This guide covers the conventions
used across all repositories under [qws941](https://github.com/qws941).

These conventions are inspired by
[Google's Engineering Practices](https://google.github.io/eng-practices/),
[Kubernetes governance](https://github.com/kubernetes/community), and
[Angular commit conventions](https://github.com/angular/angular/blob/main/CONTRIBUTING.md).

---

## Table of Contents

1. [Development Model](#1-development-model)
2. [Change Descriptions](#2-change-descriptions)
3. [Commit Messages](#3-commit-messages)
4. [Branch Naming](#4-branch-naming)
5. [Pull Requests](#5-pull-requests)
6. [Code Review](#6-code-review)
7. [Issues](#7-issues)
8. [Code Quality](#8-code-quality)
9. [OWNERS and Governance](#9-owners-and-governance)

---

## 1. Development Model

We follow **trunk-based development** — all work targets the default branch
(`master` or `main`) with short-lived feature branches.

### 1.1. Rules

- **No long-lived branches.** Feature branches should live at most a few days.
- **Small changes.** Target **~200 lines of code** per PR. Large PRs are harder
  to review and more likely to introduce defects.
- **Squash merge only.** All PRs are squash-merged to maintain a linear,
  readable commit history.
- **Feature flags** over feature branches for incomplete work that must be
  merged incrementally.

### 1.2. Workflow

```
1. Fork or branch from the default branch
2. Make focused, atomic changes
3. Submit a pull request
4. Address review feedback
5. Squash merge when approved
```

---

## 2. Change Descriptions

Every PR title and body follows the **CL (changelist) description format**
from [Google eng-practices](https://google.github.io/eng-practices/review/developer/cl-descriptions.html).

### 2.1. Title Format

```
<type>(<scope>): <imperative summary>
```

- **Imperative mood**: "add", "fix", "remove" — not "added", "fixes", "removing"
- **≤ 72 characters** total
- **Lowercase** (except proper nouns)

### 2.2. Body Format

```
What: Brief description of WHAT changed.

Why: Explain WHY this change is necessary. Link to issue/bug.

Testing: How the change was verified.
```

The body explains **why**, not **how**. The code shows how.

### 2.3. Examples

```
feat(traefik): add rate limiting middleware for API routes

What: Added rate limiting configuration to Traefik dynamic config.
Why: API routes were unprotected against burst traffic. Closes #42.
Testing: Verified with curl load test against staging.
```

```
fix(grafana): resolve dashboard UID collision on import

What: Prefixed dashboard UIDs with stack identifier.
Why: Two dashboards shared the same UID causing silent overwrites.
Testing: terraform plan shows no drift after re-import.
```

---

## 3. Commit Messages

We follow [Conventional Commits](https://www.conventionalcommits.org/) with
[Angular-style](https://github.com/angular/angular/blob/main/CONTRIBUTING.md#commit)
type and scope.

### 3.1. Format

```
<type>(<scope>): <short summary>

[optional body — explains WHY, not HOW]

[optional footer — e.g., "Closes #123", "BREAKING CHANGE: ..."]
```

### 3.2. Types

| Type       | Description                                          |
| ---------- | ---------------------------------------------------- |
| `feat`     | A new feature                                        |
| `fix`      | A bug fix                                            |
| `docs`     | Documentation-only changes                           |
| `refactor` | Code change that neither fixes a bug nor adds a feature |
| `test`     | Adding or updating tests                             |
| `ci`       | Changes to CI/CD configuration or workflows          |
| `chore`    | Maintenance tasks (dependencies, configs, tooling)   |
| `perf`     | Performance improvement                              |
| `build`    | Build system or external dependency changes          |
| `revert`   | Reverts a previous commit                            |

### 3.3. Scope

Scope identifies the affected module or service:

```
feat(terraform): add vault-agent module
ci(grafana): add terraform plan workflow
fix(elk): resolve ILM policy rollover threshold
docs(contributing): add Google3 conventions
chore(deps): bump proxmox provider to 0.94
```

For the `terraform` monorepo, use the service directory name as scope
(e.g., `traefik`, `grafana`, `elk`, `cloudflare`).

---

## 4. Branch Naming

Use a descriptive branch name with a type prefix matching the commit type:

```
feat/add-metrics-export
fix/resolve-login-timeout
ci/refactor-terraform-workflows
docs/update-contributing-guide
refactor/extract-config-renderer
```

---

## 5. Pull Requests

### 5.1. Requirements

- Fill out the **PR template** completely (What, Why, Testing, Checklist)
- **One logical change per PR** — avoid mixing features, fixes, and refactors
- **~200 lines of code** maximum (excluding generated files, lock files)
- All CI checks must pass before requesting review
- Link related issues using `Closes #123` or `Refs #123`

### 5.2. Review SLA

- **Reviewers should respond within 24 hours** (business days)
- If you haven't received a response, ping the reviewer
- Urgent fixes (security, outage) may bypass the SLA with reviewer agreement

### 5.3. Merge Policy

| Method         | Allowed | Notes                           |
| -------------- | ------- | ------------------------------- |
| Squash merge   | Yes     | Default — produces linear history |
| Rebase merge   | Yes     | When individual commits matter    |
| Merge commit   | **No**  | Disabled across all repositories  |

---

## 6. Code Review

Adapted from Google's
[three-bit approval model](https://google.github.io/eng-practices/review/):

### 6.1. Review Criteria

Every change must satisfy:

1. **Correctness** — Does the code do what it claims?
2. **Clarity** — Is the code easy to understand and maintain?
3. **Consistency** — Does it follow existing patterns in the codebase?
4. **Completeness** — Are tests, docs, and error handling included?

### 6.2. GitHub Adaptation

| Google Concept       | GitHub Implementation              |
| -------------------- | ---------------------------------- |
| LGTM (peer review)   | PR approval from a reviewer        |
| Owner approval        | CODEOWNERS required review         |
| Readability approval  | CI lint/format gates (automated)   |

### 6.3. Reviewer Guidelines

- **Review the CL, not the author.** Focus on code quality, not style preferences.
- **Be constructive.** Suggest alternatives, not just problems.
- **Distinguish blocking vs. non-blocking** feedback (prefix with `nit:` for
  non-blocking suggestions).
- **Approve when "good enough"** — don't block on perfection if the code is
  correct, clear, and tested.

---

## 7. Issues

### 7.1. Templates

Use the provided issue templates:

- **Bug Report** — for bugs and unexpected behavior
- **Feature Request** — for new features and improvements

### 7.2. Guidelines

- Search existing issues before creating a new one
- Provide as much context as possible (logs, screenshots, reproduction steps)
- Use labels appropriately (`type:bug`, `type:feature`, `priority:*`)
- Do **not** open issues for security vulnerabilities — see [SECURITY.md](SECURITY.md)

---

## 8. Code Quality

### 8.1. General Rules

- Follow the existing code style in the repository
- Run linters and formatters before committing
- All existing tests must pass
- New code should include tests where applicable

### 8.2. Anti-Patterns (Blocked)

| Anti-Pattern                  | Rule                              |
| ----------------------------- | --------------------------------- |
| `as any` / `@ts-ignore`      | Never suppress type errors        |
| Empty `catch {}` blocks       | Always handle or log errors       |
| Hardcoded IPs/secrets         | Use variables, Vault, or env vars |
| `print()` in production code  | Use proper logging frameworks     |
| Deleting failing tests        | Fix the code, not the tests       |

---

## 9. OWNERS and Governance

### 9.1. OWNERS Files

Each repository has an `OWNERS` file at its root (and optionally in
subdirectories) following the
[Kubernetes OWNERS format](https://www.kubernetes.dev/docs/guide/owners/).

```yaml
# OWNERS
approvers:
  - qws941
reviewers:
  - qws941
```

- **Approvers** accept changes holistically (design, architecture, intent)
- **Reviewers** focus on code quality (style, correctness, testing)
- OWNERS files are **hierarchical** — subdirectory OWNERS inherit from parent

### 9.2. CODEOWNERS

[CODEOWNERS](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-code-owners)
is the GitHub-native enforcement mechanism (separate from OWNERS):

```
# .github/CODEOWNERS
* @qws941
```

**OWNERS** documents governance intent. **CODEOWNERS** enforces review requirements.

---

## Questions?

Open a discussion or issue in the relevant repository.

# Security Policy

This policy applies to all repositories under
[qws941](https://github.com/qws941).

---

## Table of Contents

1. [Reporting a Vulnerability](#1-reporting-a-vulnerability)
2. [Response Timeline](#2-response-timeline)
3. [Scope](#3-scope)
4. [Safe Harbor](#4-safe-harbor)
5. [Supported Versions](#5-supported-versions)
6. [Security Best Practices](#6-security-best-practices)

---

## 1. Reporting a Vulnerability

> **Do NOT open a public GitHub issue for security vulnerabilities.**

### 1.1. How to Report

Email **security@jclee.me** with:

- A description of the vulnerability
- Steps to reproduce
- Affected repository and version/commit
- Potential impact assessment (severity estimate)
- Any suggested fix or mitigation

### 1.2. What to Expect

We take all security reports seriously and will respond promptly. See
[Section 2](#2-response-timeline) for our response commitments.

---

## 2. Response Timeline

| Stage                  | SLA                  |
| ---------------------- | -------------------- |
| Acknowledgment         | Within **48 hours**  |
| Initial assessment     | Within **5 business days** |
| Fix or mitigation plan | Within **10 business days** |
| Public disclosure      | After fix is deployed, coordinated with reporter |

If a vulnerability is actively exploited, we will expedite the response.

---

## 3. Scope

### 3.1. In Scope

- All public and private repositories under [qws941](https://github.com/qws941)
- Infrastructure managed by these repositories (Proxmox, Cloudflare, etc.)
- CI/CD workflows and automation
- Secrets management and access controls

### 3.2. Out of Scope

- Third-party services not maintained by this account
- Vulnerabilities in upstream dependencies (report to the upstream project)
- Social engineering attacks

---

## 4. Safe Harbor

We consider security research conducted in **good faith** to be authorized.
We will not pursue legal action against researchers who:

- Make a good faith effort to avoid privacy violations and service disruption
- Do not access or modify data belonging to other users
- Report vulnerabilities promptly and allow reasonable time for remediation
- Do not publicly disclose vulnerability details before a fix is available

We will credit researchers in the fix advisory (unless they prefer anonymity).

---

## 5. Supported Versions

Only the **latest version** (default branch: `master` or `main`) of each
repository is actively supported with security updates.

Archived repositories (`cloudflare`, `proxmox`) are not maintained — their
functionality has been migrated to the
[terraform](https://github.com/qws941/terraform) monorepo.

---

## 6. Security Best Practices

These practices are enforced across all repositories:

| Practice                           | Implementation                      |
| ---------------------------------- | ----------------------------------- |
| Dependency scanning                | Dependabot alerts + security fixes  |
| Secret scanning                    | GitHub secret scanning enabled      |
| Branch protection                  | CODEOWNERS required reviews          |
| Code review                        | CODEOWNERS required reviews         |
| No hardcoded secrets               | Vault, env vars, or `.env.example`  |
| Signed commits (terraform repo)    | Required signatures via branch rules |

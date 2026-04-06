# Versioning

This repository uses a practical SemVer-style scheme with `0.x` releases while the fork
is still evolving quickly at the operator/runtime boundary.

Current tracked version is stored in [`VERSION`](./VERSION).

## Rules

### Major

Bump the major version only after the project reaches `1.0.0`, and only for intentional
breaking changes in one of these operator-facing contracts:

- config format
- storage layout
- automation-visible `/status` contract
- pool routing semantics that require operator action to adapt

### Minor

Bump the minor version for intentional, user-visible or operator-visible changes:

- new pool capability
- new account type or routing mode
- new admin or dashboard workflow
- meaningful routing-policy changes
- new fallback behavior

### Patch

Bump the patch version for changes that should not alter the intended external contract:

- bug fixes
- internal hardening
- tests
- refactors
- logging and observability improvements

## Pre-release Labels

Use pre-release suffixes while work is still moving on a branch:

- `-dev` for active branch work
- `-rc.1`, `-rc.2`, ... for release candidates

Examples:

- `0.4.0`
- `0.5.0-dev`
- `0.5.0-rc.1`

Optional git metadata may be attached in release automation only, for example:

- `0.5.0-dev+f1fc044`

## Recommended Workflow

1. Keep `main` on the latest stable release number.
2. Move active feature branches to the next intended minor as `-dev`.
3. Cut release candidates only when smoke tests and operator checks are green.
4. Tag stable releases as `vX.Y.Z`.
5. Record user-visible changes in [`CHANGELOG.md`](./CHANGELOG.md) with every release.

## Current Version Line

- `0.1.0`: standalone operator-ready fork
- `0.2.0`: websocket auth and dead-seat handling
- `0.3.0`: tighter operator dashboard logic
- `0.4.0`: OpenAI API fallback pool
- `0.5.0`: request-planning refactor wave, GitLab Claude pool lane, dashboard-first operator landing, and GitLab health-truth hardening
- `0.5.1`: proxy response-handling seam cleanup across buffered, streamed, and websocket lanes with expanded regression coverage and text-first operator docs cleanup
- `0.6.0`: Gemini operator onboarding, quarantine visibility, persistent Codex usage restore/models cache, and live-proven fallback/cooldown routing hardening
- `0.6.1`: Gemini operator lane split, explicit source labels, and honest managed-OAuth degradation when local Gemini client credentials are absent
- `0.7.0`: browser-auth Gemini onboarding/import, pooled Gemini Code Assist facade, provider-truth routing for imported Gemini seats, and end-to-end Claude trace observability
- `0.8.0`: Gemini Browser Auth onboarding, OpenCode export/setup, reset tooling, and provider-scoped observability across Codex, Claude, and Gemini lanes
- `0.8.1`: landing-page Gemini per-model quota visibility and truthful quota normalization
- `0.8.2`: `/status` Gemini per-model quota visibility parity
- `0.8.3`: warmed fallback-project Gemini seats stay routable instead of being falsely hard-blocked
- `0.8.4`: model-specific Gemini cooldown export and operator smoke visibility
- `0.8.5`: proactive Gemini provider-truth refresh and truthful cooldown health projection
- `0.8.6`: Codex refresh-invalid health truth, deterministic sticky Codex seat selection, and canonical browser-auth Gemini/OpenCode operator cleanup
- `0.8.7`: source-level follow-up that restores the canonical `/operator/gemini/oauth-start` browser-auth route in the published tree
- `0.9.0`: screenshot-first operator-surface cleanup, diagnostics-only `/status`, broader exported Gemini model catalog, and Codex sticky/fallback follow-through for refreshable seats
- `0.10.0`: isolated GitLab Codex sidecar publish, shared GitLab Claude org-TPM cooldown handling, and safer Gemini/OpenCode defaults plus catalog truth follow-through
- `0.10.1`: sterile publish cleanup that removes repo-local governance/audit artifacts from `main` and aligns the public bundle export rules with the documented helper surface
- `0.10.2`: GitLab Claude shared-TPM cooldown scope fix so healthy sibling seats stay routable
- `0.10.3`: GitLab Claude recovery canaries, persisted local Codex stream cooldowns, and lower-inflight Codex seat reservation for fresh work

# Changelog

All notable fork-specific changes in this repository will be documented in this file.

This repository is a standalone extracted fork layered on top of `darvell/codex-pool`.
It does not preserve upstream git ancestry. The documented imported Go-core baseline is
`darvell/codex-pool@4570f6b`.

The format is loosely based on Keep a Changelog. Versioning rules are defined in
[`VERSIONING.md`](./VERSIONING.md).

## [0.6.0] - 2026-03-24

### Added
- Managed Gemini operator onboarding on both `/` and `/status`, including loopback OAuth start/callback handling, popup/manual-open recovery, and raw `oauth_creds.json` fallback paste flow.
- Explicit long-dead-seat quarantine handling and operator visibility for quarantined accounts across status JSON, status HTML, and the landing overview.
- Persistent Codex usage snapshots with restore-time reload support, plus a local cached `/backend-api/codex/models` path to reduce fragile upstream metadata round-trips.
- Additional focused regression coverage for Gemini persistence, Codex usage restore/rotation, quarantine visibility, and fallback/request-path behavior.

### Changed
- The local landing page now mirrors real operator truth from `/status?format=json` instead of acting like a separate setup-only surface. It exposes live `Codex`, `Claude`, and `Gemini` dashboards, cleanup status, operator actions, and deletion controls in one place.
- Codex routing now keeps one active local seat until threshold, restores usage state across restart more truthfully, honors local cooldown windows, and avoids retry-path active-seat poisoning.
- Codex fallback operation is now explicitly operator-visible: fallback API keys are health-probed, displayed separately from local seats, and proven to take live traffic when local Codex seats are temporarily unavailable.
- Managed Gemini and GitLab Claude state handling now round-trips more of the operator-visible runtime fields across save/load/reload boundaries.

### Fixed
- Codex seats no longer forget recent quota state on restart and immediately drift back onto already-burned accounts because of stale or missing usage snapshots.
- Local Codex seats that hit live cooldown now leave fresh rotation instead of remaining eligible behind a debug-only bypass.
- The active Codex lease is no longer rewritten by retry-only fallthrough candidates that never actually won a successful request.
- The landing/dashboard surfaces no longer hide quarantine and dead-seat cleanup truth behind `/status` only.
- Managed Gemini OAuth client credentials are no longer hardcoded in the repository; the operator flow now expects them from the local service environment.

## [0.5.1] - 2026-03-23

### Changed
- Refactored buffered, streamed, and websocket proxy response handling into smaller explicit seams so retryable status inspection, copied-response delivery, websocket success recovery, and pooled websocket proxy execution are no longer mixed into large inline handler blocks.
- Shared pre-copy status inspection and replay handling between streamed and websocket lanes while keeping their remaining transport-specific differences explicit.
- Hydrated the next websocket execution-shell follow-up (`T31`) in repo-local SSOT so the ongoing refactor wave is traceable from plan to evidence.
- Replaced screenshot-heavy README sections with text-first operator documentation aligned with the current dashboard-first local UI.

### Added
- Focused buffered regression coverage for managed API and GitLab Claude retry/failover paths.
- Shared proxy account snapshot helpers for buffered, streamed, and websocket response-path tests.
- Additional websocket finalizer coverage for non-`101` successful recovery and failed-handshake no-op behavior.

## [0.5.0] - 2026-03-23

### Added
- GitLab-backed Claude pooling with managed Duo direct-access token minting.
- Operator-facing GitLab Claude token onboarding and pool visibility in `/status`.
- Dashboard-first local landing with live `Codex`, `Claude`, and `Gemini` views powered by `/status?format=json`.
- Additional operator controls for fallback API keys, GitLab Claude tokens, and manual account deletion on the local dashboard surfaces.
- GitLab-specific status/admin visibility for cooldowns, quota backoff counters, and direct-access rate-limit signals.
- Repo-local engineering governance files: `ACTION_PLAN.md`, `DEBUG.md`,
  `EVIDENCE_LOG.md`, and `PROJECT_MANIFEST.md`.

### Changed
- Extracted proxy admission logic out of the main request handler.
- Introduced explicit request-planning contracts for route selection.
- Enforced Codex seat cutoff at `>= 90%` usage and added sticky seat reuse.
- Unified usage ingestion across body, headers, and stream paths.
- Extracted shared response stream usage recording helpers.
- Reused shared retry/error/finalization handling across buffered, streamed, and websocket proxy paths.
- Replaced the old setup-first local landing with a provider-dashboard-first operator surface and removed the decorative hero treatment.
- Hardened managed GitLab Claude persistence into one canonical fail-closed serializer and shortened status/admin lock scope with snapshot-based rendering.

### Fixed
- Ordinary non-stream Claude `/v1/messages` responses now contribute to local usage totals.
- Streamed and websocket managed-upstream inspection now preserves client-visible error bodies while still classifying retryable failures.
- GitLab Claude gateway `402/401/403` handling now rotates correctly, persists cooldown state, and avoids falsely killing healthy source tokens.
- Malformed successful GitLab direct-access refresh responses now become explicit `error` state and clear stale gateway auth material instead of remaining deceptively healthy.

## [0.4.0] - 2026-03-22

### Added
- OpenAI API fallback pool support for Codex execution.
- Managed API key health probing and status visibility.
- Operator UI flows for adding and deleting OpenAI API keys.
- Routing support for fallback-only managed API accounts.

### Changed
- Codex routing can now fall through to the API key pool when subscription seats are not usable.
- `/status` gained operator-facing API pool visibility and controls.

## [0.3.0] - 2026-03-21

### Changed
- Tightened `/status` dashboard wording and operator logic.
- Improved operator-facing auth and refresh timestamps.
- Reduced noisy raw/internal links on the local operator page.

## [0.2.0] - 2026-03-21

### Added
- Codex websocket authentication handling for pooled seats.
- Dead-seat detection and automatic failover for deactivated Codex accounts.

### Changed
- Hardened Codex websocket request handling and recovery behavior.

## [0.1.0] - 2026-03-19

### Added
- Standalone operator-ready fork packaging around the upstream proxy core.
- `orchestrator/codex_pool_manager.py`.
- `systemd/codex-pool.service`.
- Local install and security documentation.
- Operator-oriented landing and status flows for local deployment.

## Upstream Divergence Notes

- Imported upstream baseline: `darvell/codex-pool@4570f6b`
- Current upstream head at comparison time: `darvell/codex-pool@cf782a7`
- This fork is intentionally more operator-centric and Codex-centric than upstream.
- Upstream may contain newer generic provider features that are not mirrored here.

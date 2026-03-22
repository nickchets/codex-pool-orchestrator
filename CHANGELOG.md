# Changelog

All notable fork-specific changes in this repository will be documented in this file.

This repository is a standalone extracted fork layered on top of `darvell/codex-pool`.
It does not preserve upstream git ancestry. The documented imported Go-core baseline is
`darvell/codex-pool@4570f6b`.

The format is loosely based on Keep a Changelog. Versioning rules are defined in
[`VERSIONING.md`](./VERSIONING.md).

## [Unreleased] - 0.5.0-dev

### Changed
- Extracted proxy admission logic out of the main request handler.
- Introduced explicit request-planning contracts for route selection.
- Enforced Codex seat cutoff at `>= 90%` usage and added sticky seat reuse.
- Unified usage ingestion across body, headers, and stream paths.
- Extracted shared response stream usage recording helpers.

### Added
- Admission, request-planning, and usage-ingestion regression tests for the refactor path.
- Repo-local engineering governance files: `ACTION_PLAN.md`, `DEBUG.md`,
  `EVIDENCE_LOG.md`, and `PROJECT_MANIFEST.md`.

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

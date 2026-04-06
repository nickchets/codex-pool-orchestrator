# Changelog

All notable fork-specific changes in this repository will be documented in this file.

This repository is a standalone extracted fork layered on top of `darvell/codex-pool`.
It does not preserve upstream git ancestry. The documented imported Go-core baseline is
`darvell/codex-pool@4570f6b`.

The format is loosely based on Keep a Changelog. Versioning rules are defined in
[`VERSIONING.md`](./VERSIONING.md).

## [0.10.4] - 2026-04-06

### Changed
- Public bundle export now uses a deterministic tar-based copy path instead of the previous `rsync` flow that could hang in uninterruptible IO wait on this workstation during local release validation.
- Dashboard assembly is split into smaller helper stages for current-seat selection, Gemini/GitLab enrichment, workspace grouping, provider summaries, and fallback pool bookkeeping without changing the `/status` JSON or HTML contract.

### Fixed
- Buffered retry handling now reuses one shared bookkeeping helper, reducing branch drift in prestream retry paths.
- GitLab Claude shared-cooldown reads now use a shared snapshot helper so recovery polling, cooldown gating, and status paths stop duplicating lock/read/filter logic.

### Internal
- Kimi and MiniMax now share one API-key provider base, and repeated setup/opencode response-body helpers were collapsed into common utilities.
- Persisted account health field writes now flow through one shared helper instead of being re-serialized in multiple save paths.

## [0.10.3] - 2026-04-06

### Added
- GitLab Claude shared-TPM recovery now keeps a per-scope canary schedule and surfaces the last canary result directly in dashboard and account-status output.
- Runtime metrics now track named retry and recovery events plus provider TTFB buckets so operator troubleshooting can separate prestream retry churn from downstream latency.

### Changed
- Fresh Codex seat selection now reserves the chosen seat before caller work starts and prefers lower-inflight seats for new work, reducing duplicate concurrent picks on the same seat.
- GitLab Claude dashboard health rendering now clears stale shared-cooldown noise once a seat becomes eligible again while preserving visible recovery-canary state.

### Fixed
- Local Codex streamed usage-limit failures now become persisted cooldown state instead of leaving the seat falsely healthy after the SSE failure event.
- Managed OpenAI API usage-limit error text is now classified as a retryable rate-limit condition instead of collapsing into a dead-key style quota failure.

## [0.10.1] - 2026-03-30

### Changed
- The published repository tree is now steriler: repo-local governance, handoff, audit, and closure-spec documents are no longer shipped in `main`.
- Public bundle export rules now match the published tree and keep the documented `orchestrator/codex_pool_manager.py` helper available instead of treating it as private packaging residue.

## [0.10.2] - 2026-03-30

### Fixed
- GitLab Claude shared org-TPM cooldown propagation now scopes by live entitlement headers instead of collapsing every `gitlab.com` Claude Duo seat into one synthetic cooldown bucket.
- Pool-side GitLab Claude routing no longer falsely marks healthy sibling seats unavailable just because another seat in a different entitlement group hit the shared TPM limiter.

### Verified
- Live per-seat probes now separate one genuinely dead `claude_gitlab` seat (`402 insufficient_credits`) from the remaining healthy seats instead of flattening the whole provider lane into `rate_limited`.

## [0.10.0] - 2026-03-30

### Added
- An optional dedicated GitLab Codex sidecar lane for Codex CLI, including `PROXY_FORCE_CODEX_REQUIRED_PLAN=gitlab_duo`, discovery-backed GitLab Codex model catalogs, `systemd/codex-pool-gitlab.service`, and an isolated `clcode` setup/bootstrap flow that does not mutate the main `~/.codex`.
- Pool-user onboarding now exposes a dedicated `clcode_setup` URL, and `orchestrator/codex_pool_manager.py` can bootstrap that isolated lane directly.

### Changed
- OpenCode export now defaults to `codex-pool/gemini-3.1-flash-lite` for a safer pooled Gemini baseline while still surfacing `gemini-3.1-pro-high`, `gemini-3.1-pro-low`, and the broader live Gemini model catalog.
- The dedicated GitLab Codex sidecar now serves Codex CLI auxiliary plugin/connectors/WHAM endpoints locally instead of leaking those requests to upstream GitLab endpoints.

### Fixed
- GitLab Claude organization-level TPM `429` responses now become a scoped shared cooldown with honest `Retry-After` behavior instead of fanning out across sibling GitLab Claude seats and collapsing into `503 no live claude accounts`.
- GitLab Codex `402 USAGE_QUOTA_EXCEEDED` and gateway `403` failures are now classified as cooldown states; when every eligible GitLab Codex seat is cooling down, the sidecar returns a local `429` rather than noisy `502`/`503` churn.
- `clcode` model-catalog refresh now reads nested `tokens.access_token` from `auth.json`, and `/v1/models` now resolves through the same cached GitLab Codex catalog path as `/backend-api/codex/models`.
- Antigravity Gemini fallback-project truth now persists more accurately for restricted or projectless seats, and OpenCode bundle metadata keeps canonical names for known Gemini models.

## [0.9.0] - 2026-03-29

### Changed
- The local operator contract now treats `/` as the canonical onboarding/dashboard surface, while `/status` is explicitly the read-only diagnostics page backed by the same `/status?format=json` truth.
- The landing page now keeps long Codex seat identities readable, exposes a dedicated `Quota Snapshot` view with freshness and reset timing, and keeps OpenCode on `codex-pool/gemini-3.1-pro-high` while exporting the broader live Gemini model catalog.

### Fixed
- Refreshable expired Codex seats no longer fall out of sticky reuse or best-seat fallback solely because the current access token is expired; fallback now preserves the highest eligible tier instead of draining lower-headroom seats first.
- The screenshot-first status UI audit now points at permanent `screenshots/status-ui-audit-20260329/` artifacts rather than temporary `.tmp` leftovers.

## [0.8.7] - 2026-03-28

### Fixed
- The canonical `/operator/gemini/oauth-start` route now dispatches to the browser-auth Gemini onboarding handler instead of the retired legacy Gemini OAuth path, so the published source matches the already-verified operator UI and running binary.
- Public bundle export now excludes leftover closure-spec artifacts alongside the other planning residue, keeping the extracted publish surface cleaner.

## [0.8.6] - 2026-03-28

### Changed
- Codex seat selection now uses stable score-first ordering for equally eligible subscription seats, so cold-start round-robin offset no longer burns a different seat when the current best seat is still within the intended sticky/headroom policy.
- Operator-facing Gemini/OpenCode surfaces now consistently describe the canonical browser-auth Gemini lane and export `codex-pool/gemini-3.1-pro-high` through `pool-gemini-accounts.json`, while the legacy Gemini auth path remains only as a compatibility alias.

### Fixed
- Codex OAuth seats are no longer marked dead blindly on `invalid_grant` or `refresh_token_reused`; the pool now probes current `/backend-api/codex/models` access first and persists `health_status=refresh_invalid` for still-live seats.
- Codex OAuth health/runtime state now survives save, reload, force-refresh, and status rendering more truthfully, including persisted `last_used`, `last_healthy_at`, and operator-visible health lines.
- The canonical `POST /operator/gemini/oauth-start` route now actually launches the browser-auth Gemini flow promised by the operator UI instead of falling through to the older managed-OAuth handler.

## [0.8.5] - 2026-03-28

### Changed
- Browser-auth Gemini truth refresh now becomes proactive one poll interval before `fresh_until`, so ready seats no longer age out into `stale_provider_truth` between scheduled refresh ticks.
- `/status`, `/status?format=json`, and related status-style JSON surfaces now promote Gemini cooldown seats to top-level `health_status="cooldown"` instead of leaving them mislabeled as generic `healthy`.

### Fixed
- Warmed browser-auth Gemini seats that still converge to `provider_truth.state=missing_project_id` now stay export-truthful across status and OpenCode quota rows: they remain `degraded_enabled` with fallback-project reason while their Gemini quota models stay `routable=true`.
- Operator-facing Gemini status no longer contradicts runtime truth by showing `health_status=healthy` for seats that are actually `operational_truth.state=cooldown` and `routing.state=degraded_enabled`.

## [0.8.4] - 2026-03-28

### Changed
- `/status?format=json` now exports `provider_truth.rate_limit_reset_times` for Gemini seats, and the merged quota rows surface live per-model cooldown reset times directly from runtime state.
- Gemini operator `seat-smoke` now reports `requested_model_key`, `requested_model_limited`, `requested_model_recovery_at`, and the live `rate_limit_reset_times` map, so model aliasing and requested-model cooldowns are explicit in one response.

### Fixed
- Gemini `429 RESOURCE_EXHAUSTED` on one routed model no longer poisons the whole seat with seat-wide cooldown state when the seat is still usable for other Gemini models.
- OpenCode export now keeps such seats enabled and carries model-specific reset windows instead of disabling the entire seat on a single-model cooldown.

## [0.8.3] - 2026-03-27

### Changed
- Warmed browser-auth Gemini seats that still report `provider_truth_state=missing_project_id` now stay in `degraded_enabled` when the fallback Code Assist project is actually usable, instead of being hard-blocked despite successful operational proof.

### Fixed
- Routing truth, `/status`, and downstream exports no longer contradict live Gemini seat smoke for fallback-project seats that can answer requests even without a persisted provider project id.

## [0.8.2] - 2026-03-27

### Changed
- The `/status` HTML dashboard now renders the same Gemini per-model quota rows as the local landing page, including reset time, key model limits/capabilities, provider tags, and explicit `routable` / `seat-blocked` / `catalog-only` state labels for each model.

### Fixed
- Gemini quota visibility on `/status` no longer stops at summary text, which previously hid whether a seat's quota catalog was actually routable, blocked by seat state, or catalog-only.

## [0.8.1] - 2026-03-27

### Changed
- The local landing page now expands Gemini quota visibility from summary-only counters to per-model limit rows with reset time, routable/catalog-only state, protected flags, and key model capabilities.

### Fixed
- Browser-auth Gemini quota normalization now treats the outer `fetchAvailableModels` map key as canonical model identity, so placeholder inner `model` fields no longer collapse live quota snapshots into `0 models captured`.
- Gemini truth-refresh logs now report the real hydrated quota model count instead of the misleading top-level quota key count.

## [0.8.0] - 2026-03-27

### Added
- Browser-first Gemini onboarding via Gemini Browser Auth on `/` and `/status`, including provider-truth persistence for project identity, subscription tier, protected models, typed quota snapshots, and warm-seat state.
- OpenCode export and guided setup surfaces (`/config/opencode/<token>` and `/setup/opencode/<token>`) plus Anthropic-compatible Gemini adapters for `/v1/messages` and `/v1/chat/completions`.
- Loopback-only Gemini operator diagnostics and reset tooling: seat smoke, `reset-bundle`, `reset-delete`, and `reset-rollback` with manifest snapshots and rollback artifacts.
- Provider-scoped request tracing for OAuth exchange, token refresh, health probe, facade routing, and metadata-cache events across Codex, Claude, and Gemini lanes.

### Changed
- Gemini routing is now sticky-until-pressure and gates on provider truth, warm-seat state, quota pressure, project availability, and observed operational failure before rotating to another seat.
- `/status`, `/status?format=json`, the landing page, direct Gemini API-key setup, and OpenCode export now project the same `provider_truth`, `operational_truth`, `routing.state`, `gemini_pool`, `provider_quota_summary`, and compatibility-lane contract.
- Direct Gemini API-key setup now keeps clients on the pool root URL in external API-key mode, while legacy local/manual Gemini import is retired from the operator surface in favor of Gemini Browser Auth.
- Codex route readiness, models-cache fetches, OAuth exchange, API-key probes, and refresh flows now emit trace data and preserve the exact `>= 90%` quota cutoff with sticky seat reuse.

### Fixed
- Restarted Gemini seats now refresh stale provider truth and empty-quota snapshots more truthfully instead of collapsing eligible seats into stale-routing dead ends.
- OpenCode exports no longer let blocked `missing_project_id` seats steal `activeIndex`; disabled seats stay visible, but they do not become the active account.
- Gemini reset rollback now validates operator-managed paths before delete/restore, closing the path-traversal gap in the reset tooling.
- Restricted browser-auth Gemini seats can now be diagnosed and, when appropriate, exercised through the fallback project without flattening provider restrictions into generic operational failure.

## [0.7.0] - 2026-03-25

### Added
- Gemini Browser Auth onboarding on `/` and `/status`, including browser OAuth start/callback, browser-auth account JSON import normalization, Code Assist project bootstrap, and persistence of provider-truth fields needed for routing.
- A pooled Gemini `/v1beta/models/*:generateContent|streamGenerateContent` facade that rewrites supported requests into the Code Assist `v1internal` lane for imported browser-auth Gemini seats.
- Claude request-trace correlation from wrapper to pool, including per-request trace headers, SSE/usage event counters, `chunk_gap` detection, and explicit idle-timeout diagnostics.
- Focused regression coverage for Gemini provider persistence, Gemini Browser Auth onboarding, dashboard/operator JSON truth, setup scripts, facade transforms, and request-trace behavior.

### Changed
- Direct Gemini API-key client setup scripts now keep the client in external API key mode with `GEMINI_API_KEY` and `GOOGLE_GEMINI_BASE_URL` instead of the earlier OAuth-bypass environment shape.
- Gemini seat persistence and routing now keep operator/source provenance plus provider block-state fields such as `proxy_disabled`, `validation_blocked`, quota-forbidden status, subscription tier, and validation metadata.
- `/status`, `/status?format=json`, and the landing page now describe Gemini seats with browser-auth provenance and provider-truth fields instead of treating all non-managed seats as one generic manual-import lane.
- Explicit force-refresh paths now bypass the Gemini per-account refresh throttle when the operator asks for a real refresh.

### Fixed
- Gemini `v1beta` requests no longer route onto imported seats that lack the provider project id required for the Code Assist facade.
- Idle SSE timeout detection now records real timeout state instead of collapsing into a generic downstream `context canceled`.
- Local Claude tracing can now be correlated end-to-end without leaking wrapper-only headers upstream.

## [0.6.1] - 2026-03-24

### Changed
- Gemini operator surfaces now split managed OAuth onboarding from manual `oauth_creds.json` import instead of presenting them as one mixed flow.
- `/status?format=json` now exposes explicit Gemini operator truth via `gemini_operator` and per-account `operator_source` labels so the landing page and `/status` can describe the same pool composition.
- When the local service has no configured Gemini OAuth client, the managed Gemini CTA now degrades honestly into an unavailable note instead of looking like another working onboarding path.

### Fixed
- Manual Gemini `oauth_creds.json` import is no longer mislabeled as a fallback/API pool. Imported credentials are shown as regular Gemini seats with explicit source labels.

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
- Documented the next websocket execution-shell follow-up (`T31`) so the ongoing refactor wave remains traceable end-to-end.
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
- Additional operator controls for fallback API keys, GitLab Claude tokens, and manual account deletion on the dashboard surfaces.
- GitLab-specific status/admin visibility for cooldowns, quota backoff counters, and direct-access rate-limit signals.

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
- Standalone deployment assets around the upstream proxy core.
- `systemd/codex-pool.service`.
- Install and security documentation.
- Operator-oriented landing and status flows for self-hosted deployment.

## Upstream Divergence Notes

- Imported upstream baseline: `darvell/codex-pool@4570f6b`
- Current upstream head at comparison time: `darvell/codex-pool@cf782a7`
- This fork is intentionally more operator-centric and Codex-centric than upstream.
- Upstream may contain newer generic provider features that are not mirrored here.

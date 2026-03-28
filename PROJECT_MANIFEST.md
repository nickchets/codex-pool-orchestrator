# PROJECT_MANIFEST — codex-pool-orchestrator

## Objective
Run `codex-pool-orchestrator` as a repo-local product capsule that provides a multi-account agent proxy, operator dashboard, and reusable packaging/runtime surfaces without pushing product behavior back into the root control plane.

## Definition Of Done
- `go build ./...` passes from the repo root.
- The targeted smoke suite used by the root proof remains green.
- Repo-local execution truth lives here, not in `/home/lap`.
- Operator-facing entrypoints, packaging files, and status surfaces stay documented and reproducible.
- Evidence for repo-local changes is recorded in [`EVIDENCE_LOG.md`](/home/lap/projects/codex-pool-orchestrator/EVIDENCE_LOG.md).

## Scope
- Proxy/runtime behavior in Go sources at the repo root.
- Operator tooling under `orchestrator/`, `systemd/`, and `docs/`.
- Repo-local board/evidence for proof and future routed implementation.

## Authority Boundary
- Root control plane in `/home/lap` owns directive intake, routing, active-repo selection, and cross-repo governance.
- This repo owns product behavior, repo-local verification, and repo-local operational docs.
- Root may audit this repo through declared SSOT and capsule artifacts, but it does not silently rewrite product truth.

## Repo-Local SSOT
- `PROJECT_MANIFEST.md`
- `ACTION_PLAN.md`
- `EVIDENCE_LOG.md`
- `DEBUG.md`
- `README.md`

## Canonical Verification
- `go build ./...`
- `go test ./...`
- `systemctl --user is-active codex-pool.service`
- `curl -fsS http://127.0.0.1:8989/healthz`
- `curl -fsS http://127.0.0.1:8989/status?format=json`

## Current Routing Note
- This repo was selected by root card `ROOT-E22-S1-T2` as the first external proof capsule.
- The first live proof was executed under root card `ROOT-E22-S1-T3`.
- The first governed refactor wave on 2026-03-22 restored a full green Go test baseline and added live `/responses` smoke verification on the deployed user service.
- The second governed refactor wave on 2026-03-22 froze request-planning contracts into a dedicated layer so buffered, streamed, and websocket request entrypaths reuse one routing plan.
- The next governed bugfix slice on 2026-03-22 hardened Codex seat selection so exact `90%` quota thresholds now block fresh routing, streamed requests retain session affinity when possible, and `/status` previews match the real sticky selector.
- The next governed refactor wave on 2026-03-22 unified usage ingestion under a shared `UsageDelta` contract so provider parsers and non-SSE body fallback no longer maintain separate token/rate-limit parsing branches.
- The following refactor slice on 2026-03-22 collapsed duplicated SSE usage interception between buffered and streamed proxy paths without changing retry or routing behavior.
- The current Claude/GitLab product track is repo-local: GitLab-backed Claude pooling, quota/fallback truth, and operator-facing Claude dashboard behavior belong in this capsule rather than in root control-plane docs.
- The current operator-surface track is to make `/` a dashboard-first local operator page that reuses the same live `/status?format=json` contract instead of drifting into a separate setup-only landing UI.
- The current Gemini sequencing is explicit after the Gemini/OpenCode audit: env-backed managed OAuth/runtime truth comes first, then provider-truth persistence and warm-seat admission, then Gemini routing/cooldown logic, and only after that broader UI parity on `/status` or `/`.
- The Gemini runtime compatibility slice on 2026-03-24 repaired legacy managed-seat recovery after OAuth client externalization: managed seats now prefer env-backed default profiles before stale embedded clients, and legacy managed seats carrying operator identity can regain truthful managed labeling without reopening quota/routing scope.
- The legacy external-manager transfer is pattern-level only: sticky current-seat preference, provider-truth quota state, warm-seat admission, and core backoff may be imported, but manager file indices, sqlite bookkeeping, log-scrape guards, and hidden fixed-account behavior must not become a second control plane inside this repo.
- Gemini routing correctness after the audit has a higher bar than generic account scoring: provider-aware model/quota pressure must drive fresh admission and rotation, while `/status` and `/` remain projections of core runtime truth instead of inventing parallel semantics.
- The follow-on route refreshed on 2026-03-25 is now explicit as well: after `T46/T47`, freeze the quota-first Gemini contract and shim semantics (`T49`), align OpenCode Gemini connectivity and exported limits truth (`T50`), then land operator/dashboard parity (`T44`), and only after that run any controlled live seat delete/reimport proof (`T51`).
- The last pre-`T47` Gemini boundary closed on 2026-03-26 without widening selector scope: ready provider truth now carries additive freshness semantics (`freshness_state`, `stale`, `stale_reason`, `fresh_until`) in `/status?format=json`, but stale truth does not become routing-blocking until `T47`.
- The Gemini browser-auth validation audit on 2026-03-27 tightened the Gemini truth contract further: `validation_blocked` can no longer stand in for both provider restriction and live operational failure. The current implementation wave must keep `provider_truth`, observed `operational_truth`, and `routing.state` explicit and additive before any seat-reset or UI-polish work.
- The follow-on live rollout on 2026-03-27 closed that state-split wave on the running service: allowlisted Gemini seats now reload as `health_status=restricted` instead of stale `validation_blocked`, while operator smoke persists `operational_truth=degraded_ok` separately and can truthfully downgrade a seat into `missing_project_id` without pretending it is dead.
- The next live boundary closed on 2026-03-27 as well: `T47` now blocks stale provider truth / stale quota snapshots, `missing_project_id`, not-warmed restricted seats, quota pressure, and hard operational failure before Gemini selection, while startup stale-truth refresh re-hydrates ready browser-auth Gemini seats so the new gate does not collapse the pool after restart.
- The client/dashboard parity wave was live-closed on 2026-03-27 as well: OpenCode exports `/v1` plus `pool-gemini-accounts.json` with an enabled `activeIndex`, and the landing/status surfaces consume the same `gemini_pool` / `provider_quota_summary` / `compatibility_lane` truth on the restarted service.
- The current release policy was refreshed again on 2026-03-27 after the post-restart proofs and Opus/Gemini audit: do not cut a narrow Gemini publish, but also do not keep a forced destructive browser-auth re-add drill as a fake release gate. `T51` is now release-closed as safe operator reset tooling plus live rollback proof, while the next organic browser-auth add remains the fresh-import-after-all-fixes follow-up proof.
- The final 2026-03-27 publish boundary is intentionally broader than a Gemini-only tag: the same `0.8.0` operator-facing release now bundles the already verified Codex/Gemini observability and status/probe hardening slices together with the Gemini/OpenCode closure chain, while blocked GitLab Claude token recovery stays outside the release scope.
- The post-release closure chain on 2026-03-27 is now explicit and mostly complete: `T55` re-proved a fresh browser-auth seat on the published binary, `T58` repaired the false-negative operator `gemini-3.1-pro` seat-smoke path, and `T56` made OpenCode, including the local `agcode` wrapper, the canonical no-prompt local entrypoint with exported `model=codex-pool/gemini-3.1-pro-high` plus a re-proved ready-seat `activeIndex` on the live closure snapshot.
- The repo-local Gemini closure chain is now complete through `0.8.6`: `T57` closed the last bounded repository slice around cooldown/stale-quota/model-specific rate-limit taxonomy, precise provider-derived reset windows, and richer operator/export diagnostics, while the follow-on publish wave also cleaned the canonical browser-auth/OpenCode operator contract and Codex refresh-invalid health truth. Any further follow-up is narrower operational/provider truth or a fresh repo-local card, not a still-open continuation of `T57`.
- The current out-of-band product incident is separate from the Gemini track: live GitLab Claude pooling is under repo-local incident card `REPO-CPO-BUG-P1-T52` for `503 no live claude accounts`, and that runtime diagnosis must not be folded back into Gemini closure evidence.
- Root card `ROOT-E30-S1-T5` now anchors the remaining post-release Gemini limits/accounts/observability complaint on the control-plane side: if that follow-up reopens into product work after `0.8.6`, claim a fresh repo-local Gemini operator card from this capsule's board/evidence and use this manifest note as the handoff anchor instead of routing the complaint back through `ROOT-AUTO-INTAKE-T1`, reopening finished root-core cards, or silently reviving already-closed repo-local Gemini closure cards such as `REPO-CPO-ARCH-P2-T57`.

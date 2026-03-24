# ACTION_PLAN — codex-pool-orchestrator

> Repo-local board for governed product work in this capsule.

## Board

### DOING

#### REPO-CPO-ALIGN-P1-T43: Separate Gemini onboarding lanes from provider-specific automation
1. Split the current Gemini operator contract into explicit lanes so `/` and `/status` stop mixing managed Gemini OAuth seat onboarding with provider-specific automation/import behavior.
2. Remove borrowed antigravity semantics from the default Gemini dashboard flow and replace them with first-party pool concepts: managed Gemini seats, optional generic API pool, and any future automation as a separate operator lane.
3. Keep the slice bounded to operator/UI/router behavior first, then re-verify that the resulting Gemini dashboard still reflects truthful pool state instead of button-driven side effects.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -run 'TestLocalOperatorGeminiSeatAddStoresManagedSeat|TestLocalOperatorGeminiOAuthStartAllowsLoopbackWithoutAdminHeader|TestManagedGeminiOAuthCallbackStoresManagedSeat|TestServeStatusPageIncludesOperatorActionForLocalLoopback|TestServeStatusPageHidesOperatorActionOutsideLoopback|TestServeFriendLanding_LocalTemplateIncludesCodexOAuthAction|TestBuildPoolDashboardData.*' ./...`

### NEXT

#### REPO-CPO-ALIGN-P1-T44: Reconcile Gemini pool truth after the operator split
1. Make the Gemini dashboard summarize the actual pool composition after the operator split: managed seats, generic API-style fallback lanes if enabled, and automation/import lanes if configured.
2. Ensure account rows, counters, and operator actions use the same source-of-truth field names across `/status?format=json`, `/status`, and the landing page.
3. Lock the slice with targeted dashboard/status regressions and one live smoke against the running service once the UI split lands.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -run 'TestServeStatusPageReturnsJSONForFormatQuery|TestServeStatusPage|TestServeFriendLanding_LocalTemplateIncludesCodexOAuthAction|TestBuildPoolDashboardData.*|TestGeminiProviderLoadAccountLoadsPersistedState' ./... && curl -fsS http://127.0.0.1:8989/status?format=json | jq '{gemini_pool:.gemini_pool,accounts:[.accounts[]|select(.type=="gemini")|{id,eligible:(.routing.eligible//false),block_reason:(.routing.block_reason//null),managed:(.managed//false),health_status:(.health_status//null)}]}'`

### BLOCKED

_(none)_

### DONE

#### REPO-CPO-VERIFY-P1-T41: Controlled live threshold and API-fallback cutover proof
1. Temporarily exclude every currently eligible local Codex seat from the live pool, then verify the API fallback lane becomes the only remaining eligible Codex path.
2. Send one short pooled Codex request while the local seats are excluded and confirm the request still completes successfully through the fallback lane.
3. Restore every touched seat file and reload the pool back into normal operation, with before/after operator evidence proving both the cutover and the recovery.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_t41_before.json && AUTH=$(jq -r '.tokens.access_token' /home/lap/.codex/auth.json) && timeout 60s curl -sS -N -o /tmp/cpo_t41_responses.sse -w '%{http_code}' http://127.0.0.1:8989/v1/responses -H "Authorization: Bearer $AUTH" -H 'Content-Type: application/json' --data '{"model":"gpt-5.4","instructions":"Reply with exactly OK.","input":[{"role":"user","content":[{"type":"input_text","text":"Reply with exactly OK."}]}],"store":false,"stream":true}' && curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_t41_after.json`

#### REPO-CPO-BUG-P1-T42: Honor local Codex cooldowns and stop retry-path active-seat poisoning
1. Make local Codex seat cooldowns (`RateLimitUntil`) actually block fresh routing instead of only incrementing penalty while the selector still treats the seat as eligible.
2. Keep the active Codex lease stable across retry fallthrough: first-attempt selection may establish stickiness, but retry candidates must not overwrite the active seat pointer for future traffic.
3. Lock the slice with focused selector regressions, rebuild the binary, and restart the local user service on the updated selector logic.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -run 'TestRoutingStateBlocksRateLimitedLocalCodexSeat|TestCandidateSkipsRateLimitedLocalCodexSeat|TestCandidateRetryPathDoesNotMoveActiveCodexSeat|TestRoutingStateBlocksRateLimitedManagedOpenAIAPIKey|TestCandidateDropsActiveCodexSeatAtExactPrimaryThreshold|TestCandidateDropsActiveCodexSeatAtExactSecondaryThreshold' ./... && go build ./... && go build -o /home/lap/.local/bin/codex-pool . && systemctl --user restart codex-pool.service && curl -fsS http://127.0.0.1:8989/healthz`

#### REPO-CPO-VERIFY-P1-T40: Live-smoke Codex seat stickiness on the running pool
1. Capture a before/after live `/status?format=json` snapshot around a minimal pooled Codex request so the running service proves the active seat remains sticky instead of distributing traffic round-robin.
2. Confirm the request completes through the local Codex seat lane without unexpectedly cutting over to the API fallback path on a healthy local-seat run.
3. Keep the slice observational and cheap: one short pooled request plus the minimum JSON/SSE artifacts needed to prove live selector behavior after `T39`.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_t40_before.json && AUTH=$(jq -r '.tokens.access_token' /home/lap/.codex/auth.json) && timeout 60s curl -sS -N -o /tmp/cpo_t40_responses.sse -w '%{http_code}' http://127.0.0.1:8989/v1/responses -H "Authorization: Bearer $AUTH" -H 'Content-Type: application/json' --data '{"model":"gpt-5.4","instructions":"Reply with exactly OK.","input":[{"role":"user","content":[{"type":"input_text","text":"Reply with exactly OK."}]}],"store":false,"stream":true}' && curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_t40_after.json`

#### REPO-CPO-BUG-P1-T39: Fix Codex quota freshness across reset rollover and restart
1. Stop carrying expired Codex reset timestamps forward when fresh `token_count` usage arrives without reset metadata, so post-reset burn cannot masquerade as `0%` until a later WHAM/header refresh.
2. Let restore-time `Totals` repair a stale persisted usage snapshot when the totals are newer, instead of trusting an older snapshot just because `Usage.RetrievedAt` is non-zero.
3. Lock the weekly-routing edge explicitly: active-seat reuse and most-recently-used fallback must both drop at the exact secondary threshold, and re-entry must resume cleanly after a fresh reset.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -run 'TestApplyUsageSnapshotDoesNotCarryExpiredResetAcrossTokenCount|TestRestorePersistedUsageStatePrefersNewerTotalsWhenSnapshotStale|TestCandidateStopsReusingMostRecentlyUsedSeatAtExactSecondaryThreshold|TestCandidateDropsActiveCodexSeatAtExactSecondaryThreshold|TestRoutingStateReentersAfterSecondaryResetWithFreshUsage' ./... && go build ./...`

#### REPO-CPO-ALIGN-P2-T38: Project cleanup truth onto the local landing dashboards
1. Consume the existing quarantine and dead-seat visibility data from `/status?format=json` on `/` so the operator dashboard stops hiding long-dead-seat cleanup state.
2. Keep `/status` as the dense deep-ops view, but make the landing summarize the same cleanup truth instead of forcing operators to switch surfaces for that one class of state.
3. Keep the slice narrow: no new cleanup policy, only render/use the already-verified quarantine and dead-state data on the landing with targeted template regressions.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -run 'TestServeStatusPageIncludesQuarantineStatus|TestServeFriendLanding_LocalTemplateIncludesCodexOAuthAction' ./...`

#### REPO-CPO-BUG-P2-T34: Quarantine long-dead seats and keep cleanup truth visible
1. Extend dead-seat cleanup beyond temporary cooldowns so permanently bad managed seats stop inflating pool totals and recovery expectations after long failure windows.
2. Surface cleanup truth explicitly in `/status` and dashboard data instead of forcing operators to infer it from stale `dead` or health fields.
3. Keep this slice operational only: no broader provider redesign, just deterministic cleanup/recovery state for existing Codex, Gemini, and GitLab Claude seats.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -run 'TestLoadAccountsQuarantinesLongDeadAccount|TestServeStatusPageIncludesQuarantineStatus|TestServeStatusPageReturnsJSONForFormatQuery|TestBuildPoolDashboardData.*|TestReloadAccountsPreservesRuntimeState|TestGeminiProviderLoadAccountLoadsPersistedState' ./... && curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_t34.json`

#### REPO-CPO-ALIGN-P1-T37: Align the legacy Gemini operator/dashboard/docs slice
1. Reconcile `/status`, the local landing page, and `README.md` into one truthful Gemini onboarding contract instead of today’s mixed messaging between managed `/status` OAuth, fallback paste flow, and stale manual file-copy guidance.
2. Harden the operator flow itself: enforce expiring OAuth state, make the loopback redirect family consistent with the route/browser trust contract, and make popup/manual-open completion refresh the dashboard truthfully.
3. Add missing negative auth and UX regression coverage for `/operator/gemini/*`, including loopback-only checks and fallback/manual-open behavior.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -run 'TestLocalOperatorGeminiSeatAddStoresManagedSeat|TestLocalOperatorGeminiSeatAddMarksUnauthorizedSeatDead|TestLocalOperatorGeminiSeatAddIgnoresProvidedRuntimeState|TestLocalOperatorGeminiSeatAddRejectsNullAuthJSON|TestLocalOperatorGeminiOAuthStartAllowsLoopbackWithoutAdminHeader|TestLocalOperatorGeminiOAuthCallbackStoresManagedSeat|TestManagedGeminiOAuthCallbackRejectsExpiredState|TestManagedGeminiRedirectURIPreservesLoopbackFamily|TestServeStatusPageIncludesOperatorActionForLocalLoopback|TestServeStatusPageHidesOperatorActionOutsideLoopback|TestLocalOperatorGemini|TestServeStatusPage|TestServeFriendLanding_LocalTemplateIncludesCodexOAuthAction' ./...`

#### REPO-CPO-PLAN-P1-T35: Hydrate the legacy dirty-tree alignment plan through repo-local bureaucracy
1. Inventory the pre-existing unmanaged changes instead of mixing them into the just-finished Codex routing bugfix, and split them into coherent backend/runtime vs operator/dashboard/docs tracks.
2. Pull targeted risk findings and verify hooks out of specialist audits so the next work can execute as bounded cards rather than one vague “clean up the old changes” bucket.
3. Reorder the repo-local board so the active successor set reflects the real dirty-tree pressure now: Gemini runtime alignment, Gemini operator/docs alignment, then the remaining Codex cold-start hardening.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && git status --short --branch && git diff --stat -- README.md frontend_setup_scripts_test.go router.go status.go status_dashboard_test.go templates/local_landing.html provider_gemini.go gemini_operator.go provider_gemini_test.go`

#### REPO-CPO-ALIGN-P1-T36: Align the legacy Gemini provider/runtime slice
1. Turn the pre-existing Gemini backend changes into one coherent runtime contract: persisted OAuth profile and health state survive reloads, and multi-client refresh fallback continues across `400 invalid_grant` / `invalid_client` as well as `401/403`.
2. Keep the managed Gemini seat file format backward-compatible while proving that saved runtime state, recovery state, and client-profile hints round-trip truthfully through load/save/recovery flows.
3. Keep this slice backend-only: the remaining Gemini work now sits in operator/dashboard/docs behavior, not provider/runtime drift.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -run 'TestGeminiProviderLoadAccountLoadsPersistedState|TestGeminiProviderLoadAccountLoadsOAuthProfileID|TestSaveGeminiAccountPersistsStateFields|TestSaveGeminiAccountPersistsOAuthProfileID|TestFinalizeProxyResponsePersistsHealthyGeminiRecovery|TestFinalizeProxyResponsePersistsHealthyGeminiStateFromUnknown|TestFinalizeProxyResponsePersistsHealthyGeminiTimestampsWhenAlreadyHealthy|TestFinalizeWebSocketSuccessStatePersistsHealthyGeminiState|TestGeminiProviderRefreshTokenFallsBackToGCloudClient|TestGeminiProviderRefreshTokenFallsBackOn400InvalidGrant|TestGeminiProviderRefreshTokenFallsBackOn400InvalidClient|TestReloadAccountsKeepsGeminiPersistedProfileAndHealthState' ./...`

#### REPO-CPO-BUG-P1-T32: Restore truthful Codex seat routing across restarts and concurrent load
1. Wire persisted usage snapshots plus `Totals -> Usage` bridge into cold start so restart-time routing does not forget weekly/5h headroom and re-admit already-exhausted seats.
2. Apply live Codex `token_count` snapshots to `a.Usage` during streamed responses and persist them, so the selector sees updated 5h/weekly limits before the response fully completes.
3. Replace recency-only Codex reuse with an explicit active-seat lease for unpinned work: hold one local seat until its 5h or weekly headroom drops below `10%`, then rotate to the next eligible local seat or API fallback.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -run "TestRestorePersistedUsageState.*|TestWrapUsageInterceptWriterAppliesCodexSnapshot|TestReloadAccountsPreservesRuntimeState|TestParseCodexUsageDelta.*|TestUpdateUsageFromBody.*|TestCandidate.*|TestRoutingState.*|TestBuildPoolDashboardDataSelectsCurrentSeatFromInflightAndLastUsed|TestBuildPoolDashboardDataSeparatesLastUsedAndBestEligibleWhenIdle" ./... && go build ./...`

#### REPO-CPO-BUG-P1-T33: Harden cold start and low-risk Codex metadata paths
1. Soft-gate pooled Codex traffic for the initial cold-start window when local seat usage snapshots are still missing, while allowing the metadata lane to operate through restored state and cache.
2. Serve `/backend-api/codex/models` through a local cache/refresh path so Codex metadata stops depending on fragile upstream round-trips during normal CLI use.
3. Keep the new usage-state restore/lease behavior untouched while hardening only the remaining cold-start and metadata edges.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -run 'TestRestorePersistedUsageState.*|TestPeekCandidateDoesNotClaimActiveCodexSeat|TestCodexWarmState.*|TestServeCodexModels.*|TestBuildWhamUsageURLKeepsBackendAPI|TestCodexProviderUpstreamURLBackendAPIPathUsesWhamBase|TestCodexProviderNormalizePathBackendAPIPathStripsPrefix|TestStatusJSONIncludesUsageRouting|TestStatusJSONIncludesAPIKeyStats|TestProxyBufferedRetryable5xxRetriesNextSeat' ./... && go build -o /home/lap/.local/bin/codex-pool . && systemctl --user restart codex-pool.service && curl -fsS http://127.0.0.1:8989/healthz`

#### REPO-CPO-REFAC-P1-T30: Extract the pooled websocket reverse-proxy shell
1. Collapse the remaining pooled websocket reverse-proxy contour so rewrite/error/status capture wiring no longer lives as one large inline literal inside `proxyRequestWebSocket`.
2. Preserve pooled websocket semantics exactly: auth rewrite, subprotocol bearer replacement, response status capture, error handling, metrics accounting, and debug logging must remain unchanged.
3. Keep the new shared pre-copy status helper and copied-response delivery helpers untouched; this slice is only about shrinking the last large pooled websocket shell before any pooled/passthrough merge discussion.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 120s -run "TestFinalizeWebSocketSuccessState.*|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestProxyWebSocketPoolAcceptsAuthFromSubprotocol|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketMarksDeactivatedCodexAccountDeadAndFallsThroughNextSeat|TestProxyWebSocketPassthroughPreservesAuthorization" ./...`

#### REPO-CPO-REFAC-P1-T29: Share pre-copy status handling between streamed and websocket paths
1. Collapse the duplicated streamed/websocket pre-copy response contour so retryable-status inspection, raw-body replay, and status disposition live behind one explicit helper instead of staying half-inline in both paths.
2. Preserve path-specific differences exactly: websocket still skips `101 Switching Protocols`, while streamed still owns the extra non-managed `401/403` diagnostic log and passes `needStatusBody` into copied-response delivery for early flush behavior.
3. Keep websocket success finalization and copied-response delivery helpers untouched; this slice is only about removing the last duplicated pre-copy status handling after `T28`.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 120s -run "TestInspectResponseBodyForClassification|TestFinalizeWebSocketSuccessState.*|TestApplyPreCopyUpstreamStatusDisposition|TestProxyStreamedManagedAPI5xxPreservesFullErrorBody|TestProxyStreamedManagedAPI5xxDoesNotWaitForFullLargeBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketMarksDeactivatedCodexAccountDeadAndFallsThroughNextSeat" ./...`

#### REPO-CPO-REFAC-P1-T28: Extract the websocket `ModifyResponse` contour
1. Collapse the remaining websocket response-handling contour so `ParseUsageHeaders`, status-body inspection/replay, pre-copy disposition, and success finalization live behind one explicit helper instead of staying inline in `proxyRequestWebSocket`.
2. Preserve websocket semantics exactly: `101` vs non-`101` handling, failed-handshake no-pin behavior, raw error-body replay, and status/metrics propagation must remain unchanged.
3. Keep copied-response delivery helpers untouched; this slice is only about shrinking the last inline websocket response contour after `T26/T27`.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 120s -run "TestFinalizeWebSocketSuccessState.*|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketMarksDeactivatedCodexAccountDeadAndFallsThroughNextSeat|TestApplyPreCopyUpstreamStatusDisposition" ./...`

#### REPO-CPO-REFAC-P1-T27: Share success-account recovery between copied-response and websocket finalizers
1. Extract the duplicated managed-account success recovery, `LastUsed`, and penalty-decay mutation so `finalizeProxyResponse` and `finalizeWebSocketSuccessState` stop carrying parallel account-state updates.
2. Preserve path-specific differences exactly: buffered/streamed keep body-derived conversation pinning and GitLab Claude persistence, while websocket keeps request-conversation-only pinning and no extra persistence side effects.
3. Keep the pre-copy disposition helpers and shared copied-response delivery core untouched while finishing the remaining success-path duplication in one bounded slice.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 120s -run "TestFinalizeProxyResponse|TestFinalizeWebSocketSuccessState.*|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketMarksDeactivatedCodexAccountDeadAndFallsThroughNextSeat" ./...`

#### REPO-CPO-REFAC-P1-T26: Extract the websocket success-state finalizer
1. Collapse the remaining websocket success-state mutation block in `proxyRequestWebSocket` `ModifyResponse` so session pinning, managed API recovery, `LastUsed`, and penalty decay live behind one explicit helper instead of inline mutation after status disposition.
2. Preserve the current websocket semantics exactly: `101` vs non-`101` handling, failed-handshake no-pin behavior, managed API recovery, and response-body/error propagation must remain unchanged.
3. Keep the shared copied-response delivery core from `T25` untouched; websocket stays on its own lane because it does not call `finalizeCopiedProxyResponse`.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 120s -run "TestFinalizeWebSocketSuccessState.*|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketMarksDeactivatedCodexAccountDeadAndFallsThroughNextSeat|TestApplyPreCopyUpstreamStatusDisposition" ./...`

#### REPO-CPO-REFAC-P1-T25: Extract the shared copied-response delivery core
1. Collapse the duplicated buffered/streamed copied-response delivery mechanics behind one shared helper that owns header copy, usage-header replacement, SSE writer setup, idle-timeout wiring, body copy, and `finalizeCopiedProxyResponse` entry while accepting the remaining mode-specific inputs explicitly.
2. Preserve the current mode differences exactly: buffered `sampleBuf` reuse vs streamed tee sampling, buffered `conversationID` passthrough vs streamed empty pin input, streamed early flush after inspected status bodies, and the current response-body close semantics.
3. Keep websocket flow untouched while preparing a later success-path cleanup for that lane instead of widening this slice into a three-way merge.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 120s -run "TestApplyPreCopyUpstreamStatusDisposition|TestInspectResponseBodyForClassification|TestInspectBufferedRetryBody|TestProxyBuffered.*|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback" ./...`

#### REPO-CPO-REFAC-P1-T24: Extract the streamed success-delivery tail
1. Collapse the remaining streamed success-response setup in `proxyRequestStreamed` so header copy, optional early flush after inspected status bodies, sample tee wiring, SSE wrapping, idle-timeout wiring, body copy, and `finalizeCopiedProxyResponse` entry live behind one explicit helper instead of another large inline tail.
2. Preserve the current streamed delivery semantics exactly: replayed inspected status bodies, usage capture, managed-stream failure handling, empty buffered conversation pin input, and `recent`/metrics finalization must remain unchanged.
3. Keep the buffered-only delivery helper from `T23` untouched while preparing a later shared copied-response delivery slice between buffered and streamed modes.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 120s -run "TestApplyPreCopyUpstreamStatusDisposition|TestInspectResponseBodyForClassification|TestInspectBufferedRetryBody|TestProxyBuffered.*|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback" ./...`

#### REPO-CPO-REFAC-P1-T23: Extract the buffered success-delivery tail
1. Collapse the remaining buffered success-response setup in `proxyRequest` so header copy, SSE wrapping, idle-timeout wiring, body copy, and `finalizeCopiedProxyResponse` entry live behind one explicit helper instead of another large inline tail.
2. Preserve the current buffered delivery semantics exactly: usage-header replacement, SSE flush/usage interception, idle timeout handling, conversation pinning, and `recent`/metrics finalization must remain unchanged.
3. Keep the new retry-attempt contour from `T22` untouched while preparing the buffered path for a later shared copied-response transport slice with streamed mode.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 120s -run "TestApplyPreCopyUpstreamStatusDisposition|TestInspectResponseBodyForClassification|TestInspectBufferedRetryBody|TestProxyBuffered.*|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback" ./...`

#### REPO-CPO-REFAC-P1-T22: Extract the buffered retry attempt contour
1. Collapse the remaining repeated buffered retry-attempt bookkeeping in `proxyRequest` so account selection, exclusion, retryable-status formatting, and final all-attempt failure shaping share one explicit contour.
2. Preserve the current disposition semantics for ordinary Codex seats, managed API keys, and managed GitLab Claude tokens exactly as locked by `T20/T21`.
3. Keep streamed and websocket paths untouched while preparing the buffered path for smaller follow-on routing refactors instead of more ad hoc branch growth.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 120s -run "TestApplyPreCopyUpstreamStatusDisposition|TestInspectResponseBodyForClassification|TestInspectBufferedRetryBody|TestProxyBuffered.*|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback" ./...`

#### REPO-CPO-TEST-P1-T21: Add buffered GitLab Claude retry parity coverage
1. Add focused non-stream proxy tests for GitLab Claude buffered `402/401/403` retry behavior, including quota-exceeded cooldown handling and refresh-failed dead-state handling where applicable.
2. Verify that buffered Claude/GitLab retries keep their current semantics without borrowing any streamed/websocket replay assumptions.
3. Keep the new buffered Codex parity suite unchanged while closing the last provider-specific buffered coverage gap before broader retry-loop extraction.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 120s -run "TestApplyPreCopyUpstreamStatusDisposition|TestInspectResponseBodyForClassification|TestInspectBufferedRetryBody|TestProxyBuffered.*|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback" ./...`

#### REPO-CPO-TEST-P1-T20: Add buffered retry parity coverage for status handling
1. Add focused non-stream proxy tests that exercise buffered retry behavior for managed API `402/429` and ordinary account `402/403/5xx` branches, so the retry loop is covered as behavior rather than only through helper-level tests.
2. Verify that buffered retries keep their current semantics: managed API fallback/dead-state handling, ordinary codex dead-state and retry behavior, and synthesized upstream error messages without replay assumptions.
3. Keep streamed/websocket coverage unchanged while locking the buffered path before provider-specific GitLab Claude follow-ups.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 120s -run "TestApplyPreCopyUpstreamStatusDisposition|TestInspectResponseBodyForClassification|TestInspectBufferedRetryBody|TestProxyBuffered.*|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback" ./...`

#### REPO-CPO-REFAC-P1-T19: Simplify buffered retry status branches after inspection split
1. Use the explicit buffered inspection helper as the only semantic body snapshot primitive in the non-stream retry loop, and remove the remaining repeated per-branch inspection calls around retryable `402/429/401/403/5xx` handling.
2. Keep the streamed/websocket pre-copy replay contract untouched while shrinking buffered retry branching to status-specific disposition logic only.
3. Lock the slice with focused helper/replay parity tests before returning to deeper buffered retry coverage.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 120s -run "TestApplyPreCopyUpstreamStatusDisposition|TestInspectResponseBodyForClassification|TestInspectBufferedRetryBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback" ./...`

#### REPO-CPO-REFAC-P1-T18: Align buffered inspection with the split pre-copy contract
1. Keep streamed/websocket pre-copy inspection on the split `preCopyInspection` contract, while making the buffered retry loop explicitly use a separate fully buffered semantic snapshot helper instead of a dead compatibility shim.
2. Remove the transitional `inspectAndReplayResponseBody` path now that no production caller needs a one-call “inspect plus replay” helper.
3. Lock the slice with focused inspection tests plus retry-path parity before returning to broader buffered branch simplification.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 120s -run "TestApplyPreCopyUpstreamStatusDisposition|TestInspectResponseBodyForClassification|TestInspectBufferedRetryBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback" ./...`

#### REPO-CPO-REFAC-P1-T17: Separate pre-copy inspection semantics from replay transport
1. Split the shared pre-copy status-inspection contract so transport body replay and semantic error classification no longer overload the same helper return value.
2. Preserve streamed and websocket non-`101` client-visible error bodies exactly while unifying gzip/plain inspection behavior behind one bounded semantic inspector.
3. Lock the refactor with parity tests for managed API quota/auth bodies and websocket fallback responses before touching broader retry or provider routing paths.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 120s -run "TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback|TestApplyPreCopyUpstreamStatusDisposition" ./...`

#### REPO-CPO-BUG-P1-T12: Harden gzip retryable inspection against short first reads
1. Remove the remaining chunking sensitivity in gzip retryable-status inspection so managed API quota/auth markers are not lost when the transport surfaces only a short first read.
2. Preserve early error response delivery and full client-visible body replay for streamed and websocket non-`101` paths.
3. Lock the slice with late-marker and short-read gzip fixtures before returning to lower-priority websocket success-state refactors.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 120s -run "TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429DoesNotWaitForFullLargeBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestApplyPreCopyUpstreamStatusDisposition" ./...`

#### REPO-CPO-REFAC-P1-T16: Harden GitLab Claude persistence and health truth
1. Collapse `saveGitLabClaudeAccount` to one fail-closed persistence path so managed GitLab Claude files stop relying on dual serialization branches and ad hoc map rewrites.
2. Shorten dashboard/status lock scope and keep GitLab health/rate-limit state truthful even when refresh or quota probes fail.
3. Lock the slice with focused GitLab/status tests and a live `/status?format=json` smoke on the restarted service.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -run 'TestClaudeProviderLoadsGitLabManagedAccount|TestClaudeProviderRefreshGitLabManagedAccount|TestClassifyManagedGitLabClaudeErrorQuotaExceeded|TestBuildPoolDashboardDataShowsGitLabDirectAccessSignals|TestServeStatusPageReturnsJSONForFormatQuery' ./... && go build -o /home/lap/.local/bin/codex-pool . && systemctl --user restart codex-pool.service && curl -fsS http://127.0.0.1:8989/healthz && python3 - <<'PY'\nimport json, urllib.request\npayload = json.load(urllib.request.urlopen('http://127.0.0.1:8989/status?format=json'))\nassert payload['gitlab_claude_pool']['total_tokens'] >= 0\nassert 'accounts' in payload and isinstance(payload['accounts'], list)\nPY`

#### REPO-CPO-UI-P1-T15: Turn the local landing page into a dashboard-first operator surface
1. Replace the decorative local landing hierarchy with an operator-first dashboard shell that uses `/status?format=json` as the single live data source.
2. Make the `Codex`, `Claude`, and `Gemini` tabs render provider-specific live summaries, operator actions, and filtered account tables instead of setup-first marketing panels.
3. Move setup/manual blocks below the live dashboards, remove hero-image-driven presentation from the local landing, and lock the slice with landing HTML tests plus a local visual smoke.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -run 'TestServeFriendLanding_LocalTemplateIncludesCodexOAuthAction|TestServeStatusPageReturnsJSONForFormatQuery|TestBuildPoolDashboardDataShowsGitLabDirectAccessSignals' ./... && go build ./... && curl -fsS http://127.0.0.1:8989/ >/tmp/cpo_local_landing_dashboard.html && rg -n 'Codex Dashboard|Claude Dashboard|Gemini Dashboard|Fallback API Pool|GitLab Claude Pool' /tmp/cpo_local_landing_dashboard.html && ! rg -n '/hero.png|hero-art|hero-wrapper' /tmp/cpo_local_landing_dashboard.html`

#### REPO-CPO-BUG-P1-T14: Count non-stream Claude message usage in local totals
1. Ensure ordinary non-stream Claude `/v1/messages` JSON responses contribute to local usage aggregates instead of only SSE `message_start` / `message_delta` events.
2. Lock the regression with a focused parser/accounting test that exercises a top-level Anthropic `{"type":"message","usage":...}` payload.
3. Verify on the live user service with a real GitLab Claude smoke request and confirm the managed `gitlab_duo` account increments `request_count` and token totals.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -run 'TestClaudeProviderParseUsageSupportsNonStreamMessagePayload|TestUpdateUsageFromBodyRecordsClaudeNonStreamMessage|TestClaudeProviderLoadsGitLabManagedAccount|TestClaudeProviderSetAuthHeadersForGitLabManagedAccount|TestClaudeProviderRefreshGitLabManagedAccount|TestProviderUpstreamURLForGitLabClaudeAccount|TestNeedsRefreshWhenGitLabClaudeGatewayStateMissing|TestClassifyManagedGitLabClaudeErrorQuotaExceeded' ./... && go build -o /home/lap/.local/bin/codex-pool . && systemctl --user restart codex-pool.service && curl -fsS http://127.0.0.1:8989/healthz && POOL_USER_TOKEN=$(jq -r '.[0].token' /home/lap/.root_layer/codex_pool/data/pool_users.json) && CLAUDE_POOL_TOKEN=$(curl -fsS "http://127.0.0.1:8989/config/claude/${POOL_USER_TOKEN}" | jq -r '.access_token') && curl -sS -X POST http://127.0.0.1:8989/v1/messages -H "Authorization: Bearer ${CLAUDE_POOL_TOKEN}" -H 'Content-Type: application/json' -H 'anthropic-version: 2023-06-01' --data '{"model":"claude-sonnet-4-20250514","max_tokens":64,"messages":[{"role":"user","content":"Reply with exactly OK"}]}' && python3 /home/lap/tools/codex_pool_manager.py status | jq '.admin_accounts[] | select(.type=="claude" and .plan_type=="gitlab_duo") | .totals'`

#### REPO-CPO-FEAT-P1-T13: Add GitLab-backed Claude pool lane
1. Add a managed GitLab Claude account mode that stores GitLab source tokens, mints short-lived Duo direct-access credentials, and routes `/v1/messages` traffic through GitLab's Anthropic-compatible gateway without forking a second Claude path.
2. Expose local-operator status UI + endpoint for adding GitLab tokens and surface pool counts/eligibility in `/status`.
3. Lock the slice with targeted Go tests, package build/test, strict runbook status, and live `/status` + operator-endpoint smoke on the restarted user service.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test /home/lap/projects/codex-pool-orchestrator -run 'TestClaudeProviderLoadsGitLabManagedAccount|TestClaudeProviderSetAuthHeadersForGitLabManagedAccount|TestClaudeProviderRefreshGitLabManagedAccount|TestProviderUpstreamURLForGitLabClaudeAccount|TestNeedsRefreshWhenGitLabClaudeGatewayStateMissing|TestClassifyManagedGitLabClaudeErrorQuotaExceeded' && go test /home/lap/projects/codex-pool-orchestrator && go build /home/lap/projects/codex-pool-orchestrator && go build -o /home/lap/.local/bin/codex-pool /home/lap/projects/codex-pool-orchestrator && systemctl --user restart codex-pool.service && python3 /home/lap/tools/codex_pool_manager.py status --strict && curl -fsS http://127.0.0.1:8989/healthz && curl -fsS http://127.0.0.1:8989/status | rg 'GitLab Claude Pool|gitlab-claude-token-add-btn|gitlab-claude-instance-input|GitLab Claude Tokens' && curl -sS -X POST http://127.0.0.1:8989/operator/claude/gitlab-token-add -H 'Content-Type: application/json' --data '{"token":""}'`

#### REPO-CPO-REFAC-P1-T9: Reuse shared pre-copy status disposition in websocket flow
1. Switch `proxyRequestWebSocket` `ModifyResponse` status handling to the shared pre-copy disposition helpers instead of carrying a third local copy of managed API failure classification, auth-failure penalties, and `5xx` penalties.
2. Preserve websocket-specific `101` handling, protocol auth rewrite, session pinning, and client-visible error bodies exactly.
3. Lock parity with focused websocket/proxy tests and live websocket smoke before touching broader routing or selector behavior.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 120s -run "TestProxyStreamedRequestClaude|TestProxyStreamedManagedAPI5xxPreservesFullErrorBody|TestProxyStreamedManagedAPI5xxDoesNotWaitForFullLargeBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429DoesNotWaitForFullLargeBody|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback|TestProxyWebSocketMarksDeactivatedCodexAccountDeadAndFallsThroughNextSeat|TestBuild.*RequestShape|TestParse|TestApplyPreCopyUpstreamStatusDisposition" ./... && go test ./... && go build ./... && go build -o /home/lap/.local/bin/codex-pool . && systemctl --user restart codex-pool.service && systemctl --user is-active codex-pool.service && curl -fsS http://127.0.0.1:8989/healthz && curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_websocket_t9.json && AUTH=$(jq -r '.tokens.access_token' /home/lap/.codex/auth.json) && timeout 60s curl -sS -N -o /tmp/cpo_live_proxy_websocket_t9.sse -w '%{http_code}' http://127.0.0.1:8989/responses -H "Authorization: Bearer $AUTH" -H 'Content-Type: application/json' --data '{"model":"gpt-5.4","instructions":"Reply with exactly OK.","store":false,"stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}' && AUTH=$(jq -r '.tokens.access_token' /home/lap/.codex/auth.json) && exec 3<>/dev/tcp/127.0.0.1/8989 && printf 'GET /responses HTTP/1.1\r\nHost: 127.0.0.1:8989\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nAuthorization: Bearer %s\r\nsession_id: live-ws-t9-smoke\r\n\r\n' \"$AUTH\" >&3 && IFS= read -r status <&3 && printf '%s\n' \"$status\" >/tmp/cpo_live_proxy_websocket_t9_handshake.txt && curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_websocket_t9_after_smoke.json`

#### REPO-CPO-BUG-P1-T10: Preserve full streamed error bodies during status inspection
1. Ensure streamed pre-copy status inspection does not truncate forwarded client error bodies when it needs to inspect managed API failures or auth failures.
2. Preserve bounded classification/logging input while rewinding the full upstream error payload back to the client.
3. Lock the regression with a streamed managed-API `5xx` test whose response body exceeds the inspection limit.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 90s -run "TestProxyStreamedRequestClaude|TestProxyStreamedManagedAPI5xxPreservesFullErrorBody|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestBuild.*RequestShape|TestParse|TestFinalizeProxyResponse|TestFinalizeCopiedProxyResponse|TestApplyPreCopyUpstreamStatusDisposition" ./... && go test ./... && go build ./... && go build -o /home/lap/.local/bin/codex-pool . && systemctl --user restart codex-pool.service && systemctl --user is-active codex-pool.service && curl -fsS http://127.0.0.1:8989/healthz && curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_streamed_error_body_fix.json && AUTH=$(jq -r '.tokens.access_token' /home/lap/.codex/auth.json) && timeout 60s curl -sS -N -o /tmp/cpo_live_proxy_streamed_error_body_fix.sse -w '%{http_code}' http://127.0.0.1:8989/responses -H "Authorization: Bearer $AUTH" -H 'Content-Type: application/json' --data '{"model":"gpt-5.4","instructions":"Reply with exactly OK.","store":false,"stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}' && curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_streamed_error_body_fix_after_smoke.json`

#### REPO-CPO-REFAC-P1-T8: Extract retryable upstream status disposition
1. Collapse duplicated pre-copy retryable status handling for buffered and streamed proxy paths: rate-limit penalties, managed API failure classification, auth-failure penalties/dead-state handling, and 5xx penalties.
2. Preserve the buffered retry loop, streamed one-shot response semantics, and response-body preservation exactly.
3. Lock parity with focused proxy tests before touching provider routing, refresh policy, or websocket flows.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 90s -run "TestProxyStreamedRequestClaude|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestBuild.*RequestShape|TestParse|TestFinalizeProxyResponse|TestFinalizeCopiedProxyResponse|TestApplyPreCopyUpstreamStatusDisposition" ./... && go test ./... && go build ./... && go build -o /home/lap/.local/bin/codex-pool . && systemctl --user restart codex-pool.service && systemctl --user is-active codex-pool.service && curl -fsS http://127.0.0.1:8989/healthz && curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_retryable_status_disposition.json && AUTH=$(jq -r '.tokens.access_token' /home/lap/.codex/auth.json) && timeout 60s curl -sS -N -o /tmp/cpo_live_proxy_retryable_status_disposition.sse -w '%{http_code}' http://127.0.0.1:8989/responses -H "Authorization: Bearer $AUTH" -H 'Content-Type: application/json' --data '{"model":"gpt-5.4","instructions":"Reply with exactly OK.","store":false,"stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}' && curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_retryable_status_disposition_after_smoke.json`

#### REPO-CPO-REFAC-P1-T7: Extract retry/error finalizer
1. Collapse duplicated post-copy retry/error bookkeeping for buffered and streamed proxy paths: `recent` error capture, error metrics, and the shared success/error exit contour around copied upstream responses.
2. Preserve status-specific penalty changes, refresh behavior, managed API failure handling, and body ownership exactly.
3. Lock parity with focused proxy tests before touching routing, auth, or provider-specific logic.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 90s -run "TestProxyStreamedRequestClaude|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestBuild.*RequestShape|TestParse|TestFinalizeProxyResponse|TestFinalizeCopiedProxyResponse" ./... && go test ./... && go build ./... && systemctl --user is-active codex-pool.service && curl -fsS http://127.0.0.1:8989/healthz && curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_retry_finalizer.json`

#### REPO-CPO-REFAC-P1-T6: Extract post-response finalizer
1. Collapse duplicated post-copy success handling for buffered and streamed proxy paths: sample logging, non-SSE usage fallback, conversation pinning, managed API recovery, and penalty decay.
2. Preserve retry, refresh, header replacement, and websocket behavior exactly.
3. Lock parity with targeted proxy tests and live smoke before touching any routing heuristics.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 90s -run "TestProxyStreamedRequestClaude|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestBuild.*RequestShape|TestParse|TestFinalizeProxyResponse" ./... && go test ./... && go build ./... && systemctl --user is-active codex-pool.service && curl -fsS http://127.0.0.1:8989/healthz && curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_post_response_finalizer.json`

#### REPO-CPO-REFAC-P1-T5: Collapse duplicated response streaming usage capture
1. Extract one shared response-stream usage recorder for buffered and streamed proxy paths in `main.go`.
2. Preserve managed API-key SSE failure handling, Claude two-event accumulation, and conversation pinning semantics exactly.
3. Lock parity with targeted proxy/usage tests before touching retry or routing behavior.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go build ./... && go test -count=1 -timeout 90s -run "TestProxyStreamedRequestClaude|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestBuild.*RequestShape|TestParse" ./... && go test ./... && systemctl --user is-active codex-pool.service && curl -fsS http://127.0.0.1:8989/healthz && curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_stream_capture.json`

#### REPO-CPO-REFAC-P1-T3: Unify usage ingestion
1. Replace duplicated header/body/SSE usage parsing with one canonical `UsageDelta` pipeline.
2. Keep provider-specific parsing only as strategy implementations over the shared contract.
3. Lock parity with JSON, SSE, and header-driven usage fixtures before touching scoring or analytics.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go build ./... && go test -count=1 -timeout 90s -run "TestMergeUsage|TestParse|TestExtract|TestUsageStore|TestCodexProviderParseUsageHeaders|TestParseRequestUsageFromSSE" ./... && go test ./... && systemctl --user is-active codex-pool.service && curl -fsS http://127.0.0.1:8989/healthz && curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_usage_ingestion.json`

#### REPO-CPO-BUG-P1-T4: Enforce codex seat cutoff and sticky selection semantics
1. Exclude Codex seats from fresh routing as soon as observed 5h or 7d usage reaches `90%`, instead of allowing the exact threshold to stay routable.
2. Reuse the most recently used eligible seat for new unpinned Codex work so the pool drains one seat until headroom reaches the cutoff, instead of spreading load evenly by score alone.
3. Preserve session affinity for streamed requests when a session header is present, and align `/status` copy and preview logic with the real selector.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go build ./... && go test -count=1 -timeout 90s -run "TestBuild.*RequestShape|TestCandidate|TestRoutingState|TestBuildPoolDashboardData|TestServeStatusPageClarifiesQuotaVsLocalFields" ./... && go test ./... && systemctl --user is-active codex-pool.service && curl -fsS http://127.0.0.1:8989/healthz && curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_sticky_logic.json`

#### REPO-CPO-REFAC-P1-T2: Freeze request planning contracts
1. Introduce canonical types for `AdmissionResult`, `RequestShape`, and `RoutePlan`.
2. Move provider/path/model/required-plan planning into a pure layer that can be reused by buffered, streamed, and websocket flows.
3. Add guardrail tests for provider routing, model override, and streamed-body opaque planning.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go build ./... && go test -count=1 -timeout 90s -run "TestBuild.*RequestShape|TestPlanRoute|TestProxyStreamedRequestClaude|TestResolveProxyAdmission|TestProxyWebSocketPoolRewritesAuthAndPinsSession" ./... && go test ./... && systemctl --user is-active codex-pool.service && curl -fsS http://127.0.0.1:8989/healthz && curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_smoke.json`

#### REPO-CPO-REFAC-P0-T1: Establish green baseline and extract proxy admission contract
1. Fix the stale streamed Claude test so the repo returns to a truthful green baseline.
2. Extract pool-user vs passthrough vs unauthorized proxy admission resolution from `main.go` into a dedicated helper without changing external behavior.
3. Verify the slice with focused Go tests, full `go test ./...`, a user-service restart, `/healthz`, `/status`, and a live `/responses` smoke on the new binary.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go build ./... && go test ./... && systemctl --user is-active codex-pool.service && curl -fsS http://127.0.0.1:8989/healthz && curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_smoke.json`

#### REPO-CPO-PROOF-T1: Verify build and targeted test suite pass
1. Run `go build ./...` and confirm exit 0.
2. Run targeted test suite (`TestMetrics|TestCandidate|TestPenalty|TestScore|TestSign|TestValidate|TestIsPool|TestLooksLike|TestSaveAccount|TestMergeUsage|TestParse|TestExtract|TestUsageStore|TestRouting|TestPinned|TestShouldStream`) and confirm all pass.
3. Record evidence in repo-local `EVIDENCE_LOG.md`.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go build ./... && go test -run "TestMetrics|TestCandidate|TestPenalty|TestScore|TestSign|TestValidate|TestIsPool|TestLooksLike|TestSaveAccount|TestMergeUsage|TestParse|TestExtract|TestUsageStore|TestRouting|TestPinned|TestShouldStream" -count=1 -timeout 60s ./...`

# EVIDENCE_LOG — codex-pool-orchestrator

> Repo-local evidence for root harness proof execution.

### 2026-03-24T18:08:49Z | REPO-CPO-ARCH-P1-T46 provider-truth schema/status slice
- Commands
  - `gofmt -w pool.go provider_gemini.go account_snapshot.go status.go gemini_operator.go gemini_antigravity.go provider_gemini_test.go status_dashboard_test.go`
  - `go test -count=1 -run '^(TestGeminiProviderLoadAccountLoadsAntigravityFields|TestSaveGeminiAccountPersistsAntigravityFields|TestBuildPoolDashboardDataIncludesGeminiProviderTruth|TestLocalOperatorGeminiAntigravityOAuthCallbackStoresImportedSeat|TestLocalOperatorGeminiAntigravityOAuthCallbackBootstrapsOnboardBeforeLoadCodeAssist|TestGeminiProviderLoadAccountLoadsPersistedState|TestSaveGeminiAccountPersistsStateFields|TestLocalOperatorGeminiSeatAddStoresManagedSeat|TestLocalOperatorGeminiSeatAddMarksUnauthorizedSeatDead|TestBuildPoolDashboardDataSeparatesGeminiOperatorLanes|TestServeStatusPageReturnsJSONForFormatQuery)$' ./...`
  - `go build ./...`
- Result
  - PASS
  - `T46` now has a real backend-first schema round-trip for Gemini provider-truth coming from Antigravity `loadCodeAssist`: subscription tier id/name, validation reason/message/url, and provider checked-at all persist through auth JSON, load back into runtime state, flow through account snapshots, and surface in `/status?format=json`.
  - Antigravity OAuth onboarding now preserves that provider-truth through the import normalization path instead of dropping it before the seat is written to disk.
  - Added focused regression coverage for the new path: load/save round-trip, status projection, and Antigravity OAuth callback persistence all execute in the same repo-local verify slice without reopening `T45`.
- Artifacts
  - live command output captured in terminal only
- Notes
  - This stays inside the first truthful `T46` sub-slice only: provider-truth schema/load-save/status projection.
  - Warm-seat admission and routing pressure remain the next bounded follow-up inside `T46/T47`; this slice does not claim that gate is finished.

### 2026-03-24T13:11:29Z | REPO-CPO-ALIGN-P1-T48 Gemini Antigravity facade live smoke
- Commands
  - `gofmt -w gemini_code_assist_facade.go gemini_code_assist_facade_test.go main.go`
  - `go test -count=1 -run 'TestMaybeBuildGeminiCodeAssistFacadeRequest|TestUnwrapGeminiCodeAssistResponse|TestTransformGeminiCodeAssistSSE|TestMaybeTransformGeminiCodeAssistFacadeResponseBuffered' ./...`
  - `go build -o /home/lap/.local/bin/codex-pool .`
  - `payload=$(jq -Rs '{auth_json: .}' /home/lap/.antigravity_tools/accounts/2a208961-4979-41d3-9697-3d6929601489.json) && curl -fsS -X POST http://127.0.0.1:8989/operator/gemini/import-oauth-creds -H 'Content-Type: application/json' --data "$payload" >/tmp/cpo_antigravity_import_working_response.json`
  - `curl -fsS -X POST http://127.0.0.1:8989/v1beta/models/gemini-2.5-flash:generateContent -H "x-goog-api-key: $POOL_KEY" -H 'Content-Type: application/json' --data @/tmp/cpo_pool_v1beta_probe_body.json >/tmp/cpo_pool_v1beta_probe.out`
  - `GEMINI_API_KEY="$POOL_KEY" GOOGLE_GEMINI_BASE_URL='http://127.0.0.1:8989' GOOGLE_API_KEY='' GOOGLE_GENAI_USE_GCA='' GOOGLE_CLOUD_ACCESS_TOKEN='' CODE_ASSIST_ENDPOINT='' gemini -m gemini-2.5-flash -p 'Reply with exactly AG_POOL_OK.' --output-format text >/tmp/cpo_gemini_cli_smoke_success.txt 2>/tmp/cpo_gemini_cli_smoke_success.err`
- Result
  - PASS
  - Our pool now preserves its own auth and pool-user surface while translating pooled Gemini `/v1beta` content calls into Google Code Assist `v1internal` requests for imported Antigravity Gemini OAuth seats, then unwraps the responses back into Gemini API shape for clients.
  - Direct Gemini Developer API upstream was not usable for these imported seats because it rejected the OAuth token scope, so the live pooled path now depends on the Code Assist facade instead of the developer API upstream.
  - A working imported Antigravity seat carrying `token.project_id = psyched-sphere-vj8c5` became healthy again and served both the direct pool probe (`DIRECT_POOL_OK`) and the real Gemini CLI smoke (`AG_POOL_OK`).
  - Post-smoke status showed the imported seat healthy and recently used in the live pool.
- Artifacts
  - `/tmp/cpo_pool_v1beta_probe.out`
  - `/tmp/cpo_gemini_cli_smoke_success.txt`
  - `/tmp/cpo_gemini_cli_smoke_success.err`
  - `/tmp/cpo_gemini_status_after_success.json`
  - `/tmp/cpo_antigravity_import_working_response.json`
  - `/tmp/cpo_pool_user_create_probe.json`
- Notes
  - `POOL_KEY` in this slice means the synthetic Gemini pool API key used for `x-goog-api-key` admission, not the raw pool-user download token.
  - Imported Antigravity seats without `project_id` were insufficient for this live Gemini facade lane.
  - Do not record live tokens, pool keys, or auth payload contents in repo-local evidence.

### 2026-03-24T12:26:45Z | REPO-CPO-BUG-P1-T45 residual projection-truth cleanup
- Commands
  - `gofmt -w pool.go status_dashboard_test.go`
  - `go test -count=1 -run '^(TestGeminiProviderLoadAccountLeavesLegacyOperatorSourceUnsetWithoutProfileID|TestGeminiProviderLoadAccountInfersManagedOAuthFromOperatorEmail|TestGeminiProviderRefreshTokenPrefersManagedProfileBeforeLegacyRawClient|TestGeminiProviderRefreshTokenMigratesLegacySeatToManagedProfile|TestGeminiProviderRefreshTokenFallsBackToGCloudClient|TestGeminiProviderRefreshTokenFallsBackOn400InvalidGrant|TestGeminiProviderRefreshTokenFallsBackOn400InvalidClient|TestBuildPoolDashboardDataSeparatesGeminiOperatorLanes|TestBuildPoolDashboardDataLeavesLegacyGeminiOperatorSourceUnsetWithoutProvenance)$' ./...`
  - `go build ./...`
- Result
  - PASS
  - The residual `T45` projection mismatch is closed: orphan Gemini seats with no explicit `operator_source` and no `oauth_profile_id` no longer get projected back into `manual_import_legacy` through `normalizeGeminiOperatorSource`, so operator-facing JSON/dashboard projection now matches the runtime/storage truth established by the earlier compatibility slice.
  - Added focused regression coverage for the operator surface: `TestBuildPoolDashboardDataLeavesLegacyGeminiOperatorSourceUnsetWithoutProvenance` proves such a legacy seat keeps an empty `operator_source` in dashboard projection and does not inflate managed/imported Gemini operator counts.
  - This cleanup stayed deliberately bounded: it does not begin `REPO-CPO-ARCH-P1-T46` provider-truth persistence or `REPO-CPO-BUG-P1-T47` routing logic; it only clears the truthful foundation those later slices will build on.
- Artifacts
  - live command output captured in terminal only
- Notes
  - No live binary restart or `/status` smoke was performed in this residual cleanup slice because the change is limited to in-process projection logic plus regression coverage.

### 2026-03-24T11:43:00Z | REPO-CPO-BUG-P1-T45
- Commands
  - `gofmt -w provider_gemini.go provider_gemini_test.go`
  - `go test -count=1 -run '^(TestGeminiProviderLoadAccountLeavesLegacyOperatorSourceUnsetWithoutProfileID|TestGeminiProviderLoadAccountInfersManagedOAuthFromOperatorEmail|TestGeminiProviderRefreshTokenPrefersManagedProfileBeforeLegacyRawClient|TestGeminiProviderRefreshTokenMigratesLegacySeatToManagedProfile|TestGeminiProviderRefreshTokenFallsBackToGCloudClient|TestGeminiProviderRefreshTokenFallsBackOn400InvalidGrant|TestGeminiProviderRefreshTokenFallsBackOn400InvalidClient|TestLocalOperatorGeminiOAuthStartAllowsLoopbackWithoutAdminHeader|TestManagedGeminiOAuthCallbackStoresManagedSeat|TestBuildPoolDashboardDataSeparatesGeminiOperatorLanes)$' ./...`
  - `go build ./...`
  - `curl -fsS http://127.0.0.1:8989/status?format=json | jq '{gemini_operator:.gemini_operator,gemini_accounts:[.accounts[]|select(.type=="gemini")|{id,operator_source,health_status}]}'`
- Result
  - PASS
  - Legacy Gemini seats that were saved without explicit `operator_source` no longer get force-labeled as `manual_import_legacy` during `LoadAccount`. That preserves the distinction between truly explicit manual-import seats and older seats whose provenance was never written to disk.
  - Older managed Gemini seats that already carried `operator_email` still load as managed without waiting for a later refresh, so the compatibility fix does not regress the previously introduced managed-seat inference.
  - A legacy seat can now self-migrate cleanly into the managed Gemini lane after a successful env/gcloud refresh fallback: the refresh path persists `oauth_profile_id`, drops stale raw client credentials, and now also writes `operator_source = managed_oauth` instead of staying stuck in a false manual-import label forever.
  - The running local service still degrades honestly when no Gemini OAuth client is configured in its env. Live `/status?format=json` continues to report `managed_oauth_available = false`, `managed_seat_count = 0`, `imported_seat_count = 4`; this slice did not fake managed availability or try to pull T46/T47 quota-routing behavior forward.
- Artifacts
  - live command output captured in terminal only
- Notes
  - The migration is intentionally bounded: seats with an explicit stored source still keep that source; only legacy seats that had no persisted provenance can be promoted into the managed lane by a successful profile-backed refresh.
  - This slice repairs operator/runtime truth for env-backed Gemini recovery only. Provider-truth persistence, warm-seat admission, and Gemini routing pressure remain in `T46` and `T47`.

### 2026-03-24T10:38:00Z | REPO-CPO-ALIGN-P1-T43
- Commands
  - `gofmt -w pool.go account_snapshot.go provider_gemini.go gemini_operator.go status.go router.go provider_gemini_test.go status_dashboard_test.go frontend_setup_scripts_test.go`
  - `go test -count=1 -run 'TestBuildPoolDashboardDataSeparatesGeminiOperatorLanes|TestGeminiProviderLoadAccountLoadsPersistedState|TestSaveGeminiAccountPersistsOAuthProfileID|TestGeminiProviderRefreshTokenFallsBackToGCloudClient|TestGeminiProviderRefreshTokenFallsBackOn400InvalidGrant|TestGeminiProviderRefreshTokenFallsBackOn400InvalidClient|TestLocalOperatorGeminiSeatAddStoresManagedSeat|TestLocalOperatorGeminiSeatAddMarksUnauthorizedSeatDead|TestLocalOperatorGeminiSeatAddIgnoresProvidedRuntimeState|TestLocalOperatorGeminiSeatAddRejectsNullAuthJSON|TestLocalOperatorGeminiOAuthStartAllowsLoopbackWithoutAdminHeader|TestManagedGeminiOAuthCallbackRejectsExpiredState|TestManagedGeminiRedirectURIPreservesLoopbackFamily|TestLocalOperatorGeminiOAuthCallbackStoresManagedSeat|TestServeStatusPageIncludesOperatorActionForLocalLoopback|TestServeStatusPageHidesOperatorActionOutsideLoopback|TestServeFriendLanding_LocalTemplateIncludesCodexOAuthAction' ./...`
  - `go build ./...`
  - `go build -o /home/lap/.local/bin/codex-pool .`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_gemini_split_status.json`
  - `jq '{gemini_operator:.gemini_operator, gemini_accounts:[.accounts[]|select(.type=="gemini")|{id,operator_source,health_status,block_reason:(.routing.block_reason//null)}]}' /tmp/cpo_gemini_split_status.json`
  - `curl -fsS http://127.0.0.1:8989/ >/tmp/cpo_gemini_split_landing.html`
  - `rg -n 'Managed Gemini OAuth|Manual Gemini Import|Import oauth_creds.json|Managed OAuth|Imported Seats' /tmp/cpo_gemini_split_landing.html`
- Result
  - PASS
  - Gemini operator flow is now split into two explicit lanes instead of one ambiguous dashboard action. `/status` and `/` both show `Managed Gemini OAuth` separately from `Manual Gemini Import`, and the raw `oauth_creds.json` path is no longer described as a fallback/API pool.
  - The live JSON contract now surfaces Gemini operator truth directly: the running service reports `gemini_operator.managed_oauth_available = false`, `managed_seat_count = 0`, `imported_seat_count = 4`, and every current Gemini account is labeled `operator_source = "manual import"`.
  - The live landing page reflects the same split. Captured HTML includes `Managed Gemini OAuth`, `Manual Gemini Import`, `Import oauth_creds.json`, `Imported Seats`, and `Managed OAuth`, so the operator-facing DOM no longer hides the lane distinction behind one mixed CTA.
  - Managed Gemini OAuth now degrades honestly when the service env is not configured. Instead of exposing a broken button that silently behaves like another flow, the running UI reports that the service has no configured Gemini OAuth client and leaves only the manual-import lane available.
- Artifacts
  - `/tmp/cpo_gemini_split_status.json`
  - `/tmp/cpo_gemini_split_landing.html`
- Notes
  - This slice intentionally stopped before repairing the underlying managed Gemini OAuth runtime for env-backed recovery of older seats; that successor is now tracked separately after the UI/operator split landed.

### 2026-03-24T10:08:00Z | REPO-CPO-VERIFY-P1-T41
- Commands
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_t41_before.json`
  - `python3 /home/lap/tools/codex_pool_manager.py status --strict | jq -r '.admin_accounts[] | select(.type=="codex" and .plan_type!="api" and (.routing.eligible // false)) | .id' > /tmp/cpo_t41_20260324060556/eligible_ids.txt`
  - `cp -p /home/lap/.root_layer/codex_pool/pool/codex/<id>.json /tmp/cpo_t41_20260324060556/<id>.json.bak`
  - `jq '.disabled = true' /home/lap/.root_layer/codex_pool/pool/codex/<id>.json > <tmp> && mv <tmp> /home/lap/.root_layer/codex_pool/pool/codex/<id>.json`
  - `curl -fsS -X POST http://127.0.0.1:8989/admin/reload -H "X-Admin-Token: <admin-token>"`
  - `python3 /home/lap/tools/codex_pool_manager.py status --strict | jq '{current_seat:.current_seat,last_used_seat:.last_used_seat,openai_api_pool:.openai_api_pool,eligible_codex:[.admin_accounts[]|select(.type=="codex" and (.routing.eligible // false))|{id,plan_type,block_reason:(.routing.block_reason//null)}], blocked_local:[.admin_accounts[]|select(.type=="codex" and .plan_type!="api" and (.routing.eligible != true))|{id,block_reason:(.routing.block_reason//null)}]}'`
  - `AUTH=$(jq -r '.tokens.access_token' /home/lap/.codex/auth.json) && timeout 60s curl -sS -N -o /tmp/cpo_t41_responses.sse -w '%{http_code}' http://127.0.0.1:8989/v1/responses -H "Authorization: Bearer $AUTH" -H 'Content-Type: application/json' --data '{"model":"gpt-5.4","instructions":"Reply with exactly OK.","input":[{"role":"user","content":[{"type":"input_text","text":"Reply with exactly OK."}]}],"store":false,"stream":true}'`
  - `cp -p /tmp/cpo_t41_20260324060556/<id>.json.bak /home/lap/.root_layer/codex_pool/pool/codex/<id>.json`
  - `curl -fsS -X POST http://127.0.0.1:8989/admin/reload -H "X-Admin-Token: <admin-token>"`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_t41_after.json`
  - `python3 /home/lap/tools/codex_pool_manager.py status --strict | jq '{eligible_codex:[.admin_accounts[]|select(.type=="codex" and .plan_type!="api" and (.routing.eligible // false))|.id], openai_api_pool:.openai_api_pool}'`
  - `rg -n 'response.completed|OK' /tmp/cpo_t41_responses.sse`
  - `curl -fsS http://127.0.0.1:8989/healthz`
- Result
  - PASS
  - Controlled cutover succeeded. After temporarily disabling the currently eligible local Codex seats and reloading the pool, the live admin view showed that the only remaining eligible Codex account was the fallback API key `openai_api_77ae4df0081f`, while the previously eligible local seats were all blocked with `block_reason = "disabled"`.
  - The pooled Codex request still completed successfully during that cutover window: `POST /v1/responses` returned HTTP `200`, and the captured SSE stream contains both `response.completed` and assistant output `OK`. This proves the pool can continue serving through the fallback API lane when no local Codex seat remains eligible.
  - Recovery also succeeded. After restoring the backed-up local seat files and reloading again, the normal eligible local set returned (`andy_4`, `andy_5`, `john4454_2`, `john4454_3`, `john4454_4`, `luka_2`, `primary`), the fallback API key stayed healthy/eligible, and `/healthz` remained `ok`.
  - The post-restore status JSON also reflects the fallback request in operator truth: `last_used_seat.id == "openai_api_77ae4df0081f"` while `current_seat.id == "primary"` for the restored local selector state.
- Artifacts
  - `/tmp/cpo_t41_20260324060556/eligible_ids.txt`
  - `/tmp/cpo_t41_20260324060556/*.json.bak`
  - `/tmp/cpo_status_t41_before.json`
  - `/tmp/cpo_status_t41_after.json`
  - `/tmp/cpo_t41_responses.sse`
- Notes
  - This was a controlled availability cutover using temporary `disabled` flags, not a destructive delete/re-add and not a synthetic Bolt snapshot mutation.
  - The live fallback lane is now proven end-to-end; further work should focus on any remaining operator-facing polish or deeper automation around this drill, not on basic fallback viability.

### 2026-03-24T09:22:00Z | REPO-CPO-BUG-P1-T42
- Commands
  - `go test -count=1 -run 'TestRoutingStateBlocksRateLimitedLocalCodexSeat|TestCandidateSkipsRateLimitedLocalCodexSeat|TestCandidateRetryPathDoesNotMoveActiveCodexSeat|TestRoutingStateBlocksRateLimitedManagedOpenAIAPIKey|TestCandidateDropsActiveCodexSeatAtExactPrimaryThreshold|TestCandidateDropsActiveCodexSeatAtExactSecondaryThreshold' ./...`
  - `go build ./...`
  - `go build -o /home/lap/.local/bin/codex-pool .`
  - `systemctl --user restart codex-pool.service`
  - `curl -fsS http://127.0.0.1:8989/healthz`
  - `curl -fsS http://127.0.0.1:8989/status?format=json | jq '{current_seat,last_used_seat,openai_api_pool,codex_seat_count}'`
  - `journalctl --user -u codex-pool.service -n 20 --no-pager`
- Result
  - PASS
  - Local Codex `429` cooldowns now gate routing the same way managed fallback keys already did: if `RateLimitUntil` is still in the future, the selector blocks the seat with `block_reason = rate_limited` instead of marking a debug-only bypass and continuing to reuse it.
  - Retry fallthrough no longer rewrites the active Codex lease. The selector still establishes stickiness on the first clean candidate of a request, but retry candidates selected under a non-empty exclude set no longer poison future traffic by replacing `activeCodexID`.
  - Focused regressions for local cooldown blocking, retry-path lease stability, and the existing exact-threshold rotation behavior all passed, and the rebuilt service came back healthy on the new binary (`/healthz -> {"status":"ok","uptime":"9s"}`).
  - After restart the live status surface reflected the updated selector state cleanly: the pool exposed a normal eligible local seat as `current_seat` and kept the API fallback lane separate (`next_key_id = "openai_api_77ae4df0081f"`), with no startup failure on the new routing logic.
- Artifacts
  - live command output captured in terminal only
- Notes
  - This slice locks the selector semantics in tests; it did not yet run a forced live `429` or full fallback exhaustion drill.
  - The next truthful successor remains `REPO-CPO-VERIFY-P1-T41`, which owns the controlled live threshold/exclusion/fallback proof.

### 2026-03-24T09:18:00Z | REPO-CPO-VERIFY-P1-T40
- Commands
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_t40_before.json`
  - `AUTH=$(jq -r '.tokens.access_token' /home/lap/.codex/auth.json) && timeout 60s curl -sS -N -o /tmp/cpo_t40_responses.sse -w '%{http_code}' http://127.0.0.1:8989/v1/responses -H "Authorization: Bearer $AUTH" -H 'Content-Type: application/json' --data '{"model":"gpt-5.4","instructions":"Reply with exactly OK.","input":[{"role":"user","content":[{"type":"input_text","text":"Reply with exactly OK."}]}],"store":false,"stream":true}'`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_t40_after.json`
  - `sleep 3 && curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_t40_post.json`
  - `rg -n 'response.completed|OK' /tmp/cpo_t40_responses.sse`
  - `jq -n '{before: (input | {current: .current_seat.id, last: .last_used_seat.id, api_next: .openai_api_pool.next_key_id}), after: (input | {current: .current_seat.id, last: .last_used_seat.id, api_next: .openai_api_pool.next_key_id}), post: (input | {current: .current_seat.id, last: .last_used_seat.id, api_next: .openai_api_pool.next_key_id})}' /tmp/cpo_status_t40_before.json /tmp/cpo_status_t40_after.json /tmp/cpo_status_t40_post.json`
- Result
  - PASS
  - The live pooled Codex request completed successfully with HTTP `200`, and the captured SSE stream contains both `response.completed` and assistant output `OK`, so the running service handled the request end-to-end on the updated binary.
  - The active local Codex seat stayed stable across the smoke: `current_seat.id` was `luka_2` before, immediately after, and a few seconds after the request. The API fallback pointer also stayed unchanged (`openai_api_pool.next_key_id == "openai_api_77ae4df0081f"`), so this smoke did not unexpectedly jump to the fallback lane.
  - The post-request status still showed the same live seat with fresh headroom (`primary_headroom_pct = 92`, `secondary_headroom_pct = 30`) and active in-flight work, which is consistent with the selector continuing to hold one eligible local seat instead of spreading the request onto another account.
- Artifacts
  - `/tmp/cpo_status_t40_before.json`
  - `/tmp/cpo_status_t40_after.json`
  - `/tmp/cpo_status_t40_post.json`
  - `/tmp/cpo_t40_responses.sse`
- Notes
  - This smoke intentionally did not force the pool through an exhaustion or fallback cutover; it only proved sticky local-seat behavior on a healthy request path after `T39`.
  - The next truthful successor is `REPO-CPO-VERIFY-P1-T41`, which now owns the controlled threshold/exclusion/fallback exercise.

### 2026-03-24T09:08:00Z | REPO-CPO-BUG-P1-T39
- Commands
  - `go test -count=1 -run 'TestApplyUsageSnapshotDoesNotCarryExpiredResetAcrossTokenCount|TestRestorePersistedUsageStatePrefersNewerTotalsWhenSnapshotStale|TestCandidateStopsReusingMostRecentlyUsedSeatAtExactSecondaryThreshold|TestCandidateDropsActiveCodexSeatAtExactSecondaryThreshold|TestRoutingStateReentersAfterSecondaryResetWithFreshUsage' ./...`
  - `go build ./...`
  - `go build -o /home/lap/.local/bin/codex-pool .`
  - `systemctl --user restart codex-pool.service`
  - `curl -fsS http://127.0.0.1:8989/healthz`
  - `curl -fsS http://127.0.0.1:8989/status?format=json | jq '{current_seat, last_used_seat, codex_seat_count, openai_api_pool, quarantine}'`
  - `journalctl --user -u codex-pool.service -n 20 --no-pager`
- Result
  - PASS
  - Fresh Codex `token_count` snapshots no longer inherit expired reset timestamps from older usage state. That closes the rollover bug where post-reset burn could still be interpreted as `0%` because an old `PrimaryResetAt` or `SecondaryResetAt` was silently carried forward and then zeroed by routing.
  - Restore now lets newer persisted `Totals` repair an older saved usage snapshot instead of treating any non-zero `Usage.RetrievedAt` as untouchable. The new regression keeps unexpired reset times when they are still valid, but stale percentages no longer survive restart just because a snapshot happened to exist.
  - Secondary-window routing is now explicitly locked at the selector layer: the most-recently-used seat and the active Codex lease both rotate away at the exact weekly threshold, and a fresh post-reset snapshot re-enters routing cleanly.
  - The rebuilt service came back healthy on the new binary (`/healthz -> {"status":"ok","uptime":"9s"}`), restored persisted state on startup (`totals=17 snapshots=14 bridged_from_totals=0` in `journalctl`), and the live status JSON showed one sticky local seat (`current_seat.id == last_used_seat.id == "luka_2"`) instead of an immediately drifting selection.
- Artifacts
  - live command output captured in terminal only
- Notes
  - The live pool did not happen to contain a stale snapshot that needed totals-bridging at this restart, so that path is locked by the new targeted regression rather than by opportunistic production state.
  - The next truthful successor is `REPO-CPO-VERIFY-P1-T40`, which will gather before/after runtime artifacts for live stickiness and controlled fallback cutover on the running pool.

### 2026-03-24T08:57:37Z | REPO-CPO-ALIGN-P2-T38
- Commands
  - `go test -count=1 -run 'TestServeStatusPageIncludesQuarantineStatus|TestServeFriendLanding_LocalTemplateIncludesCodexOAuthAction' ./...`
  - `curl -fsS http://127.0.0.1:8989/healthz`
  - `curl -fsS http://127.0.0.1:8989/status?format=json | jq '{quarantine, providers: [.accounts[]?.provider] | unique}'`
  - `Playwright navigate http://127.0.0.1:8989/`
  - `Playwright screenshot /home/lap/cpo-landing-t38.png`
- Result
  - PASS
  - The landing page now truthfully mirrors cleanup state from the live `/status?format=json` contract instead of hiding it behind the deep-ops `/status` page. The overview surface exposes the `Quarantine` card and warns from the same `quarantine.total/providers/recent` payload already used by the status dashboard.
  - Account health copy on `/` now carries `dead since ...` details, so long-dead-seat cleanup state is visible on the landing itself rather than only inside `/status` JSON/HTML.
  - Live proof on the running service stayed healthy during the slice: `/healthz` returned `{"status":"ok","uptime":"1m"}`, `/status?format=json` reported `quarantine.total = 0`, and the browser snapshot showed the landing overview with the new `Quarantine` card reading `0` and `No long-dead seats currently quarantined.`
- Artifacts
  - `/home/lap/cpo-landing-t38.png`
- Notes
  - This slice only closed operator-surface truth for already-existing cleanup data; it did not add new cleanup policy.
  - The next truthful successor is `REPO-CPO-BUG-P1-T39`, which now owns the remaining Codex routing mismatch around quota freshness at reset/restart boundaries.

### 2026-03-24T08:18:00Z | REPO-CPO-ALIGN-P1-T36
- Commands
  - `go test -count=1 -run 'TestGeminiProviderLoadAccountLoadsPersistedState|TestGeminiProviderLoadAccountLoadsOAuthProfileID|TestSaveGeminiAccountPersistsStateFields|TestSaveGeminiAccountPersistsOAuthProfileID|TestFinalizeProxyResponsePersistsHealthyGeminiRecovery|TestFinalizeProxyResponsePersistsHealthyGeminiStateFromUnknown|TestFinalizeProxyResponsePersistsHealthyGeminiTimestampsWhenAlreadyHealthy|TestFinalizeWebSocketSuccessStatePersistsHealthyGeminiState|TestGeminiProviderRefreshTokenFallsBackToGCloudClient|TestGeminiProviderRefreshTokenFallsBackOn400InvalidGrant|TestGeminiProviderRefreshTokenFallsBackOn400InvalidClient|TestReloadAccountsKeepsGeminiPersistedProfileAndHealthState' ./...`
- Result
  - PASS
  - Gemini refresh fallback is now locked for both existing `401/403` behavior and the previously unguarded `400 invalid_grant` / `400 invalid_client` OAuth responses, so one bad public client profile no longer strands an otherwise usable Gemini refresh token without regression coverage.
  - Managed Gemini seat files now have an explicit reload-proof contract in tests: persisted `oauth_profile_id`, `health_status`, `health_error`, `health_checked_at`, `last_healthy_at`, and `rate_limit_until` survive a pool reload while runtime-only `Usage`, `Penalty`, `LastUsed`, and `Totals` are still preserved.
  - Successful Gemini HTTP and websocket proxy paths now persist healthy operator state back to disk, so reload truth no longer depends only on failure probes or stale pre-success snapshots.
  - `applyRuntimeState` no longer clobbers a freshly loaded Gemini `rate_limit_until` with a stale zero-value from the previous in-memory pool, which was the hidden reload coupling between persisted operator probe state and hot-reload runtime merge.
  - The slice stayed backend-only: no `/status`, landing-page, or docs/UI behavior changed here.
- Artifacts
  - targeted test output captured in terminal only
- Notes
  - Existing load/save/finalize Gemini tests were already green; this slice closed the remaining runtime gaps by adding the missing bad-request fallback coverage, healthy-state persistence coverage, and the reload round-trip guard.
  - The next truthful successor is `REPO-CPO-ALIGN-P1-T37`, which now owns the Gemini operator/dashboard/docs contract on top of this verified backend state.

### 2026-03-24T08:39:57Z | REPO-CPO-BUG-P2-T34
- Commands
  - `gofmt -w pool_test.go status_dashboard_test.go`
  - `go test -count=1 -run 'TestLoadAccountsQuarantinesLongDeadAccount|TestServeStatusPageIncludesQuarantineStatus|TestServeStatusPageReturnsJSONForFormatQuery|TestBuildPoolDashboardData.*|TestReloadAccountsPreservesRuntimeState|TestGeminiProviderLoadAccountLoadsPersistedState' ./...`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_t34.json`
- Result
  - PASS
  - Long-dead seats are now explicitly locked by regression coverage at the loader boundary: a seat with `dead_since > 72h` is moved under `pool/quarantine/...`, excluded from the active in-memory pool, and counted in the quarantine summary instead of silently inflating live routing totals.
  - `/status?format=json` and the loopback HTML status page now have direct regression coverage for quarantine visibility, so cleanup truth is no longer implicit implementation detail only. The JSON payload exposes `quarantine.total/providers/recent`, and the local HTML operator card renders the quarantine summary text and recent entries.
  - Live status proof was refreshed from the running service into `/tmp/cpo_status_t34.json`, keeping the slice grounded in the same operator endpoint that the dashboard consumes.
- Artifacts
  - `/tmp/cpo_status_t34.json`
- Notes
  - The cleanup implementation already existed in the dirty tree; this slice finished the governance work by locking the move-to-quarantine behavior and operator visibility with focused tests, then re-pointing the next successor at landing-surface cleanup visibility instead of more backend cleanup policy.

### 2026-03-24T08:34:02Z | REPO-CPO-ALIGN-P1-T37
- Commands
  - `gofmt -w frontend_setup_scripts_test.go`
  - `go test -count=1 -run 'TestLocalOperatorGeminiSeatAddStoresManagedSeat|TestLocalOperatorGeminiSeatAddMarksUnauthorizedSeatDead|TestLocalOperatorGeminiSeatAddIgnoresProvidedRuntimeState|TestLocalOperatorGeminiSeatAddRejectsNullAuthJSON|TestLocalOperatorGeminiOAuthStartAllowsLoopbackWithoutAdminHeader|TestLocalOperatorGeminiOAuthCallbackStoresManagedSeat|TestManagedGeminiOAuthCallbackRejectsExpiredState|TestManagedGeminiRedirectURIPreservesLoopbackFamily|TestServeStatusPageIncludesOperatorActionForLocalLoopback|TestServeStatusPageHidesOperatorActionOutsideLoopback|TestLocalOperatorGemini|TestServeStatusPage|TestServeFriendLanding_LocalTemplateIncludesCodexOAuthAction' ./...`
- Result
  - PASS
  - The targeted Gemini operator tests are now green end-to-end, including the callback-expiry and loopback-host-family guards that keep the OAuth route/browser trust contract deterministic.
  - The local landing page now exposes real Gemini operator actions instead of a dead-end note: `Start Gemini OAuth`, the manual `Open OAuth Page` fallback, the raw `oauth_creds.json` fallback textarea, and the same automatic reload-on-success behavior used for the other local operator flows.
  - The landing-page Gemini onboarding copy and `README.md` now agree on the actual contract: use `/` or `/status` for managed OAuth onboarding, and use pasted `oauth_creds.json` only as an explicit fallback instead of copying pooled seat files into `~/.gemini/`.
- Artifacts
  - targeted test output captured in terminal only
- Notes
  - This closes the Gemini operator/dashboard/docs slice that was left after `T36`; the next truthful successor is `REPO-CPO-BUG-P2-T34` for long-dead seat cleanup and visible quarantine truth.

### 2026-03-24T07:12:00Z | REPO-CPO-BUG-P1-T33
- Commands
  - `gofmt -w main.go pool.go pool_test.go codex_models.go codex_models_test.go`
  - `go test -count=1 -run 'Test(PeekCandidateDoesNotClaimActiveCodexSeat|CodexWarmState|ServeCodexModels)' ./...`
  - `go test -count=1 -run 'Test(BuildWhamUsageURLKeepsBackendAPI|CodexProviderUpstreamURLBackendAPIPathUsesWhamBase|CodexProviderNormalizePathBackendAPIPathStripsPrefix|StatusJSONIncludesUsageRouting|StatusJSONIncludesAPIKeyStats)' ./...`
  - `go test -count=1 -run 'TestRestorePersistedUsageState|TestCodexWarmState|TestServeCodexModels|TestPeekCandidateDoesNotClaimActiveCodexSeat' ./...`
  - `go test -count=1 -run 'TestProxyBufferedRetryable5xxRetriesNextSeat' ./...`
  - `go build -o /home/lap/.local/bin/codex-pool .`
  - `systemctl --user restart codex-pool.service`
  - `curl -fsS http://127.0.0.1:8989/healthz`
  - `curl -fsS http://127.0.0.1:8989/status?format=json > /tmp/cpo_status_t33.json`
  - `curl -sS -D /tmp/codex-models-headers.txt -o /tmp/codex-models-body.json --max-time 15 -H 'Authorization: Bearer <pool-codex-access-token>' 'http://127.0.0.1:8989/backend-api/codex/models?client_version=0.106.0'`
  - `curl -sS -D /tmp/codex-models-headers-2.txt -o /tmp/codex-models-body-2.json --max-time 15 -H 'Authorization: Bearer <pool-codex-access-token>' 'http://127.0.0.1:8989/backend-api/codex/models?client_version=0.106.0'`
  - `curl -sS -D /tmp/codex-responses-headers-6.txt -o /tmp/codex-responses-body-6.txt --max-time 45 -H 'Authorization: Bearer <pool-codex-access-token>' -H 'Content-Type: application/json' -X POST http://127.0.0.1:8989/v1/responses --data '{"model":"gpt-5.4","instructions":"You are a concise assistant. Reply with OK only.","input":[{"role":"user","content":[{"type":"input_text","text":"Reply with OK only."}]}],"store":false,"stream":true}'`
  - `journalctl --user -u codex-pool.service -n 20 --no-pager`
- Result
  - PASS
  - `peekCandidate` no longer steals the active Codex lease when metadata code only needs a read-only candidate, so `/backend-api/codex/models` refreshes stop mutating `activeCodexID` or reshuffling the next real seat.
  - Pooled Codex traffic now gets a bounded soft warm-gate: for up to `30s` after startup, requests that need a live Codex seat are rejected with `503` while local seat usage snapshots are still cold. The metadata GET path is intentionally excluded from that barrier.
  - `/backend-api/codex/models` now uses a local cache with `1h` fresh TTL, `24h` stale-serve TTL, and a `10s` upstream fetch timeout. Live proof on the restarted service showed `X-Codex-Models-Cache: refresh` on the first request and `X-Codex-Models-Cache: hit` on the second request with `total=0.000571`.
  - The restarted service came up with persisted usage state already restored (`totals=16 snapshots=14 bridged_from_totals=0` in `journalctl`), so the new warm-gate is guarding truly cold seats rather than compensating for restart amnesia.
  - Live pooled Codex request proof passed on the restarted service: `POST /v1/responses` returned HTTP `200` and the captured SSE stream finished with `response.completed` and assistant text `OK`.
  - The live smoke also locked the current upstream request contract for this lane: `input` must be a list, `store` must be `false`, `stream` must be `true`, and `max_output_tokens` is rejected on this ChatGPT-backed Codex endpoint.
- Artifacts
  - `/tmp/cpo_status_t33.json`
  - `/tmp/codex-models-headers.txt`
  - `/tmp/codex-models-headers-2.txt`
  - `/tmp/codex-models-body.json`
  - `/tmp/codex-responses-headers-6.txt`
  - `/tmp/codex-responses-body-6.txt`
- Notes
  - The first T33 test run failed only because `codex_models_test.go` still used the old `NewProviderRegistry` test scaffolding. The test harness was updated to instantiate the current Codex, Claude, and Gemini providers before the real T33 verification run.
  - Cross-check against the operator microaudit: persisted usage snapshots plus `Totals -> Usage` bridge were already landed in `T32`; this slice closed the remaining warm-gate and local models-cache items. Long-dead seat quarantine stays open as successor `REPO-CPO-BUG-P2-T34`.

### 2026-03-24T07:05:00Z | REPO-CPO-PLAN-P1-T35
- Commands
  - `git status --short --branch`
  - `git diff --stat -- README.md frontend_setup_scripts_test.go router.go status.go status_dashboard_test.go templates/local_landing.html provider_gemini.go gemini_operator.go provider_gemini_test.go`
  - `git diff -- README.md frontend_setup_scripts_test.go router.go status.go status_dashboard_test.go templates/local_landing.html provider_gemini.go gemini_operator.go provider_gemini_test.go | sed -n '1,260p'`
- Result
  - PASS
  - The pre-existing dirty tree was classified into two real alignment tracks instead of being left as one mixed tail: a Gemini provider/runtime slice (`provider_gemini.go`, `gemini_operator.go`, `provider_gemini_test.go`, connected runtime state) and an operator/dashboard/docs slice (`status.go`, `router.go`, `templates/local_landing.html`, `README.md`, frontend/status tests).
  - Specialist audit findings were promoted into bounded successor cards with explicit verify hooks instead of vague cleanup language. The main risks now on record are: Gemini refresh fallback stops too early on `400 invalid_grant` / `invalid_client`, Gemini OAuth state TTL is not enforced end-to-end, loopback redirect handling is inconsistent across `127.0.0.1` / `localhost` / `::1`, popup/manual-open refresh is weaker than the Codex flow, and the Gemini fallback copy path text is currently wrong.
  - The repo-local board was reordered so the next execution wave is truthful: `REPO-CPO-ALIGN-P1-T36` for Gemini backend/runtime alignment, `REPO-CPO-ALIGN-P1-T37` for Gemini operator/dashboard/docs alignment, then `REPO-CPO-BUG-P1-T33` for remaining Codex cold-start hardening.
- Artifacts
  - specialist audit outputs captured in-session only
- Notes
  - This planning slice intentionally did not modify the old implementation hunks themselves; it only turned the unmanaged tail into governed executable successors after the Codex routing fix was closed.

### 2026-03-24T06:45:00Z | REPO-CPO-BUG-P1-T32
- Commands
  - `gofmt -w main.go pool.go usage_tracking.go response_usage_stream.go usage_state.go pool_test.go usage_state_test.go response_usage_stream_test.go storage.go sse.go`
  - `go test -count=1 -run 'TestRestorePersistedUsageState.*|TestWrapUsageInterceptWriterAppliesCodexSnapshot|TestCandidateKeepsActiveCodexSeatWhileEligible|TestCandidateDropsActiveCodexSeatAtExactPrimaryThreshold|TestCandidateActiveManagedAPIFallbackDoesNotStealEligibleCodexSeat|TestRoutingStateBlocksExactTenPercentHeadroom|TestCandidateStopsReusingMostRecentlyUsedSeatAtExactPrimaryThreshold' ./...`
  - `go test -count=1 -run 'TestReloadAccountsPreservesRuntimeState|TestCodexProviderParseUsageHeaders|TestParseCodexUsageDelta.*|TestUpdateUsageFromBody.*|TestSSEInterceptWriterEventCallbackReceivesNonUsageEvents|TestCandidate.*|TestRoutingState.*|TestBuildPoolDashboardDataSelectsCurrentSeatFromInflightAndLastUsed|TestBuildPoolDashboardDataSeparatesLastUsedAndBestEligibleWhenIdle' ./...`
  - `go build ./...`
  - `go build -o /home/lap/.local/bin/codex-pool .`
  - `systemctl --user show -p MainPID,ExecMainStartTimestamp,ActiveState,SubState codex-pool.service`
  - `curl -fsS http://127.0.0.1:8989/healthz`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_post_restart_usage_fix.json`
  - `curl -fsS http://127.0.0.1:8989/config/codex/<pool-user-token> >/tmp/cpo_pool_user_auth.json`
  - `timeout 60s curl -sS -N -o /tmp/cpo_live_usage_fix_smoke.sse -w '%{http_code}' http://127.0.0.1:8989/responses -H 'Authorization: Bearer <pool-codex-access-token>' -H 'Content-Type: application/json' --data '{"model":"gpt-5.4","instructions":"Reply with exactly OK.","store":false,"stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}'`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_post_smoke_usage_fix.json`
  - `journalctl --user -u codex-pool.service -n 40 --no-pager | tail -n 20`
- Result
  - PASS
  - Cold-start state is now restored through the new persisted usage snapshot bucket plus the `Totals -> Usage` bridge, instead of restoring only aggregate totals. After the live `systemd` restart at `Tue 2026-03-24 02:40:30 EDT`, `journalctl` recorded `restored usage state from disk: totals=15 snapshots=12 bridged_from_totals=1`.
  - Streamed Codex `token_count` events now count as usage-bearing SSE events, update live `a.Usage`, and persist the refreshed snapshot instead of only updating `Totals.Last*Pct`.
  - Codex seat selection now keeps an explicit active seat for new unpinned work until routing becomes ineligible at the existing `>=90%` headroom guards; managed OpenAI API fallback no longer steals traffic while an eligible local Codex seat exists.
  - The targeted restore/SSE/routing tests and the broader adjacent regression suite both passed, and `go build ./...` plus the deploy build to `/home/lap/.local/bin/codex-pool` completed successfully.
  - Live service proof passed on the restarted service: `curl /healthz` returned `{"status":"ok","uptime":"3m"}`, the pool-routed `/responses` smoke returned HTTP `200`, and the captured SSE stream finished with assistant text `OK`.
  - Post-smoke live status remained coherent: `last_used_seat.id == "luka_3"` immediately after the smoke with updated headroom (`primary_headroom_pct=94`, `secondary_headroom_pct=98`), showing the request ran through a local Codex seat on the restarted process.
- Artifacts
  - `/tmp/cpo_status_post_restart_usage_fix.json`
  - `/tmp/cpo_status_post_smoke_usage_fix.json`
  - `/tmp/cpo_live_usage_fix_smoke.sse`
- Notes
  - The service was already running the new binary under `systemd` during verification (`MainPID=26994`); the earlier “temporary” launch attempt ended up coinciding with the real managed restart window, so verification was completed on the production-managed process rather than a sidecar listener.
  - This slice intentionally stopped before startup warm-gating, local `/backend-api/codex/models` caching, and dead-seat quarantine; those successors are now hydrated as `REPO-CPO-BUG-P1-T33` and `REPO-CPO-BUG-P2-T34`.

### 2026-03-23T17:45:16Z | REPO-CPO-REFAC-P1-T30
- Commands
  - `go test -count=1 -timeout 120s -run 'TestFinalizeWebSocketSuccessState.*|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestProxyWebSocketPoolAcceptsAuthFromSubprotocol|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketMarksDeactivatedCodexAccountDeadAndFallsThroughNextSeat|TestProxyWebSocketPassthroughPreservesAuthorization' ./...`
- Result
  - PASS
  - `proxyRequestWebSocket` now delegates the pooled websocket reverse-proxy execution shell to `servePooledWebSocketProxy`, so rewrite/error/status capture wiring no longer lives as one large inline literal in the main pooled handler.
  - Pooled websocket semantics stayed fixed: auth overwrite, subprotocol bearer replacement, response status capture, error handling, metrics accounting, and debug logs all remained green under focused websocket coverage.
  - This slice shrank the last large pooled websocket shell without merging pooled and passthrough behavior yet.
- Artifacts
  - live command output captured in terminal only
- Notes
  - No service restart was needed for this slice: the change is bounded to repo-local websocket reverse-proxy refactoring.
  - The next truthful successor is `REPO-CPO-REFAC-P1-T31`, which can share the remaining common websocket execution shell between pooled and passthrough lanes.

### 2026-03-23T17:42:38Z | REPO-CPO-REFAC-P1-T29
- Commands
  - `go test -count=1 -timeout 120s -run 'TestInspectResponseBodyForClassification|TestFinalizeWebSocketSuccessState.*|TestApplyPreCopyUpstreamStatusDisposition|TestProxyStreamedManagedAPI(5xxPreservesFullErrorBody|5xxDoesNotWaitForFullLargeBody|Compressed429ClassifiesQuotaAndPreservesBody|Compressed429ClassifiesQuotaAfterShortFirstReads)$|TestProxyWebSocket(PoolRewritesAuthAndPinsSession|ManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback|ManagedAPICompressed429ClassifiesQuotaAndPreservesBody|MarksDeactivatedCodexAccountDeadAndFallsThroughNextSeat)$' ./...`
- Result
  - PASS
  - Streamed and websocket pre-copy response handling now share `applyPreCopyUpstreamStatusHandling`, so retryable-status inspection, raw-body replay, and status disposition no longer sit half-inline in both call sites.
  - Path-specific behavior stayed explicit: websocket still treats `101 Switching Protocols` as a no-op for pre-copy disposition, while streamed still owns the extra non-managed `401/403` diagnostic log and still passes `needStatusBody` into copied-response delivery for early flush behavior.
  - Focused streamed + websocket parity coverage stayed green after the extraction, including gzip short-read classification and full error-body replay on both lanes.
- Artifacts
  - live command output captured in terminal only
- Notes
  - No service restart was needed for this slice: the change is bounded to repo-local pre-copy response refactoring.
  - The next truthful successor is `REPO-CPO-REFAC-P1-T30`, which can shrink the remaining large pooled websocket reverse-proxy shell without reopening transport semantics.

### 2026-03-23T17:40:30Z | REPO-CPO-REFAC-P1-T28
- Commands
  - `go test -count=1 -timeout 120s -run 'TestFinalizeWebSocketSuccessState.*|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketMarksDeactivatedCodexAccountDeadAndFallsThroughNextSeat|TestApplyPreCopyUpstreamStatusDisposition' ./...`
- Result
  - PASS
  - `proxyRequestWebSocket` now hands the remaining response-side `ModifyResponse` contour to `modifyWebSocketProxyResponse`, so usage-header parsing, pre-copy status handling, and websocket success finalization no longer live inline inside the reverse-proxy literal.
  - Websocket semantics stayed fixed: failed handshakes still do not pin sessions or update `LastUsed`, `101` success still pins the request conversation, and raw error-body replay still survives managed fallback paths.
  - This keeps websocket on its own lane while removing the last large inline response contour before broader reverse-proxy cleanup.
- Artifacts
  - live command output captured in terminal only
- Notes
  - No service restart was needed for this slice: the change is bounded to repo-local websocket response refactoring.
  - The next truthful successor is `REPO-CPO-REFAC-P1-T29`, which can share the still-duplicated pre-copy status handling between streamed and websocket paths.

### 2026-03-23T17:37:00Z | REPO-CPO-REFAC-P1-T27
- Commands
  - `go test -count=1 -timeout 120s -run 'TestFinalizeProxyResponse|TestFinalizeWebSocketSuccessState.*|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketMarksDeactivatedCodexAccountDeadAndFallsThroughNextSeat' ./...`
- Result
  - PASS
  - `finalizeProxyResponse` and `finalizeWebSocketSuccessState` now share `applySuccessfulAccountStateLocked`, so managed API recovery, `LastUsed`, and penalty decay no longer live in two parallel success-state blocks.
  - Path-specific behavior stayed explicit: copied-response finalization still owns body-derived conversation pinning and GitLab Claude persistence/reset semantics, while websocket finalization still only pins the request conversation and never picks up the GitLab persistence side effects.
  - Focused finalizer plus websocket parity coverage stayed green after the extraction, so the shared success-state helper did not reopen the earlier websocket failure/pinning guarantees.
- Artifacts
  - live command output captured in terminal only
- Notes
  - No service restart was needed for this slice: the change is bounded to repo-local success-state refactoring.
  - The next truthful successor is `REPO-CPO-REFAC-P1-T28`, which can extract the remaining inline websocket `ModifyResponse` contour now that both status disposition and success recovery are explicit seams.

### 2026-03-23T17:35:04Z | REPO-CPO-REFAC-P1-T26
- Commands
  - `go test -count=1 -timeout 120s -run 'TestFinalizeWebSocketSuccessStateRecoversManagedAPIAccountOnNonSwitching2xx|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketMarksDeactivatedCodexAccountDeadAndFallsThroughNextSeat|TestApplyPreCopyUpstreamStatusDisposition' ./...`
  - `go test -count=1 -timeout 120s -run 'TestFinalizeWebSocketSuccessStateRecoversManagedAPIAccountOnNonSwitching2xx|TestProxyWebSocket.*' ./...`
- Result
  - PASS
  - `proxyRequestWebSocket` now hands the remaining success-side mutation to `finalizeWebSocketSuccessState`, so session pinning, managed API recovery, `LastUsed`, and penalty decay no longer live inline inside `ModifyResponse` after the pre-copy status disposition seam.
  - Websocket semantics stayed fixed: failed handshakes still do not pin sessions or update `LastUsed`, `101` success still pins the request conversation, and non-`101` `2xx` success remains treated as recovery for managed API accounts.
  - Added a focused unit lock for the previously untested non-`101` `2xx` recovery branch, so the refactor now proves websocket success recovery without leaning only on the raw-handshake integration tests.
- Artifacts
  - live command output captured in terminal only
- Notes
  - No service restart was needed for this slice: the change is bounded to repo-local websocket success-state refactoring.
  - The next truthful successor is `REPO-CPO-REFAC-P1-T27`, which can extract the still-duplicated managed-account success recovery shared by `finalizeProxyResponse` and `finalizeWebSocketSuccessState`.

### 2026-03-23T17:29:39Z | REPO-CPO-REFAC-P1-T25
- Commands
  - `go test -count=20 -timeout 120s -run 'TestProxyBuffered.*|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback' ./...`
  - `go test -count=1 -timeout 120s -run "TestApplyPreCopyUpstreamStatusDisposition|TestInspectResponseBodyForClassification|TestInspectBufferedRetryBody|TestProxyBuffered.*|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback" ./...`
  - `go test -race -count=1 -timeout 120s -run 'TestProxyBufferedManagedAPI429RetriesNextSeatAfterQuotaFallback|TestProxyBufferedRetryable5xxRetriesNextSeat|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback' ./...`
- Result
  - PASS
  - Buffered and streamed success-delivery helpers now share one copied-response transport core, so header copy, usage-header replacement, SSE writer setup, idle-timeout wiring, body copy, and `finalizeCopiedProxyResponse` entry no longer live in two nearly identical helpers.
  - The shared helper still preserves the deliberate mode differences explicitly through options: buffered keeps `sampleBuf` reuse, `conversationID`, and explicit `resp.Body.Close()`, while streamed keeps tee sampling, empty pin input, and the early non-SSE flush after inspected status bodies.
  - Mixed buffered/streamed repeat coverage, the canonical shared verify hook, and a focused race pass all stayed green after the unification, so the shared transport core did not reopen the prior buffered retry or streamed gzip work.
- Artifacts
  - live command output captured in terminal only
- Notes
  - No service restart was needed for this slice: the change is bounded to repo-local copied-response delivery refactoring.
  - The next truthful successor is `REPO-CPO-REFAC-P1-T26`, which can extract the remaining websocket success-state finalizer now that buffered/streamed delivery has a shared core.

### 2026-03-23T17:25:57Z | REPO-CPO-REFAC-P1-T24
- Commands
  - `go test -count=20 -timeout 120s -run 'TestProxyBuffered.*|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback' ./...`
  - `go test -count=1 -timeout 120s -run "TestApplyPreCopyUpstreamStatusDisposition|TestInspectResponseBodyForClassification|TestInspectBufferedRetryBody|TestProxyBuffered.*|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback" ./...`
  - `go test -race -count=1 -timeout 120s -run 'TestProxyBufferedManagedAPI429RetriesNextSeatAfterQuotaFallback|TestProxyBufferedRetryable5xxRetriesNextSeat|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback' ./...`
- Result
  - PASS
  - The streamed success-delivery tail now lives behind `deliverStreamedProxyResponse`, so `proxyRequestStreamed` no longer keeps response header copy, early non-SSE flush, tee/sample wiring, SSE interception, idle-timeout wrapping, and `finalizeCopiedProxyResponse` entry inline after the pre-copy disposition seam.
  - Behavior stayed fixed: the helper still parses usage headers before snapshotting account usage, still preserves the streamed-only early flush for inspected non-SSE status bodies, still uses streamed tee sampling instead of buffered `sampleBuf` reuse, and still calls `finalizeCopiedProxyResponse` with the original streamed arguments, including empty conversation pin input and debug label `streamed done`.
  - A 20x mixed buffered/streamed repeat run, the canonical shared verify hook, and a focused race pass all stayed green after the extraction, so the streamed tail split did not reopen the recent buffered or gzip short-read work.
- Artifacts
  - live command output captured in terminal only
- Notes
  - No service restart was needed for this slice: the change is bounded to repo-local streamed proxy refactoring.
  - The next truthful successor is `REPO-CPO-REFAC-P1-T25`, which can unify the buffered and streamed copied-response delivery core now that both tails are explicit helpers.

### 2026-03-23T17:23:04Z | REPO-CPO-REFAC-P1-T23
- Commands
  - `go test -count=20 -timeout 120s -run 'TestProxyBuffered.*' ./...`
  - `go test -count=1 -timeout 120s -run "TestApplyPreCopyUpstreamStatusDisposition|TestInspectResponseBodyForClassification|TestInspectBufferedRetryBody|TestProxyBuffered.*|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback" ./...`
  - `go test -race -count=1 -timeout 120s -run 'TestProxyBufferedManagedAPI429RetriesNextSeatAfterQuotaFallback|TestProxyBufferedRetryable5xxRetriesNextSeat|TestProxyBufferedGitLabClaude402QuotaExceededRetriesNextSeat|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody' ./...`
- Result
  - PASS
  - The buffered success-delivery tail now lives behind `deliverBufferedAttemptSuccess`, so `proxyRequest` no longer keeps header copy, SSE writer setup, idle-timeout wiring, body copy, and `finalizeCopiedProxyResponse` entry inline after `runBufferedAttemptContour`.
  - Behavior stayed fixed: the helper still uses `sampleBuf` from `tryOnce`, still snapshots usage headers before delivery, still replaces outgoing usage headers, still gates flush/SSE interception/idle-timeout on the same `provider.DetectsSSE(r.URL.Path, respContentType)` check, and still calls `finalizeCopiedProxyResponse` with the original `conversationID` and debug label `done`.
  - Buffered repeat coverage, the canonical shared verify hook, and a focused race run all passed after the extraction, so the helper split did not reopen the buffered retry or Claude/GitLab parity work.
- Artifacts
  - live command output captured in terminal only
- Notes
  - No service restart was needed for this slice: the change is bounded to repo-local buffered proxy refactoring.
  - The next truthful successor is `REPO-CPO-REFAC-P1-T24`, which can extract the analogous streamed success-delivery tail before a later shared copied-response transport slice.

### 2026-03-23T17:02:00Z | REPO-CPO-REFAC-P1-T22
- Commands
  - `go test -count=1 -timeout 120s -run 'TestApplyPreCopyUpstreamStatusDisposition|TestInspectBufferedRetryBody|TestProxyBuffered.*' ./...`
  - `go test -count=1 -timeout 120s -run 'TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback' ./...`
  - `go test -count=1 -timeout 120s -run "TestApplyPreCopyUpstreamStatusDisposition|TestInspectResponseBodyForClassification|TestInspectBufferedRetryBody|TestProxyBuffered.*|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback" ./...`
  - `go test -count=20 -timeout 120s -run 'TestProxyBuffered.*' ./...`
- Result
  - PASS
  - `proxyRequest` now hands the non-stream retry loop to one explicit buffered attempt contour: `runBufferedAttemptContour` owns attempt count, candidate selection, exclusion, inflight bookkeeping, retry continuation, and final failure shaping instead of keeping those branches inline.
  - Retryable buffered status handling now sits behind `applyBufferedRetryDisposition`, which preserves the existing split semantics exactly: ordinary Codex `429` still forwards after penalty/backoff, managed API `429/402` still falls through to fallback/dead-state handling, GitLab Claude `402/401/403` still uses the managed disposition path, and ordinary `401/403/5xx` retries still synthesize the same buffered errors/recent entries.
  - Focused buffered tests, focused streamed/websocket parity checks, the canonical shared verify hook, and a 20x buffered repeat run all passed after the extraction, so the contour shrink did not reopen the earlier scheduler-sensitive buffered suite.
- Artifacts
  - live command output captured in terminal only
- Notes
  - No service restart was needed for this slice: the change is bounded to repo-local buffered proxy refactoring.
  - The next truthful successor is `REPO-CPO-REFAC-P1-T23`, which can extract the remaining buffered success-delivery tail now that attempt selection/retry handling is isolated.

### 2026-03-23T16:40:42Z | REPO-CPO-TEST-P1-T21
- Commands
  - `go test -count=1 -timeout 120s -run 'TestProxyBufferedGitLabClaude402QuotaExceededRetriesNextSeat|TestProxyBufferedGitLabClaude403GatewayRejectedRetriesNextSeat|TestProxyBufferedGitLabClaude401RefreshInvalidGrantMarksDead|TestProxyBufferedGitLabClaude403DirectAccessForbiddenMarksDead|TestApplyPreCopyUpstreamStatusDispositionPreservesDeadGitLabAccount|TestApplyUpstreamAuthFailureDispositionPreservesDeadGitLabAccount' ./...`
  - `go test -count=100 -timeout 120s -run 'TestProxyBufferedRetryable5xxRetriesNextSeat' ./...`
  - `go test -count=20 -shuffle=on -timeout 120s -run "TestApplyPreCopyUpstreamStatusDisposition|TestInspectResponseBodyForClassification|TestInspectBufferedRetryBody|TestProxyBuffered.*|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback" ./...`
  - `go test -count=1 -timeout 120s -run "TestApplyPreCopyUpstreamStatusDisposition|TestInspectResponseBodyForClassification|TestInspectBufferedRetryBody|TestProxyBuffered.*|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback" ./...`
- Result
  - PASS
  - Added direct buffered GitLab Claude retry coverage for non-stream `402 quota_exceeded`, gateway `403` rejection, refresh-invalid `401`, and direct-access-forbidden `403`, closing the last provider-specific buffered retry gap left after `T20`.
  - Hardened GitLab dead-state handling so refresh/direct-access fatal failures are not overwritten by later gateway auth disposition passes; new guardrail tests lock that behavior explicitly.
  - Root-caused the old `TestProxyBufferedRetryable5xxRetriesNextSeat` failure as a test-order/scheduler race around post-copy `LastUsed` updates in `finalizeProxyResponse`, not a buffered routing regression. Stabilized buffered integration assertions with a short eventual wait on successful-account state and then verified the formerly flaky `5xx` test 100x plus the wider shuffled hook 20x.
- Artifacts
  - live command output captured in terminal only
- Notes
  - No service restart was needed for this slice: the change is bounded to retry disposition logic and targeted proxy tests. The next repo-local successor is `REPO-CPO-REFAC-P1-T22`, which can extract the remaining buffered retry attempt contour now that parity is locked.

### 2026-03-23T16:45:00Z | REPO-CPO-TEST-P1-T20
- Commands
  - `go test -count=1 -timeout 120s -run "TestApplyPreCopyUpstreamStatusDisposition|TestInspectResponseBodyForClassification|TestInspectBufferedRetryBody|TestProxyBuffered.*|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback" ./...`
- Result
  - PASS
  - Added direct buffered proxy integration coverage for the non-stream retry loop instead of relying only on helper-level assertions: managed API `429 insufficient_quota` fallback, managed API `402 billing_hard_limit_reached` fallback, ordinary codex `402 deactivated_workspace` failover, transient auth `403` failover, and ordinary `502` failover.
  - The new tests confirm the intended buffered semantics after `T18/T19`: retry branches consume bounded semantic snapshots, move to the next eligible seat when appropriate, mark permanent failures dead, and preserve error/recent bookkeeping without any replay-specific assumptions.
  - Streamed/websocket verification stayed green in the same run, so the new buffered coverage did not reopen the split inspection work.
- Artifacts
  - live command output captured in terminal only
- Notes
  - The main remaining buffered coverage gap is provider-specific Claude/GitLab retry behavior under non-stream `402/401/403` responses. Follow-on card `REPO-CPO-TEST-P1-T21` is hydrated for that surface.

### 2026-03-23T16:32:00Z | REPO-CPO-REFAC-P1-T19
- Commands
  - `go test -count=1 -timeout 120s -run "TestApplyPreCopyUpstreamStatusDisposition|TestInspectResponseBodyForClassification|TestInspectBufferedRetryBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback" ./...`
- Result
  - PASS
  - The non-stream retry loop now takes at most one buffered semantic snapshot per relevant response via `inspectBufferedRetryStatus`, instead of re-reading or reconstructing status bodies independently inside each `402` / managed `429` / generic retryable branch.
  - Shared formatting and gating helpers (`needsBufferedRetryInspection`, `formatBufferedRetryStatusError`) now keep buffered retry branching focused on disposition logic, while streamed/websocket paths stay on the separate `preCopyInspection` + raw replay contract.
  - The refactor stays bounded: no change to streamed/websocket replay behavior, no change to the T12 gzip short-read fix, and no new publish/deploy side effects.
- Artifacts
  - live command output captured in terminal only
- Notes
  - Remaining risk is coverage depth, not logic shape: helper-level tests are in place, but direct buffered retry integration tests still lag behind streamed/websocket parity. Follow-on card `REPO-CPO-TEST-P1-T20` is hydrated for that gap.

### 2026-03-23T16:18:00Z | REPO-CPO-REFAC-P1-T18
- Commands
  - `go test -count=1 -timeout 120s -run "TestApplyPreCopyUpstreamStatusDisposition|TestInspectResponseBodyForClassification|TestInspectBufferedRetryBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback" ./...`
- Result
  - PASS
  - Removed the dead `inspectAndReplayResponseBody` compatibility shim and replaced the buffered retry loop's inline `ReadAll + bodyForInspection` pattern with one explicit `inspectBufferedRetryBody` helper, so the buffered path now declares its own contract instead of looking like a partial consumer of the streamed/websocket replay API.
  - Streamed and websocket non-`101` paths still use `preCopyInspection` with explicit raw replay; buffered retries now document the opposite choice clearly: they only need a bounded semantic snapshot because the upstream body is never rewound back to the client in that loop.
  - Focused unit coverage now locks both sides of the split: `inspectResponseBodyForClassification` for replay-sensitive paths and `inspectBufferedRetryBody` for the fully buffered retry path.
- Artifacts
  - live command output captured in terminal only
- Notes
  - Residual simplification remains in the buffered retry branch structure itself: status-specific handling is still spread across several `402/429/retryable` cases even though they now share one explicit inspection primitive. Follow-on card `REPO-CPO-REFAC-P1-T19` is hydrated for that cleanup.

### 2026-03-23T15:30:00Z | REPO-CPO-REFAC-P1-T17
- Commands
  - `go build ./...`
  - `go test -count=1 -timeout 120s -run "TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback|TestApplyPreCopyUpstreamStatusDisposition|TestInspectResponseBodyForClassification|TestInspectAndReplayResponseBody" ./...`
- Result
  - PASS
  - Replaced the coupled `inspectResponseBodyPrefix(resp, limit) []byte` with a two-return-value `inspectResponseBodyForClassification(resp, limit) preCopyInspection` that separates semantic error classification (`Inspected` — always decoded plaintext) from transport body replay (`RawPrefix` — always raw wire bytes). Callers now explicitly replay via `replayResponseBody(rawPrefix, resp.Body)` instead of the function silently mutating `resp.Body` as a side effect.
  - Both the streamed (`proxyRequestStreamed`) and websocket (`proxyRequestWebSocket` `ModifyResponse`) non-`101` paths now use the split API: they inspect the decoded body for classification/logging and then replay the raw prefix for exact client-visible body preservation.
  - The T12 gzip short-read fix is preserved: `inspectGzipResponseBodyPrefix` still uses the bounded progressive raw-prefix read loop, and the new split just wraps it cleanly.
  - Added `inspectAndReplayResponseBody` convenience wrapper that preserves the old one-call-does-both behavior for any paths that don't need the split.
  - New focused tests lock the separation contract: plaintext inspection returns identical `Inspected` and `RawPrefix`, gzip inspection returns decoded `Inspected` and raw gzip `RawPrefix`, and `inspectResponseBodyForClassification` does not automatically replay into `resp.Body`.
- Artifacts
  - live command output captured in terminal only
- Notes
  - The `inspectAndReplayResponseBody` wrapper is currently unused but kept as a stable convenience for the buffered path or future callers. The buffered retry loop in `proxyRequest` still uses its own inline `bodyForInspection` path and was not touched in this bounded refactor.

### 2026-03-23T14:36:27Z | REPO-CPO-BUG-P1-T12
- Commands
  - `go test -count=1 -timeout 120s -run 'TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429DoesNotWaitForFullLargeBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback' ./...`
  - `go test -count=1 -timeout 120s -run 'TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429DoesNotWaitForFullLargeBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback|TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestApplyPreCopyUpstreamStatusDisposition' ./...`
- Result
  - PASS
  - `inspectGzipResponseBodyPrefix` now uses a bounded progressive raw-prefix read loop instead of trusting a single first `Read`, so managed API quota/auth markers remain classifiable when gzip headers or early deflate bytes arrive across multiple short transport reads.
  - The helper still stops early as soon as it has enough decoded prefix or a classifier-relevant signal, so the existing “do not wait for the delayed second half of a large gzip body” behavior remains intact.
  - Shared streamed + websocket non-`101` coverage is now explicit: new regression tests lock short-first-read streamed `429 insufficient_quota` handling and gzip-backed websocket fallback parity while preserving client-visible bodies.
- Artifacts
  - live command output captured in terminal only
- Notes
  - Residual architectural debt remains real: pre-copy status inspection still couples semantic classification and transport replay through one helper contract. Follow-on card `REPO-CPO-REFAC-P1-T17` is hydrated to separate those responsibilities without reopening the fixed gzip regression.

### 2026-03-23T13:36:00Z | REPO-CPO-REFAC-P1-T16
- Commands
  - `go test -count=1 -run 'TestClaudeProviderLoadsGitLabManagedAccount|TestClaudeProviderRefreshGitLabManagedAccount|TestClassifyManagedGitLabClaudeErrorQuotaExceeded|TestBuildPoolDashboardDataShowsGitLabDirectAccessSignals|TestBuildPoolDashboardDataBlocksGitLabTokensMissingGatewayState|TestServeStatusPageReturnsJSONForFormatQuery|TestApplyPreCopyUpstreamStatusDispositionGitLabQuotaExceededPersistsCooldown|TestApplyPreCopyUpstreamStatusDispositionGitLabQuotaExceededBackoffEscalates|TestFinalizeProxyResponseResetsGitLabQuotaBackoffAfterSuccess|TestRefreshAccountOnceGitLabBypassesPerAccountThrottle|TestSaveGitLabClaudeAccountFailsClosedOnMalformedJSON|TestSaveGitLabClaudeAccountRoundTripsGitLabFields|TestRefreshGitLabClaudeAccessMalformed2xxMarksErrorAndClearsGatewayState' ./...`
  - `go build -o /home/lap/.local/bin/codex-pool .`
  - `systemctl --user restart codex-pool.service`
  - `curl -fsS http://127.0.0.1:8989/healthz`
  - `curl -fsS http://127.0.0.1:8989/status?format=json`
  - `python3 /home/lap/tools/codex_pool_manager.py status --strict | jq '.admin_accounts[] | select(.type=="claude" and .plan_type=="gitlab_duo") | {id,health_status,gitlab_quota_exceeded_count,gitlab_last_quota_exceeded_at,last_refresh,expires_at,routing}'`
- Result
  - PASS
  - Managed GitLab Claude persistence is now canonical and fail-closed: `saveManagedGitLabClaudeToken` and `saveGitLabClaudeAccount` both flow through one serializer that preserves unknown top-level fields, persists `last_refresh`, and refuses to rewrite malformed existing JSON.
  - GitLab refresh truth is stricter: malformed `200 OK` direct-access responses now stamp `health_status=error`, preserve a concrete `health_error`, and clear stale gateway auth state instead of leaving the token looking healthy/usable after a failed refresh.
  - Routing/status truth is tighter: GitLab Claude accounts with missing gateway token/headers are now blocked with `block_reason=missing_gateway_state`, so `/status?format=json`, pool counters, and `next_token_id` can no longer advertise unusable tokens as eligible.
  - `/status` and `/admin/accounts` now snapshot account state under short locks, use atomic inflight reads, and avoid zero-time noise for the GitLab account timestamps exported through the admin surface.
  - Live restart on PID `78244` succeeded; `/healthz` returned `{"status":"ok","uptime":"0s"}` immediately after the final restart, and live `/status?format=json` showed `gitlab_claude_pool.total_tokens=4`, `eligible_tokens=3`, with exhausted token `claude_gitlab_457e812b181e` still blocked as `rate_limited` and healthy tokens remaining eligible.
- Artifacts
  - live command output captured in terminal only
- Notes
  - The existing exhausted live token still shows `gitlab_last_quota_exceeded_at=null` because its on-disk JSON predates the new field and has not yet been re-persisted through a fresh quota event or healthy recovery cycle; backward-compatible `gitlab_quota_exceeded_count=1` inference still keeps runtime routing truthful.

### 2026-03-23T10:31:00Z | REPO-CPO-BUG-P1-T16
- Commands
  - `go test -count=1 -run 'TestClaudeProviderLoadsLegacyQuotaExceededAccountWithDefaultBackoffCount|TestClaudeProviderParseUsageSupportsNonStreamMessagePayload|TestUpdateUsageFromBodyRecordsClaudeNonStreamMessage|TestClaudeProviderLoadsGitLabManagedAccount|TestClaudeProviderSetAuthHeadersForGitLabManagedAccount|TestClaudeProviderRefreshGitLabManagedAccount|TestProviderUpstreamURLForGitLabManagedAccount|TestNeedsRefreshWhenGitLabClaudeGatewayStateMissing|TestClassifyManagedGitLabClaudeErrorQuotaExceeded|TestClassifyManagedGitLabClaudeGatewayForbiddenDoesNotMarkDead|TestClassifyManagedGitLabClaudeDirectAccessForbiddenMarksDead|TestGitLabClaudeQuotaExceededCooldownExpandsExponentially|TestApplyPreCopyUpstreamStatusDispositionGitLabQuotaExceededPersistsCooldown|TestApplyPreCopyUpstreamStatusDispositionGitLabQuotaExceededBackoffEscalates|TestFinalizeProxyResponseResetsGitLabQuotaBackoffAfterSuccess|TestRefreshAccountOnceGitLabBypassesPerAccountThrottle|TestBuildPoolDashboardDataShowsGitLabDirectAccessSignals' ./...`
  - `go build -o /home/lap/.local/bin/codex-pool .`
  - `systemctl --user restart codex-pool.service && sleep 2 && curl -fsS http://127.0.0.1:8989/healthz`
  - `python3 /home/lap/tools/codex_pool_manager.py status | jq '.admin_accounts[] | select(.type=="claude" and .plan_type=="gitlab_duo") | {id,health_status,health_error,gitlab_quota_exceeded_count,gitlab_last_quota_exceeded_at,routing}'`
  - `curl -fsS http://127.0.0.1:8989/status?format=json | jq '.accounts[] | select(.plan_type=="gitlab_duo") | {id,health_status,gitlab_quota_exceeded_count,gitlab_quota_probe_in,routing}'`
  - `timeout 120s fish -lc 'claude --model sonnet -p "Reply with exactly OK."'`
- Result
  - PASS
  - Replaced the fixed GitLab `quota_exceeded` cooldown with adaptive exponential backoff: `30m -> 1h -> 2h -> 4h -> 8h -> 24h cap`, persisted per token as `gitlab_quota_exceeded_count`.
  - Successful Claude message responses now clear GitLab quota backoff state and persist the healthy state back to disk; `direct_access` refresh still does not falsely clear monthly exhaustion by itself.
  - Added backward-compatible load behavior so legacy exhausted GitLab account files that only had `health_status=quota_exceeded` plus `rate_limit_until` are surfaced as backoff level `1` after restart instead of looking like a zero-count state.
  - `/status?format=json` now exposes `gitlab_quota_exceeded_count` and `gitlab_quota_probe_in`, and `/admin/accounts` now exposes GitLab health/error plus quota-backoff count.
  - After deploy and restart, health probe returned `{"status":"ok","uptime":"1s"}`, the exhausted token `claude_gitlab_457e812b181e` surfaced as `health_status=quota_exceeded`, `gitlab_quota_exceeded_count=1`, `gitlab_quota_probe_in="5.4h"`, and the real `fish -> claude --model sonnet -p ...` smoke still returned `OK.`.
- Artifacts
  - live command output captured in terminal only
- Notes
  - The new backoff count for the currently exhausted legacy token comes from backward-compatible load inference because the original on-disk record predated the new counter field.

### 2026-03-23T09:53:01Z | REPO-CPO-BUG-P1-T15
- Commands
  - `python3 - <<'PY' ... direct_access -> /v1/messages ... PY`
  - `go test -count=1 -run 'TestClaudeProviderParseUsageSupportsNonStreamMessagePayload|TestUpdateUsageFromBodyRecordsClaudeNonStreamMessage|TestClaudeProviderLoadsGitLabManagedAccount|TestClaudeProviderSetAuthHeadersForGitLabManagedAccount|TestClaudeProviderRefreshGitLabManagedAccount|TestProviderUpstreamURLForGitLabManagedAccount|TestNeedsRefreshWhenGitLabClaudeGatewayStateMissing|TestClassifyManagedGitLabClaudeErrorQuotaExceeded|TestClassifyManagedGitLabClaudeGatewayForbiddenDoesNotMarkDead|TestClassifyManagedGitLabClaudeDirectAccessForbiddenMarksDead|TestApplyPreCopyUpstreamStatusDispositionGitLabQuotaExceededPersistsCooldown|TestRefreshAccountOnceGitLabBypassesPerAccountThrottle' ./...`
  - `go build -o /home/lap/.local/bin/codex-pool .`
  - `systemctl --user restart codex-pool.service`
  - `curl -fsS http://127.0.0.1:8989/healthz`
  - `POOL_USER_TOKEN=$(jq -r '.[0].token' /home/lap/.root_layer/codex_pool/data/pool_users.json) && CLAUDE_POOL_TOKEN=$(curl -fsS --max-time 15 "http://127.0.0.1:8989/config/claude/${POOL_USER_TOKEN}" | jq -r '.access_token') && curl -sS --max-time 90 -D - -X POST http://127.0.0.1:8989/v1/messages -H "Authorization: Bearer ${CLAUDE_POOL_TOKEN}" -H 'Content-Type: application/json' -H 'anthropic-version: 2023-06-01' --data '{"model":"claude-sonnet-4-20250514","max_tokens":64,"messages":[{"role":"user","content":"Reply with exactly OK"}]}'`
  - `timeout 120s fish -lc 'claude --model sonnet -p "Reply with exactly OK."'`
  - `python3 /home/lap/tools/codex_pool_manager.py status | jq '.admin_accounts[] | select(.type=="claude" and .plan_type=="gitlab_duo") | {id,dead,health_status,health_error,routing,totals}'`
- Result
  - PASS
  - Live truth check still showed the underlying token split: `claude_gitlab_457e812b181e` returned GitLab gateway `402` with `USAGE_QUOTA_EXCEEDED`, while `claude_gitlab_8d2aa7ac125f` returned real `200 OK`.
  - Root cause was inside orchestrator routing, not GitLab uptime: message-path GitLab `402` was never routed through fallback, and message-path `403` reused the same `dead` semantics as direct-access token failures. That let transient/stale gateway rejects poison the whole Claude lane, and the 15-minute per-account refresh throttle blocked fresh `direct_access` minting exactly when the retry path needed it.
  - Fixes in this slice:
    - GitLab message-path `402 Payment Required` now goes through managed GitLab disposition handling, gets persisted as `rate_limit_until`, and rotates to the next candidate instead of surfacing as terminal pool failure.
    - GitLab message-path `401/403` is now treated as temporary gateway rejection with cooldown instead of hard `dead`; only direct-access `401/403` still marks the source token dead.
    - GitLab managed accounts now persist `rate_limit_until` to disk, so `quota_exceeded` exclusion survives service restart instead of disappearing from runtime truth.
    - GitLab `direct_access` refresh bypasses the generic 15-minute per-account refresh throttle, so a rejected gateway token can be reminted immediately on the same-account retry path while still respecting the global refresh throttle.
  - After deploy and restart, live pool smoke against `POST /v1/messages` returned HTTP `200` with assistant text `OK`, and the real `fish -> claude --model sonnet -p ...` path also returned `OK.`.
  - Post-smoke pool state confirmed intended fallback behavior: `claude_gitlab_457e812b181e` stayed `dead=false` but became ineligible with `block_reason=rate_limited`, while `claude_gitlab_8d2aa7ac125f` remained eligible and its local totals incremented to `request_count=2`.
- Artifacts
  - live command output captured in terminal only
- Notes
  - `python3 /home/lap/tools/codex_pool_manager.py status` still shows `health_status=null` for GitLab entries even though the pool file now persists `health_status=quota_exceeded`; routing truth is correct (`block_reason=rate_limited`, persisted `rate_limit_until`). That display mismatch is a smaller follow-up, not part of the runtime fallback regression itself.

### 2026-03-22T21:25:00Z | REPO-CPO-FEAT-P1-T13
- Commands
  - `go test /home/lap/projects/codex-pool-orchestrator -run 'TestClaudeProviderLoadsGitLabManagedAccount|TestClaudeProviderSetAuthHeadersForGitLabManagedAccount|TestClaudeProviderRefreshGitLabManagedAccount|TestProviderUpstreamURLForGitLabClaudeAccount|TestNeedsRefreshWhenGitLabClaudeGatewayStateMissing|TestClassifyManagedGitLabClaudeErrorQuotaExceeded'`
  - `go test /home/lap/projects/codex-pool-orchestrator`
  - `go build /home/lap/projects/codex-pool-orchestrator`
  - `go build -o /home/lap/.local/bin/codex-pool /home/lap/projects/codex-pool-orchestrator`
  - `systemctl --user restart codex-pool.service`
  - `systemctl --user status codex-pool.service --no-pager`
  - `curl -fsS http://127.0.0.1:8989/healthz`
  - `curl -fsS http://127.0.0.1:8989/status | rg -n "GitLab Claude Pool|gitlab-claude-token-add-btn|gitlab-claude-instance-input|GitLab Claude Tokens"`
  - `curl -sS -X POST http://127.0.0.1:8989/operator/claude/gitlab-token-add -H 'Content-Type: application/json' --data '{"token":""}'`
  - `python3 /home/lap/tools/codex_pool_manager.py status --strict`
- Result
  - PASS
  - Added a new managed `gitlab_duo` Claude account mode that stores a GitLab source token, mints short-lived Duo direct-access gateway credentials via `/api/v4/ai/third_party_agents/direct_access`, and reuses the existing Claude `/v1/messages` routing path instead of forking a second provider surface.
  - Claude provider logic is now account-aware for GitLab-backed upstream base URLs and custom GitLab gateway headers; refresh/health state persists to disk and missing/expired gateway state triggers re-minting before use.
  - Runtime handling now classifies GitLab-backed Claude auth/rate-limit failures separately from native Claude OAuth so bad source tokens can be sidelined without inheriting the old "refresh failed => dead Claude OAuth seat" behavior.
  - `/status` now exposes a local-operator GitLab Claude pool card with token + instance URL inputs, pool counts, and a dedicated `POST /operator/claude/gitlab-token-add` endpoint.
  - Focused GitLab Claude tests, package-level `go test`, and `go build` all passed.
  - After deploy, `systemctl --user status codex-pool.service --no-pager` showed the service active on PID `227412`, `/healthz` returned `{"status":"ok","uptime":"15s"}`, the served `/status` HTML contained the new GitLab Claude pool DOM hooks, the new operator endpoint returned the expected validation error `{"error":"token is required"}` for an empty payload, and `python3 /home/lap/tools/codex_pool_manager.py status --strict` returned `PASS`.
- Notes
  - This slice deliberately stops at PAT/OAuth source-token onboarding and direct-access minting. Live end-to-end Claude Code traffic through GitLab-backed seats still needs a real source token smoke once the operator adds one.

### 2026-03-19T21:56:10Z | REPO-CPO-PROOF-T1
- Commands
  - `go build ./...`
  - `go test -run "TestMetrics|TestCandidate|TestPenalty|TestScore|TestSign|TestValidate|TestIsPool|TestLooksLike|TestSaveAccount|TestMergeUsage|TestParse|TestExtract|TestUsageStore|TestRouting|TestPinned|TestShouldStream" -v -count=1 -timeout 60s ./...`
- Result
  - PASS (build clean, 27/27 tests pass)
- Artifacts
  - `/home/lap/.root_layer/shared/spikes/root_e22_s1_t3_first_repo_proof_20260319_215547/build_output.txt`
  - `/home/lap/.root_layer/shared/spikes/root_e22_s1_t3_first_repo_proof_20260319_215547/test_output.txt`
  - `/home/lap/.root_layer/shared/spikes/root_e22_s1_t3_first_repo_proof_20260319_215547/proof_summary.json`
- Notes
  - First external repo proof run for root harness card ROOT-E22-S1-T3.
  - Root control plane switched active_repo.json -> /home/lap/projects/codex-pool-orchestrator.
  - Repo-local SSOT created and card executed through verify/evidence end-to-end.

### 2026-03-22T13:07:53Z | REPO-CPO-REFAC-P0-T1
- Commands
  - `git switch -c refactor/phase0-admission-baseline`
  - `go build ./...`
  - `go test -count=1 -timeout 90s -run "TestProxyStreamedRequestClaude|TestLooksLikeProviderCredential|TestClaudePoolToken_FormatAndBackwardCompatibility|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestProxyWebSocketPassthroughPreservesAuthorization" ./...`
  - `go test ./...`
  - `systemctl --user restart codex-pool.service`
  - `systemctl --user is-active codex-pool.service`
  - `curl -fsS http://127.0.0.1:8989/healthz`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_smoke.json`
  - live pre-deploy `/responses` smoke with pool-issued auth from `/home/lap/.codex/auth.json`
  - live post-deploy `/responses` smoke with the same request against the restarted service
- Result
  - PASS
  - Full `go test ./...` returned green after fixing the stale streamed Claude test and adding focused admission tests.
  - User systemd service restarted cleanly on the newly built binary.
  - `/healthz` returned `{"status":"ok",...}` and `/status?format=json` returned the expected pool summary with 9 Codex accounts and 1 healthy API fallback key.
  - Live `/responses` smoke returned HTTP `200` SSE both before and after deploy on the same minimal request shape.
- Artifacts
  - `/tmp/cpo_live_proxy_pre.json`
  - `/tmp/cpo_live_proxy_pre_ok.json`
  - `/tmp/cpo_live_proxy_pre_ok.sse`
  - `/tmp/cpo_live_proxy_post_ok.sse`
  - `/tmp/cpo_status_smoke.json`
  - `/home/lap/.local/bin/codex-pool.backup_20260322T130700Z`
- Notes
  - The pre-wave targeted baseline was truthfully red only on `TestProxyStreamedRequestClaude`; after the slice, that test and the full suite both passed.
  - `python3 orchestrator/codex_pool_manager.py status --strict` is currently not a valid gate on this machine because it reads a different default env location and reports missing `ADMIN_TOKEN` / `POOL_JWT_SECRET`; the running user service itself is configured via `/home/lap/.root_layer/codex_pool/codex-pool.env`.

### 2026-03-22T13:17:07Z | REPO-CPO-REFAC-P1-T2
- Commands
  - `go build ./...`
  - `go test -count=1 -timeout 90s -run "TestBuild.*RequestShape|TestPlanRoute|TestProxyStreamedRequestClaude|TestResolveProxyAdmission|TestProxyWebSocketPoolRewritesAuthAndPinsSession" ./...`
  - `go test ./...`
  - `systemctl --user is-active codex-pool.service`
  - `curl -fsS http://127.0.0.1:8989/healthz`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_smoke.json`
  - `AUTH=$(jq -r '.tokens.access_token' /home/lap/.codex/auth.json) && timeout 60s curl -sS -N -o /tmp/cpo_live_proxy_plan_ok.sse -w '%{http_code}' http://127.0.0.1:8989/responses -H "Authorization: Bearer $AUTH" -H 'Content-Type: application/json' --data '{"model":"gpt-5.4","instructions":"Reply with exactly OK.","store":false,"stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}'`
- Result
  - PASS
  - Request admission and request-planning contracts now live in `request_planning.go`, and the buffered, streamed, and websocket entrypaths all consume one `RoutePlan`.
  - Targeted guardrail tests and full `go test ./...` passed after the extraction.
  - The deployed user service remained `active`, `/healthz` returned `{"status":"ok","uptime":"3m"}`, and the live `/responses` smoke returned HTTP `200` with SSE completion text `OK`.
  - `/status?format=json` reported `pool.total_accounts=9`, `pool.eligible_accounts=8`, and `api_key_pool_summary=null` at verification time.
- Artifacts
  - `/tmp/cpo_live_proxy_plan_ok.sse`
  - `/tmp/cpo_live_proxy_plan_posttrim.sse`
  - `/tmp/cpo_status_smoke.json`
  - `/home/lap/.local/bin/codex-pool.backup_20260322T131843Z`
- Notes
  - This wave intentionally froze planning contracts without changing downstream pool candidate semantics; required-plan filtering still happens only after `RoutePlan` selection.
  - A governed sidecar review flagged dead `RequestShape` transport/inspectability fields that were only referenced by tests; they were removed before the final deploy so the committed contract stays minimal.
  - The current root-side `codex exec -p root_bureaucracy` lane emitted repeated upstream auth-refresh `401` noise while reviewing the diff, so it cannot be treated as a clean independent close gate on this machine yet.

### 2026-03-22T13:48:50Z | REPO-CPO-BUG-P1-T4
- Commands
  - `go test -count=1 -timeout 90s -run "TestBuild.*RequestShape|TestCandidate|TestRoutingState|TestBuildPoolDashboardData|TestServeStatusPageClarifiesQuotaVsLocalFields" ./...`
  - `go test ./...`
  - `go build ./...`
  - `cp /home/lap/.local/bin/codex-pool /home/lap/.local/bin/codex-pool.backup_20260322T134451Z`
  - `go build -o /home/lap/.local/bin/codex-pool .`
  - `systemctl --user restart codex-pool.service`
  - `systemctl --user is-active codex-pool.service`
  - `curl -fsS http://127.0.0.1:8989/healthz`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_sticky_logic.json`
  - `AUTH=$(jq -r '.tokens.access_token' /home/lap/.codex/auth.json) && timeout 60s curl -sS -N -o /tmp/cpo_live_proxy_sticky_logic.sse -w '%{http_code}' http://127.0.0.1:8989/responses -H "Authorization: Bearer $AUTH" -H 'Content-Type: application/json' --data '{"model":"gpt-5.4","instructions":"Reply with exactly OK.","store":false,"stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}'`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_sticky_logic_after_smoke.json`
- Result
  - PASS
  - Exact-threshold routing now blocks Codex seats at observed `used >= 90%` for both 5h and 7d windows; the old `> 90%` loophole is covered by updated unit tests.
  - Fresh unpinned selection now reuses the most recently used eligible seat before score-based spreading, so the pool drains one seat until it reaches the cutoff instead of rotating evenly.
  - Streamed requests now inherit session/header affinity into `RequestShape.ConversationID`, which lets pinning logic apply to streamed paths when the client provides a session identifier.
  - After deploy, `systemctl --user is-active codex-pool.service` returned `active`, `/healthz` returned `{"status":"ok","uptime":"19s"}`, and live `/responses` smoke returned HTTP `200` with completed SSE output `OK`.
  - `/status?format=json` after restart showed `current_seat.id == active_seat.id == "andy_2"` and `best_eligible_seat == null`, matching the new sticky-selection semantics instead of advertising a different preview seat while one active seat is already being drained.
- Artifacts
  - `/tmp/cpo_status_sticky_logic.json`
  - `/tmp/cpo_status_sticky_logic_after_smoke.json`
  - `/tmp/cpo_live_proxy_sticky_logic.sse`
  - `/home/lap/.local/bin/codex-pool.backup_20260322T134451Z`
- Notes
  - The selector change intentionally preserves the existing hard weekly cutoff for fresh routing; once observed 7d usage reaches `90%`, the seat leaves the candidate pool until the next observed reset.
  - A governed sidecar review also pointed out that streamed requests were bypassing affinity whenever the body was opaque; the final patch fixed that by extracting session identifiers from headers/query in the streamed request shape.

### 2026-03-22T14:19:51Z | REPO-CPO-REFAC-P1-T3
- Commands
  - `go test -count=1 -timeout 90s -run "TestMergeUsage|TestParse|TestExtract|TestUsageStore|TestCodexProviderParseUsageHeaders|TestParseRequestUsageFromSSE" ./...`
  - `go test ./...`
  - `go build ./...`
  - `cp /home/lap/.local/bin/codex-pool /home/lap/.local/bin/codex-pool.backup_20260322T141511Z`
  - `go build -o /home/lap/.local/bin/codex-pool .`
  - `systemctl --user restart codex-pool.service`
  - `systemctl --user is-active codex-pool.service`
  - `curl -fsS http://127.0.0.1:8989/healthz`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_usage_ingestion.json`
  - `AUTH=$(jq -r '.tokens.access_token' /home/lap/.codex/auth.json) && timeout 60s curl -sS -N -o /tmp/cpo_live_proxy_usage_ingestion.sse -w '%{http_code}' http://127.0.0.1:8989/responses -H "Authorization: Bearer $AUTH" -H 'Content-Type: application/json' --data '{"model":"gpt-5.4","instructions":"Reply with exactly OK.","store":false,"stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}'`
- Result
  - PASS
  - `UsageDelta` is now the canonical usage-ingestion contract for Codex/OpenAI-style body, SSE, and header-derived quota snapshots, and the old hand-rolled body parser branches are gone.
  - Provider implementations now sit over shared parsing helpers for OpenAI-style, Anthropic-style, Gemini, and Codex `token_count` usage formats instead of each keeping its own ad hoc token-field extraction logic.
  - Targeted parse/usage tests and full `go test ./...` both passed after the extraction.
  - After deploy, `systemctl --user is-active codex-pool.service` returned `active`, `/healthz` returned `{"status":"ok","uptime":"25s"}`, and live `/responses` smoke returned HTTP `200` with completed SSE output `OK`.
  - `/status?format=json` after restart still reported `pool.total_accounts=9`, `eligible_accounts=8`, and the live seat remained `andy_2`, so the refactor did not perturb the current routing surface.
- Artifacts
  - `/tmp/cpo_status_usage_ingestion.json`
  - `/tmp/cpo_live_proxy_usage_ingestion.sse`
  - `/home/lap/.local/bin/codex-pool.backup_20260322T141511Z`
- Notes
  - This slice intentionally unified ingestion contracts without touching scoring, capacity math, or retry policy.
  - The next smallest remaining debt after this slice is the duplicated SSE/response-recording callback flow between buffered and streamed proxy paths in `main.go`.

### 2026-03-22T14:35:26Z | REPO-CPO-REFAC-P1-T5
- Commands
  - `go test -count=1 -timeout 90s -run "TestProxyStreamedRequestClaude|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestBuild.*RequestShape|TestParse" ./...`
  - `go test ./...`
  - `go build ./...`
  - `cp /home/lap/.local/bin/codex-pool /home/lap/.local/bin/codex-pool.backup_20260322T143156Z`
  - `go build -o /home/lap/.local/bin/codex-pool .`
  - `systemctl --user restart codex-pool.service`
  - `systemctl --user is-active codex-pool.service`
  - `curl -fsS http://127.0.0.1:8989/healthz`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_stream_capture.json`
  - `AUTH=$(jq -r '.tokens.access_token' /home/lap/.codex/auth.json) && timeout 60s curl -sS -N -o /tmp/cpo_live_proxy_stream_capture.sse -w '%{http_code}' http://127.0.0.1:8989/responses -H "Authorization: Bearer $AUTH" -H 'Content-Type: application/json' --data '{"model":"gpt-5.4","instructions":"Reply with exactly OK.","store":false,"stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}'`
- Result
  - PASS
  - Buffered and streamed proxy paths now share one SSE usage recorder helper instead of carrying two near-identical intercept closures in `main.go`.
  - Managed API-key SSE failure classification, Claude two-event accumulation, and enriched usage recording stayed behaviorally identical under the targeted proxy tests.
  - Full `go test ./...` stayed green after the extraction.
  - After deploy, `systemctl --user is-active codex-pool.service` returned `active`, `/healthz` returned `{"status":"ok","uptime":"33s"}`, and live `/responses` smoke returned HTTP `200` with completed SSE output `OK`.
  - `/status?format=json` after restart remained coherent, with `pool.total_accounts=9`, `eligible_accounts=7`, and a stable next seat preview pointing at `john4454`.
- Artifacts
  - `/tmp/cpo_status_stream_capture.json`
  - `/tmp/cpo_live_proxy_stream_capture.sse`
  - `/home/lap/.local/bin/codex-pool.backup_20260322T143156Z`
- Notes
  - This slice intentionally touched only response-stream usage capture; post-copy success bookkeeping and retry/error branches remain the next obvious duplication seam.

### 2026-03-22T16:52:23Z | REPO-CPO-REFAC-P1-T6
- Commands
  - `go test -count=1 -timeout 90s -run "TestProxyStreamedRequestClaude|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestBuild.*RequestShape|TestParse|TestFinalizeProxyResponse" ./...`
  - `go test ./...`
  - `go build ./...`
  - `cp /home/lap/.local/bin/codex-pool /home/lap/.local/bin/codex-pool.backup_20260322T164805Z`
  - `go build -o /home/lap/.local/bin/codex-pool .`
  - `systemctl --user restart codex-pool.service`
  - `systemctl --user is-active codex-pool.service`
  - `curl -fsS http://127.0.0.1:8989/healthz`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_post_response_finalizer.json`
  - `AUTH=$(jq -r '.tokens.access_token' /home/lap/.codex/auth.json) && timeout 60s curl -sS -N -o /tmp/cpo_live_proxy_post_response_finalizer.sse -w '%{http_code}' http://127.0.0.1:8989/responses -H "Authorization: Bearer $AUTH" -H 'Content-Type: application/json' --data '{"model":"gpt-5.4","instructions":"Reply with exactly OK.","store":false,"stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}'`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_post_response_finalizer_after_smoke.json`
- Result
  - PASS
  - Buffered and streamed proxy paths now share one `finalizeProxyResponse` helper for sample logging, non-SSE usage fallback, conversation pinning, managed API recovery, `LastUsed` updates, and penalty decay instead of carrying duplicated post-copy success blocks in `main.go`.
  - New direct tests now lock the most fragile semantics explicitly: request-known conversation IDs stay authoritative over response-derived IDs, managed OpenAI API accounts recover on successful completions, and managed stream failures do not incorrectly clear dead/health state.
  - Focused proxy/finalizer tests, full `go test ./...`, and `go build ./...` all passed after the extraction.
  - After deploy, `systemctl --user is-active codex-pool.service` returned `active`; the first immediate `healthz` probe hit a brief readiness gap after restart, then `curl -fsS http://127.0.0.1:8989/healthz` returned `{"status":"ok","uptime":"1m"}` on retry once the listener settled.
  - Live `/responses` smoke returned HTTP `200` with completed SSE output `OK`, and `/status?format=json` after smoke still reported `total_count=9`, `codex_seat_count=8`, `current_seat.id == active_seat.id == "john4454_2"`, and `last_used_seat.id == "andy_3"`, so the refactor did not perturb the current routing surface.
- Artifacts
  - `/tmp/cpo_status_post_response_finalizer.json`
  - `/tmp/cpo_status_post_response_finalizer_after_smoke.json`
  - `/tmp/cpo_live_proxy_post_response_finalizer.sse`
  - `/home/lap/.local/bin/codex-pool.backup_20260322T164805Z`
- Notes
  - This slice intentionally collapsed only the post-response success/finalization contour; retry/error bookkeeping is now the next smallest remaining duplication seam.

### 2026-03-22T17:29:49Z | REPO-CPO-REFAC-P1-T7
- Commands
  - `go test -count=1 -timeout 90s -run "TestProxyStreamedRequestClaude|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestBuild.*RequestShape|TestParse|TestFinalizeProxyResponse|TestFinalizeCopiedProxyResponse" ./...`
  - `go test ./...`
  - `go build ./...`
  - `cp /home/lap/.local/bin/codex-pool /home/lap/.local/bin/codex-pool.backup_20260322T172822Z`
  - `go build -o /home/lap/.local/bin/codex-pool .`
  - `systemctl --user restart codex-pool.service`
  - `systemctl --user is-active codex-pool.service`
  - `curl -fsS http://127.0.0.1:8989/healthz`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_retry_finalizer.json`
  - `AUTH=$(jq -r '.tokens.access_token' /home/lap/.codex/auth.json) && timeout 60s curl -sS -N -o /tmp/cpo_live_proxy_retry_finalizer.sse -w '%{http_code}' http://127.0.0.1:8989/responses -H "Authorization: Bearer $AUTH" -H 'Content-Type: application/json' --data '{"model":"gpt-5.4","instructions":"Reply with exactly OK.","store":false,"stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}'`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_retry_finalizer_after_smoke.json`
- Result
  - PASS
  - Buffered and streamed proxy paths now share one `finalizeCopiedProxyResponse` helper for post-`io.Copy` error bookkeeping and success/metrics/debug exit handling instead of carrying two near-identical exit contours in `main.go`.
  - New direct tests now lock both sides of that seam explicitly: copy errors record `recent` + `"error"` metrics without mutating success state, and successful copied responses still increment status metrics while running the shared post-response finalizer.
  - Focused proxy/finalizer tests, full `go test ./...`, and `go build ./...` all passed after the extraction.
  - After deploy, `systemctl --user is-active codex-pool.service` returned `active`, `curl -fsS http://127.0.0.1:8989/healthz` returned `{"status":"ok","uptime":"0s"}`, and live `/responses` smoke returned HTTP `200` with completed SSE output `OK`.
  - `/status?format=json` after smoke remained coherent with `total_count=9`, `codex_seat_count=8`, and `current_seat.id == active_seat.id == "andy_2"`, so the refactor did not perturb live seat selection.
- Artifacts
  - `/tmp/cpo_status_retry_finalizer.json`
  - `/tmp/cpo_status_retry_finalizer_after_smoke.json`
  - `/tmp/cpo_live_proxy_retry_finalizer.sse`
  - `/home/lap/.local/bin/codex-pool.backup_20260322T172822Z`
- Notes
  - This slice intentionally stopped at the post-copy exit contour; the next smallest remaining duplication seam is the retryable upstream status disposition block before copying the response body.

### 2026-03-22T18:07:23Z | REPO-CPO-REFAC-P1-T8
- Commands
  - `go test -count=1 -timeout 90s -run "TestProxyStreamedRequestClaude|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestBuild.*RequestShape|TestParse|TestFinalizeProxyResponse|TestFinalizeCopiedProxyResponse|TestApplyPreCopyUpstreamStatusDisposition" ./...`
  - `go test ./...`
  - `go build ./...`
  - `go build -o /home/lap/.local/bin/codex-pool .`
  - `systemctl --user restart codex-pool.service`
  - `systemctl --user is-active codex-pool.service`
  - `curl -fsS http://127.0.0.1:8989/healthz`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_retryable_status_disposition.json`
  - `AUTH=$(jq -r '.tokens.access_token' /home/lap/.codex/auth.json) && timeout 60s curl -sS -N -o /tmp/cpo_live_proxy_retryable_status_disposition.sse -w '%{http_code}' http://127.0.0.1:8989/responses -H "Authorization: Bearer $AUTH" -H 'Content-Type: application/json' --data '{"model":"gpt-5.4","instructions":"Reply with exactly OK.","store":false,"stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}'`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_retryable_status_disposition_after_smoke.json`
- Result
  - PASS
  - Buffered and streamed proxy paths now share one `applyPreCopyUpstreamStatusDisposition` seam plus a focused auth-failure helper, so rate-limit penalties, managed API fallback classification, permanent auth dead-marking, and generic `5xx` penalties no longer live in two diverging copies.
  - New direct tests now lock the most fragile status-side effects explicitly: managed API `5xx` responses record fallback state and `recent` errors, permanent Codex `401/403` failures mark the seat dead, and non-managed `429` responses still set backoff + penalty.
  - Focused proxy/status tests, full `go test ./...`, and `go build ./...` all passed after the extraction.
  - After deploy, `systemctl --user is-active codex-pool.service` returned `active`, `curl -fsS http://127.0.0.1:8989/healthz` returned `{"status":"ok","uptime":"8s"}`, and live `/responses` smoke returned HTTP `200` with completed SSE output `OK`.
  - `/status?format=json` before and after smoke stayed coherent with `total_count=9`, `codex_seat_count=8`, and one configured API fallback key (`total_keys=1`, `eligible_keys=1`, `dead_keys=0`); after the smoke request, the active/current seat advanced coherently to `andy_3` while the fallback key health state remained unchanged (`healthy_keys=0`, `health_error="context canceled"`).
- Artifacts
  - `/tmp/cpo_status_retryable_status_disposition.json`
  - `/tmp/cpo_status_retryable_status_disposition_after_smoke.json`
  - `/tmp/cpo_live_proxy_retryable_status_disposition.sse`
- Notes
  - This slice intentionally stopped before websocket response handling; `proxyRequestWebSocket` still carries a third local copy of the same pre-copy status disposition logic and is the next smallest safe extraction target.

### 2026-03-22T18:37:42Z | REPO-CPO-REFAC-P1-T9
- Commands
  - `go test -count=1 -timeout 120s -run "TestProxyStreamedRequestClaude|TestProxyStreamedManagedAPI5xxPreservesFullErrorBody|TestProxyStreamedManagedAPI5xxDoesNotWaitForFullLargeBody|TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody|TestProxyStreamedManagedAPICompressed429DoesNotWaitForFullLargeBody|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback|TestProxyWebSocketMarksDeactivatedCodexAccountDeadAndFallsThroughNextSeat|TestBuild.*RequestShape|TestParse|TestApplyPreCopyUpstreamStatusDisposition" ./...`
  - `go test ./...`
  - `go build ./...`
  - `go build -o /home/lap/.local/bin/codex-pool .`
  - `systemctl --user restart codex-pool.service`
  - `systemctl --user is-active codex-pool.service`
  - `curl -fsS http://127.0.0.1:8989/healthz`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_websocket_t9.json`
  - `AUTH=$(jq -r '.tokens.access_token' /home/lap/.codex/auth.json) && timeout 60s curl -sS -N -o /tmp/cpo_live_proxy_websocket_t9.sse -w '%{http_code}' http://127.0.0.1:8989/responses -H "Authorization: Bearer $AUTH" -H 'Content-Type: application/json' --data '{"model":"gpt-5.4","instructions":"Reply with exactly OK.","store":false,"stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}'`
  - `AUTH=$(jq -r '.tokens.access_token' /home/lap/.codex/auth.json) && exec 3<>/dev/tcp/127.0.0.1/8989 && printf 'GET /responses HTTP/1.1\r\nHost: 127.0.0.1:8989\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nAuthorization: Bearer %s\r\nsession_id: live-ws-t9-smoke\r\n\r\n' "$AUTH" >&3 && IFS= read -r status <&3 && printf '%s\n' "$status" >/tmp/cpo_live_proxy_websocket_t9_handshake.txt`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_websocket_t9_after_smoke.json`
- Result
  - PASS
  - `proxyRequestWebSocket` `ModifyResponse` now reuses the shared pre-copy status disposition helper path instead of carrying a third local copy of managed API fallback classification, auth-failure penalties, and `5xx` penalties.
  - New focused websocket regression coverage now proves managed API `502` handshake failures preserve the full upstream error body, record fallback health/error state, and do not pin failed sessions; the existing deactivated-seat websocket test also now asserts failed `401` handshakes stay unpinned.
  - The shared pre-copy contour now uses one bounded prefix-inspection + rewind helper for plain bodies plus a gzip path that reads one raw compressed prefix, partially decodes only the early JSON needed for classification, and then replays the raw prefix back to the client stream. This keeps quota/auth markers intact without waiting for slow compressed bodies to finish.
  - Focused proxy/websocket tests, full `go test ./...`, and `go build ./...` all passed after the extraction.
  - After deploy, `systemctl --user is-active codex-pool.service` returned `active`, `curl -fsS http://127.0.0.1:8989/healthz` returned `{"status":"ok","uptime":"22s"}`, live `/responses` smoke returned HTTP `200`, and a raw websocket handshake against `/responses` returned `HTTP/1.1 101 Switching Protocols`.
  - `/status?format=json` succeeded before and after live smoke; artifacts were captured for the pre-smoke snapshot, post-smoke snapshot, SSE smoke body, and websocket handshake status.
- Artifacts
  - `/tmp/cpo_status_websocket_t9.json`
  - `/tmp/cpo_status_websocket_t9_after_smoke.json`
  - `/tmp/cpo_live_proxy_websocket_t9.sse`
  - `/tmp/cpo_live_proxy_websocket_t9_handshake.txt`
- Notes
  - This slice intentionally stopped at pre-copy websocket status handling; a follow-up blind audit still flags one residual risk: gzip retryable inspection remains sensitive to pathological short first reads at the transport layer, so the next truthful successor is hardening that chunking edge before returning to lower-priority websocket success-state refactors.

### 2026-03-23T06:49:28Z | REPO-CPO-BUG-P1-T14
- Commands
  - `go test -count=1 -run 'TestClaudeProviderParseUsageSupportsNonStreamMessagePayload|TestUpdateUsageFromBodyRecordsClaudeNonStreamMessage|TestClaudeProviderLoadsGitLabManagedAccount|TestClaudeProviderSetAuthHeadersForGitLabManagedAccount|TestClaudeProviderRefreshGitLabManagedAccount|TestProviderUpstreamURLForGitLabClaudeAccount|TestNeedsRefreshWhenGitLabClaudeGatewayStateMissing|TestClassifyManagedGitLabClaudeErrorQuotaExceeded' ./...`
  - `go build -o /home/lap/.local/bin/codex-pool .`
  - `systemctl --user restart codex-pool.service`
  - `curl -fsS http://127.0.0.1:8989/healthz`
  - `POOL_USER_TOKEN=$(jq -r '.[0].token' /home/lap/.root_layer/codex_pool/data/pool_users.json) && CLAUDE_POOL_TOKEN=$(curl -fsS --max-time 15 "http://127.0.0.1:8989/config/claude/${POOL_USER_TOKEN}" | jq -r '.access_token') && curl -sS --max-time 90 -X POST http://127.0.0.1:8989/v1/messages -H "Authorization: Bearer ${CLAUDE_POOL_TOKEN}" -H 'Content-Type: application/json' -H 'anthropic-version: 2023-06-01' --data '{"model":"claude-sonnet-4-20250514","max_tokens":64,"messages":[{"role":"user","content":"Reply with exactly OK"}]}'`
  - `python3 /home/lap/tools/codex_pool_manager.py status | jq '.admin_accounts[] | select(.type=="claude" and .plan_type=="gitlab_duo") | {id, totals, dead, disabled, last_refresh, expires_at}'`
- Result
  - PASS
  - Live GitLab Claude smoke had already proven the new lane returned a real `200 OK` non-stream Anthropic message response, but local totals for the `gitlab_duo` account stayed at zero afterward.
  - Root cause: `finalizeProxyResponse` already feeds non-SSE response bodies into `updateUsageFromBody`, but `ClaudeProvider.ParseUsage` only recognized SSE event payloads (`message_start`, `message_delta`) and returned `nil` for ordinary top-level Anthropic `{"type":"message","usage":...}` JSON bodies.
  - Fix: `ClaudeProvider.ParseUsage` now falls through to the existing generic top-level `usage` parser for non-stream Anthropic message payloads, and focused regression coverage now exercises both the parser and the `updateUsageFromBody` accounting path.
  - After deploy and restart, a repeated live `POST /v1/messages` smoke through the pool again returned `200 OK` with assistant text `OK`; the managed account `claude_gitlab_457e812b181e` then showed `request_count=1`, `total_input_tokens=11`, `total_output_tokens=4`, and `total_billable_tokens=15`, confirming local accounting now tracks real non-stream Claude/GitLab traffic.
- Artifacts
  - live smoke body captured in terminal output only
- Notes
  - This fix closes the Claude/GitLab non-stream local-accounting gap without changing routing, token minting, or refresh policy.
  - `python3 /home/lap/tools/codex_pool_manager.py status --strict` still reports the pre-existing unrelated failure `pool_dashboard_account_count_mismatch`; that mismatch did not block the live Claude/GitLab smoke and was left untouched in this slice.

### 2026-03-22T18:17:07Z | REPO-CPO-BUG-P1-T10
- Commands
  - `go test -count=1 -timeout 90s -run "TestProxyStreamedRequestClaude|TestProxyStreamedManagedAPI5xxPreservesFullErrorBody|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestBuild.*RequestShape|TestParse|TestFinalizeProxyResponse|TestFinalizeCopiedProxyResponse|TestApplyPreCopyUpstreamStatusDisposition" ./...`
  - `go test ./...`
  - `go build ./...`
  - `go build -o /home/lap/.local/bin/codex-pool .`
  - `systemctl --user restart codex-pool.service`
  - `systemctl --user is-active codex-pool.service`
  - `curl -fsS http://127.0.0.1:8989/healthz`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_streamed_error_body_fix.json`
  - `AUTH=$(jq -r '.tokens.access_token' /home/lap/.codex/auth.json) && timeout 60s curl -sS -N -o /tmp/cpo_live_proxy_streamed_error_body_fix.sse -w '%{http_code}' http://127.0.0.1:8989/responses -H "Authorization: Bearer $AUTH" -H 'Content-Type: application/json' --data '{"model":"gpt-5.4","instructions":"Reply with exactly OK.","store":false,"stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}'`
  - `curl -fsS http://127.0.0.1:8989/status?format=json >/tmp/cpo_status_streamed_error_body_fix_after_smoke.json`
- Result
  - PASS
  - A blind close-audit on the previous T8 commit surfaced a real streamed-mode regression: managed `5xx` inspection reused a `LimitReader(2048)` path and rewound only the truncated error body back to the client.
  - Streamed status inspection now reads the full upstream error payload for rewinding while handing only a bounded inspected copy into classification/logging, so managed API fallback handling keeps the new shared seam without corrupting client-visible bodies.
  - New regression coverage now proves the streamed proxy returns the full managed-API `502` body even when it exceeds the inspection limit.
  - Focused proxy/status tests, full `go test ./...`, and `go build ./...` all passed after the fix.
  - After deploy, `systemctl --user is-active codex-pool.service` returned `active`, `curl -fsS http://127.0.0.1:8989/healthz` returned `{"status":"ok","uptime":"27s"}`, and live `/responses` smoke returned HTTP `200` with completed SSE output `OK`.
  - `/status?format=json` remained coherent before and after smoke with `total_count=9`, `codex_seat_count=8`, and one configured API fallback key (`total_keys=1`, `eligible_keys=1`, `dead_keys=0`); after smoke the active/current seat advanced coherently to `luka`.
- Artifacts
  - `/tmp/cpo_status_streamed_error_body_fix.json`
  - `/tmp/cpo_status_streamed_error_body_fix_after_smoke.json`
  - `/tmp/cpo_live_proxy_streamed_error_body_fix.sse`
- Notes
  - This hotfix closes the audit-found streamed body truncation regression without reopening the broader T8 extraction; websocket response handling remains the next duplicate pre-copy status seam.

### 2026-03-23T13:12:00Z | REPO-CPO-UI-P1-T15
- Commands
  - `awk '/<script>/{flag=1;next}/<\/script>/{flag=0}flag' /home/lap/projects/codex-pool-orchestrator/templates/local_landing.html >/tmp/local_landing.js && node -c /tmp/local_landing.js`
  - `go test -count=1 -run 'TestServeFriendLanding_LocalTemplateIncludesCodexOAuthAction|TestServeStatusPageReturnsJSONForFormatQuery|TestBuildPoolDashboardDataShowsGitLabDirectAccessSignals' ./...`
  - `go build ./...`
  - `go build -o /home/lap/.local/bin/codex-pool .`
  - `timeout 20s systemctl --user restart codex-pool.service`
  - `timeout 20s systemctl --user show -p MainPID,ExecMainStartTimestamp codex-pool.service`
  - `curl -fsS http://127.0.0.1:8989/ >/tmp/cpo_local_landing_dashboard.html`
  - `rg -n 'Local Operator Dashboard|Codex Dashboard|Claude Dashboard|Gemini Dashboard|Fallback API Pool|GitLab Claude Pool|/operator/codex/api-key-add|/operator/claude/gitlab-token-add|/operator/account-delete' /tmp/cpo_local_landing_dashboard.html`
  - `! rg -n '/hero.png|hero-art|hero-wrapper|data-tab="stats"|id="tab-stats"' /tmp/cpo_local_landing_dashboard.html`
  - `Playwright live smoke on http://127.0.0.1:8989/ with Codex/Claude/Gemini tab snapshots + screenshots`
  - `python3 /home/lap/tools/root_telegram_operator.py send-report --report-path /home/lap/.root_layer/shared/reports/REPO-CPO-UI-P1-T15_20260323_091337_dashboard_first_landing.md --label cpo_dashboard_first_landing`
- Result
  - PASS
  - The local `/` landing is now dashboard-first: the decorative hero block is gone, `Codex`, `Claude`, and `Gemini` tabs all render live provider dashboards from `/status?format=json`, and setup blocks were pushed below the live operator surfaces.
  - Codex now exposes both seat state and `Fallback API Pool` controls on the landing, Claude exposes `GitLab Claude Pool` health plus GitLab token add flow, and all provider tables now clip long identifiers while keeping manual delete actions in the interface.
  - The landing reuses one live data contract instead of drifting into a separate setup-only page; the old dedicated `Status` tab was removed and its live account/workspace summaries were distributed into the provider tabs.
  - Targeted landing/status tests passed, the embedded-template JS passed standalone syntax check, and the deployed user service restarted onto `MainPID=71683` at `Mon 2026-03-23 09:09:45 EDT`.
  - Final Playwright smoke on the restarted service showed populated Codex/Claude/Gemini dashboards and `browser_console_messages(level=info)` returned zero errors after the final reload.
  - Telegram repo-update delivery succeeded through the root operator channel with `message_id=529` and `file_message_id=530`.
- Artifacts
  - `/tmp/local_landing.js`
  - `/tmp/cpo_local_landing_dashboard.html`
  - `/home/lap/.root_layer/shared/reports/REPO-CPO-UI-P1-T15_20260323_091337_dashboard_first_landing.md`
  - `/home/lap/.root_layer/shared/spikes/root_telegram_channel_cpo_dashboard_first_landing_20260323_131439/summary.json`
  - `cpo-landing-codex.png`
  - `cpo-landing-claude.png`
  - `cpo-landing-gemini.png`
- Notes
  - The next truthful successor remains `REPO-CPO-REFAC-P1-T16` for GitLab Claude persistence/health truth.

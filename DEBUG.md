# DEBUG — codex-pool-orchestrator

## Fast Checks
- Build: `go build ./...`
- Full Go suite: `go test ./...`
- Admission + planning guardrails: `go test -count=1 -timeout 90s -run "TestBuild.*RequestShape|TestPlanRoute|TestResolveProxyAdmission|TestProxyStreamedRequestClaude|TestLooksLikeProviderCredential|TestClaudePoolToken_FormatAndBackwardCompatibility|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestProxyWebSocketPassthroughPreservesAuthorization" ./...`
- Selector + status guardrails: `go test -count=1 -timeout 90s -run "TestBuild.*RequestShape|TestCandidate|TestRoutingState|TestBuildPoolDashboardData|TestServeStatusPageClarifiesQuotaVsLocalFields" ./...`
- Usage ingestion guardrails: `go test -count=1 -timeout 90s -run "TestMergeUsage|TestParse|TestExtract|TestUsageStore|TestCodexProviderParseUsageHeaders|TestParseRequestUsageFromSSE" ./...`
- Response stream capture guardrails: `go test -count=1 -timeout 90s -run "TestProxyStreamedRequestClaude|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestBuild.*RequestShape|TestParse" ./...`

## Service Checks
- User service status: `systemctl --user status codex-pool.service --no-pager`
- User service restart: `systemctl --user restart codex-pool.service`
- Local status page: `curl -fsS http://127.0.0.1:8989/status`
- Local health probe: `curl -fsS http://127.0.0.1:8989/healthz`
- Gemini Browser Auth start (low-level truth check): `curl -fsS -X POST http://127.0.0.1:8989/operator/gemini/oauth-start -H 'Content-Type: application/json' --data '{}'`
- Restricted Gemini seat smoke with persisted `operational_truth`: `curl -fsS -X POST http://127.0.0.1:8989/operator/gemini/seat-smoke -H 'Content-Type: application/json' --data '{"account_id":"gemini_seat_1d2425df7919","model":"gemini-2.5-flash","prompt":"Reply with exactly GEMINI_SMOKE_OK:gemini_seat_1d2425df7919."}'`
- Direct pooled Gemini v1beta probe: `curl -fsS -X POST http://127.0.0.1:8989/v1beta/models/gemini-2.5-flash:generateContent -H "x-goog-api-key: $POOL_KEY" -H 'Content-Type: application/json' --data @/tmp/cpo_pool_v1beta_probe_body.json`
- OpenCode live smoke through pool (canonical Gemini path): `timeout 120s opencode run -m codex-pool/gemini-3.1-pro-high "Reply with exactly AG_POOL_OK."`
- Live proxy smoke: `AUTH=$(jq -r '.tokens.access_token' /home/lap/.codex/auth.json) && curl -sS -N http://127.0.0.1:8989/responses -H "Authorization: Bearer $AUTH" -H 'Content-Type: application/json' --data '{"model":"gpt-5.4","instructions":"Reply with exactly OK.","store":false,"stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}'`
- GitLab Claude direct token truth check: `python3 - <<'PY' ... direct_access -> /v1/messages ... PY`
- GitLab Claude pool fallback smoke: `POOL_USER_TOKEN=$(jq -r '.[] | select(.disabled==false and .email=="lap+agcode@local.pool") | .token' /home/lap/.root_layer/codex_pool/data/pool_users.json | head -n1) && CLAUDE_POOL_TOKEN=$(curl -fsS --max-time 15 "http://127.0.0.1:8989/config/claude/${POOL_USER_TOKEN}" | jq -r '.access_token') && curl -sS --max-time 90 -D - -X POST http://127.0.0.1:8989/v1/messages -H "Authorization: Bearer ${CLAUDE_POOL_TOKEN}" -H 'Content-Type: application/json' -H 'anthropic-version: 2023-06-01' --data '{"model":"claude-sonnet-4-20250514","max_tokens":64,"messages":[{"role":"user","content":"Reply with exactly OK"}]}'`
- GitLab Claude cooldown truth check: `curl -fsS http://127.0.0.1:8989/status?format=json | jq '[.accounts[] | select(.type=="claude") | {id,health_status,health_error,routing:.routing,recovery_at:(.routing.recovery_at // empty)}]'`
- Claude CLI wrapper smoke: `timeout 120s fish -lc 'claude --model sonnet -p "Reply with exactly OK."'`

## Notes
- Repo-local product debugging happens here; root-only routing/debug policy remains in `/home/lap/DEBUG.md`.
- Do not store secrets or exported auth payloads in repo-local evidence or docs.
- The current user service reads `/home/lap/.root_layer/codex_pool/codex-pool.env`; do not assume the runtime env lives under `%h/.local/share/codex-pool/runtime/` during local operator checks.
- For pooled Gemini API-key mode, `POOL_KEY` means the synthetic Gemini pool API key (`AIzaSy-pool-...`), not the raw pool-user download token used by `/config/gemini/<token>`.
- For GitLab Claude, `GitLabRateLimitRemaining/ResetAt` are observability fields from the direct-access/token-mint layer, not a trustworthy `/v1/messages` serving-quota balancer input. Use `RateLimitUntil`, `Inflight`, and `LastUsed` as runtime routing truth.
- Pooled Gemini `/v1beta/models/*:generateContent` for imported browser-auth Gemini seats currently succeeds through the internal Code Assist facade path and requires a persisted provider project id; seats without `token.project_id` are not usable for this live-smoke lane.
- For restricted browser-auth Gemini seats, `/operator/gemini/seat-smoke` is the canonical live proof: it can succeed through the fallback project even when `provider_truth.state=restricted` or `provider_truth.state=missing_project_id`, and it persists `gemini_operational_state=degraded_ok` separately from provider truth.
- Ready browser-auth Gemini seats now auto-refresh stale provider truth on startup and on the 10-minute stale poller; after a restart, give the local service a few seconds before treating `stale_provider_truth` as final runtime truth.

## 2026-03-28 Codex Refresh-Invalid Audit
- Manual direct-access artifact: `/home/lap/.root_layer/shared/spikes/codex_refresh_invalid_probe_20260328.md`
- Archived dead-seat material recheck: `/home/lap/.root_layer/codex_pool/backups/manual_dead_probe_20260328T151446Z/backup_material_recheck_20260328T160906Z.json`
- Current active-pool recheck: `/home/lap/.root_layer/codex_pool/backups/manual_dead_probe_20260328T151446Z/current_access_recheck_20260328T160819Z.json`
- Focused verify:
  - `go test ./... -run 'TestFetchUsageCodexRefreshInvalidKeepsLiveCurrentAccess|TestFetchUsageCodexUnauthorizedAfterRefreshInvalidKeepsLiveWhenModelsStillWork|TestForceRefreshAccountPersistsPermanentCodexRefreshFailure|TestForceRefreshAccountMarksDeadWhenCodexModelsProbeConfirmsDeactivatedWorkspace|TestRestorePersistedUsageState|TestCandidateRetryPathDoesNotMoveActiveCodexSeat|TestCandidateSkipsRateLimitedLocalCodexSeat|TestProxyWebSocketMarksDeactivatedCodexAccountDeadAndFallsThroughNextSeat|TestApplyPreCopyUpstreamStatusDispositionMarksPermanentCodexAuthFailureDead|TestSaveCodexAccountPersistsOAuthHealthState|TestReloadAccountsPreservesRuntimeState'`
  - `go test ./... -run 'TestServeStatusPageShowsCodexRefreshInvalidHealthLine|TestForceRefreshAccountPersistsPermanentCodexRefreshFailure|TestForceRefreshAccountMarksDeadWhenCodexModelsProbeConfirmsDeactivatedWorkspace|TestFetchUsageCodexRefreshInvalidKeepsLiveCurrentAccess|TestFetchUsageCodexUnauthorizedAfterRefreshInvalidKeepsLiveWhenModelsStillWork'`
  - `go build ./...`
  - `go build -o /home/lap/.local/bin/codex-pool .`
  - `systemctl --user restart codex-pool.service`
  - `python3 /home/lap/tools/codex_pool_manager.py status --strict`
  - `curl -fsS http://127.0.0.1:8989/status?format=json`
- Sentinels:
  - `status --strict` must return `PASS`.
  - `/status?format=json` must not show Codex seats marked dead solely because refresh returned `refresh_token_reused` / `invalid_grant`.
- Verified outcome on the archived material:
  - `backup_material_recheck_20260328T160906Z.json` shows `13` seats whose last saved `health_error` was `refresh token revoked/invalid` still return `200` on `codex/models`; `6` seats now classify as `workspace_deactivated` (`andy_2`, `ghjh8782`, `john4454`, `luka`, `luka_2`, `primary`).
  - The current active pool directory no longer contains the archived dead files that disappeared from service intake; `current_access_recheck_20260328T160819Z.json` therefore reflects only active material still present under `pool/codex`.

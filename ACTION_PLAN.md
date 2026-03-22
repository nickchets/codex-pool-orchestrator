# ACTION_PLAN — codex-pool-orchestrator

> Repo-local board for governed product work in this capsule.

## Board

### DOING

_(empty — truthful idle handoff; successor cards are hydrated in `NEXT`)_

### NEXT

#### REPO-CPO-REFAC-P1-T9: Reuse shared pre-copy status disposition in websocket flow
1. Switch `proxyRequestWebSocket` `ModifyResponse` status handling to the new shared pre-copy disposition helpers instead of carrying a third local copy of managed API failure classification, auth-failure penalties, and 5xx penalties.
2. Preserve websocket-specific `101` handling, protocol auth rewrite, and session pinning semantics exactly.
3. Lock parity with focused websocket/proxy tests before touching broader routing or selector behavior.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 90s -run "TestProxyStreamedRequestClaude|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestBuild.*RequestShape|TestParse|TestApplyPreCopyUpstreamStatusDisposition" ./...`

### BLOCKED

_(none)_

### DONE

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

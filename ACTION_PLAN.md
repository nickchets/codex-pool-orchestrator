# ACTION_PLAN — codex-pool-orchestrator

> Repo-local board for governed product work in this capsule.

## Board

### DOING

_(empty — truthful idle handoff; successor cards are hydrated in `NEXT`)_

### NEXT

#### REPO-CPO-REFAC-P1-T5: Collapse duplicated response streaming usage capture
1. Extract one shared response-stream usage recorder for buffered and streamed proxy paths in `main.go`.
2. Preserve managed API-key SSE failure handling, Claude two-event accumulation, and conversation pinning semantics exactly.
3. Lock parity with targeted proxy/usage tests before touching retry or routing behavior.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 90s -run "TestProxyStreamedRequestClaude|TestProxyWebSocketPoolRewritesAuthAndPinsSession|TestBuild.*RequestShape|TestParse" ./...`

### BLOCKED

_(none)_

### DONE

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

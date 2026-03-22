# ACTION_PLAN — codex-pool-orchestrator

> Repo-local board for governed product work in this capsule.

## Board

### DOING

_(empty — truthful idle handoff; successor cards are hydrated in `NEXT`)_

### NEXT

#### REPO-CPO-REFAC-P1-T2: Freeze request planning contracts
1. Introduce canonical types for `AdmissionResult`, `RequestShape`, and `RoutePlan`.
2. Move provider/path/model/required-plan planning into a pure layer that can be reused by buffered, streamed, and websocket flows.
3. Add guardrail tests for provider routing, model override, and streamed-body opaque planning.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 90s -run "TestPickUpstream|TestLooksLikeProviderCredential|TestProxyStreamedRequestClaude" ./...`

#### REPO-CPO-REFAC-P1-T3: Unify usage ingestion
1. Replace duplicated header/body/SSE usage parsing with one canonical `UsageDelta` pipeline.
2. Keep provider-specific parsing only as strategy implementations over the shared contract.
3. Lock parity with JSON, SSE, and header-driven usage fixtures before touching scoring or analytics.

**Verify hook:** `cd /home/lap/projects/codex-pool-orchestrator && go test -count=1 -timeout 90s -run "TestMergeUsage|TestParse|TestExtract|TestUsageStore" ./...`

### BLOCKED

_(none)_

### DONE

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

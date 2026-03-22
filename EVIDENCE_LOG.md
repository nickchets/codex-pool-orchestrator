# EVIDENCE_LOG — codex-pool-orchestrator

> Repo-local evidence for root harness proof execution.

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

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
- Live proxy smoke: `AUTH=$(jq -r '.tokens.access_token' /home/lap/.codex/auth.json) && curl -sS -N http://127.0.0.1:8989/responses -H "Authorization: Bearer $AUTH" -H 'Content-Type: application/json' --data '{"model":"gpt-5.4","instructions":"Reply with exactly OK.","store":false,"stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}'`

## Notes
- Repo-local product debugging happens here; root-only routing/debug policy remains in `/home/lap/DEBUG.md`.
- Do not store secrets or exported auth payloads in repo-local evidence or docs.
- The current user service reads `/home/lap/.root_layer/codex_pool/codex-pool.env`; do not assume the runtime env lives under `%h/.local/share/codex-pool/runtime/` during local operator checks.

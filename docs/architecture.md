# codex-pool architecture

Last reviewed: 2026-05-13T14:14:15Z

This document maps the current `codex-pool-orchestrator` architecture as code and process evidence, without dumping live account material, environment files, Hermes config, bbolt rows, or credentials. It is intentionally operational: it focuses on request flow, ownership boundaries, state/secrets boundaries, adapter seams, and safe refactor seams.

## Runtime and service boundary

`codex-pool-orchestrator` is a Go HTTP service that sits between local agent/LLM clients and several upstream account/provider families. It accepts local HTTP traffic, authenticates or admits the caller, plans a provider route, selects an eligible account/seat, forwards the request, delivers buffered/streaming/WebSocket responses, records usage, and exposes operator diagnostics.

Observed host boundary during this review:

- systemd unit: `codex-pool.service`.
- installed binary: `/usr/local/bin/codex-pool-orchestrator`.
- service user/group: `openclaw` / `openclaw`.
- working directory: `/var/lib/codex-pool`.
- local listener: `127.0.0.1:8989`.
- local health/status probes: `/healthz` and `/status?format=json` returned HTTP 200; `/metrics` returned HTTP 401 without an admin token, as expected.

Repository boundary:

- source repo: `/home/openclaw/ai-gateways/codex-pool-orchestrator`.
- repo edits do not modify production by themselves; build/install/restart/deploy are separate operator-controlled steps.
- this architecture task did not restart services, deploy, commit, push, reset, stash, or inspect raw secret stores.

Startup composition:

1. `config.go` and `main.go:buildConfig` merge defaults, `config.toml`, environment variables, and CLI flags. The default listen address is `127.0.0.1:8989`.
2. `main.go` constructs provider instances for Codex, Claude, Gemini, Kimi, and MiniMax, then builds a `ProviderRegistry`.
3. `pool.go:loadPool` loads provider subdirectories under the configured pool directory: `codex`, `codex_gitlab`, `openai_api`, `claude`, `claude_gitlab`, `gemini`, `kimi`, and `minimax`.
4. `storage.go:newUsageStore` opens the bbolt usage database and creates aggregate/request buckets.
5. persisted account usage/runtime state is restored into the in-memory pool.
6. pool-user storage is initialized only when pool-user auth is configured.
7. `proxyHandler` is assembled with config, transports, pool state, pool users, provider registry, usage store, metrics, recent errors, and upstream error tracker.
8. `http.Server` starts on the configured listen address with the router handler and HTTP/2 settings tuned for long-running streams.

## Request lifecycle

### 1. Router and local endpoints

All HTTP traffic enters `router.go:ServeHTTP`. The router creates a request id and trace, handles local/static/operator/admin paths first, and falls through to `proxyRequest` for upstream-bound traffic.

Important local routes:

- `/healthz`: basic process health.
- `/status`: status/dashboard data; `?format=json` is the machine-readable operator-safe form.
- `/metrics`: Prometheus-style metrics, guarded by admin auth.
- `/`: landing/dashboard UI.
- `/api/pool/*`: pool usage/user/status surfaces with admin/friend auth where required.
- `/operator/*`: trusted loopback operator flows for adding/removing/smoking seats.
- `/admin/*`: admin-token or local-operator management routes.
- `/setup/*` and `/config/*`: generated client setup/config download routes; path tokens act as route-level auth.
- `/oauth/token`: local compatibility route for client refresh flows.

Router auth helpers are intentionally separate from provider auth:

- `checkAdminAuth` accepts the configured admin token via header/query and otherwise rejects.
- `checkAdminOrFriendAuth` accepts admin token or friend code for friend-mode pool stats.
- `checkLocalOperatorAuth` requires trusted loopback posture and no forwarding headers.
- provider credentials are not used as local admin credentials.

### 2. Admission/auth

`request_planning.go:resolveProxyAdmission` owns proxy admission:

1. OpenAI-compatible pool API tokens are checked first so pool-issued virtual keys are not mistaken for provider passthrough tokens.
2. Pool-user JWT and pool-specific Claude/Gemini token forms are checked when the pool JWT secret is configured.
3. Provider-looking credentials fall through to passthrough mode.
4. Otherwise the request is rejected as unauthorized.

Admission produces an `AdmissionResult` containing non-secret routing attributes: admission kind, user id, provider type, token id/name, credential kind, token policy, status, and message. Raw token material should not cross this boundary.

### 3. Request shape planning

`proxyRequest` classifies the request shape before choosing the proxy path:

- WebSocket upgrade: `buildWebSocketRequestShape` plus `proxyRequestWebSocket`.
- streamed body: `buildStreamedRequestShape` plus `proxyRequestStreamed`.
- buffered/replayable body: `buildBufferedRequestShape` plus buffered attempt/delivery helpers.

Pool-user traffic is forced through buffered body handling when possible because pre-first-byte upstream failures can then rotate to another eligible seat without corrupting downstream semantics.

### 4. Route planning

`request_planning.go:planRoute` combines:

- path/header provider selection from `pickUpstream` and `ProviderRegistry`;
- model-based overrides for Gemini, Kimi, MiniMax, GitLab-Codex, Codex Pro, and feature-flagged Codex chat-to-responses adaptation;
- upstream path rewriting;
- required-plan selection;
- OpenAI-compatible selection mode for pool API token traffic;
- debug Gemini seat override authorization.

The resulting `RoutePlan` is the contract between admission, provider selection, pool selection, policy reservation, upstream attempt, delivery, and usage attribution.

### 5. Provider adapter boundary

`provider.go` defines the provider port. Provider implementations own:

- account-file parsing;
- upstream auth header injection;
- token refresh;
- usage parsing from events and headers;
- upstream base URL selection;
- path matching and normalization;
- SSE detection.

Registry path-match order is Gemini, Claude, Codex, then model-routed extra providers. Kimi and MiniMax are model-routed and should not win broad path matching.

### 6. Pool selection

`pool.go` owns account-seat state and candidate selection. Selection prioritizes:

1. conversation pinning when the pinned account remains eligible;
2. sticky recent eligible seat for new unpinned work;
3. provider/account type and required-plan filters;
4. account health, disabled/dead state, cooldown, route capability, quota/headroom, and inflight checks;
5. tier/headroom/drain urgency/recency/inflight scoring;
6. an exclusion set to avoid retrying already-failed candidates during a buffered attempt loop.

Candidate claims and inflight counters are the pool/execution handoff. Successful responses may pin a conversation id to the account. Failure paths update penalties, cooldowns, rate-limit windows, dead state, and recent error summaries according to the classifier.

### 7. Upstream attempt and proxy delivery

Buffered execution uses `tryOnce` and the buffered attempt contour:

- refreshes a seat when supported and needed;
- validates route support and managed-key readiness;
- optionally builds Gemini Code Assist facade requests;
- rewrites path/body/model where route planning requires it;
- strips client auth headers that must not be forwarded as-is;
- lets the provider set upstream auth headers;
- sends via the configured transport;
- inspects bounded pre-copy failures;
- retries only while no downstream bytes have been sent.

Streaming execution uses `proxyRequestStreamed` and delivers bytes once the upstream response is accepted. It still performs bounded pre-copy status handling, but after downstream bytes are committed it records partial/failure state rather than pretending a transparent retry is possible.

WebSocket execution uses `proxyRequestWebSocket` and `servePooledWebSocketProxy`:

- selects a compatible account before upgrade;
- extracts/replaces OpenAI insecure-api-key subprotocol bearer material internally;
- strips/replaces conflicting client auth headers;
- uses `httputil.ReverseProxy` to preserve upgrade semantics;
- records status, metrics, trace, and conversation pinning after switching-protocols or other final status.

Passthrough paths are separate: provider-looking credentials can be forwarded without pool-user admission, but local admin/friend/operator auth remains separate.

### 8. Error and retry taxonomy

`upstream_error.go`, `egress_health.go`, and provider-specific disposition helpers classify failures into operational categories:

- `upstream_cloudflare_challenge`;
- `upstream_provider_policy_block`;
- `upstream_auth_failure`;
- `upstream_rate_limit`;
- `upstream_transient`;
- `upstream_unknown`.

The core safety rule is semantic, not moral: retry/rotation is safe before the first downstream byte; after stream/WebSocket bytes are client-visible, the service must record and surface the actual stream result.

`egress_health.go` stores redacted summaries and hashes Cloudflare Ray correlation material (`sha256:<prefix>`) rather than exposing raw header values.

### 9. Usage, metrics, status

Usage flow:

- providers parse usage from SSE events, final JSON, and selected headers;
- `UsageAttribution` attaches non-secret token/client metadata only;
- `finalizeProxyResponseWithAttribution` updates account/user/token aggregates;
- `storage.go` persists request rows, account aggregates, user aggregates, token aggregates, plan capacity, capacity samples, and runtime snapshots in bbolt buckets.

Metrics flow:

- `metrics.go` emits `codexpool_requests_total`, `codexpool_account_requests_total`, `codexpool_events_total`, TTFB observation counters, and protocol-adapter flags;
- `/metrics` is protected by admin auth;
- metrics must use aggregate ids/statuses/classes, not raw tokens/prompts/config.

Status flow:

- `status.go` builds status/dashboard data: pool summary, provider summaries, workspace groups, current/best/last-used seats, quota/cooldown/routing state, Gemini operator state, GitLab-Claude recovery state, token usage, and upstream error summary;
- `/status?format=json` is intended to be operator-safe aggregate status, not a raw account/config dump.

## Adapter surfaces

### Codex / OpenAI-compatible

- `openai_compatible.go`: validates OpenAI-compatible pool-token endpoints (`/v1/models`, `/v1/chat/completions`, `/v1/responses` and safe response child operations), model allowlists, and default model entries.
- `codex_models.go`: model list serving/caching for Codex/OpenAI-compatible clients.
- `codex_chat_responses_adapter.go`: feature-flagged `/v1/chat/completions` to `/v1/responses` request conversion and response/SSE conversion back to OpenAI chat-completions shape. It deliberately supports a bounded field subset.
- `codex_api_keys.go`: managed upstream OpenAI-compatible API-key seats as pool capacity, not raw user-visible key material.
- `codex_egress_proxy.go`: optional HTTP proxy transport for selected ChatGPT/Codex upstream hosts; logs only proxy host/port, not proxy credentials.

### Gemini / OpenCode

- `provider_gemini.go`, `gemini_code_assist_facade.go`, `gemini_antigravity.go`, and `gemini_operator.go`: Gemini seat loading, Code Assist project/facade behavior, Antigravity flows, quota truth, seat smoke/reset flows.
- `opencode_contract.go` and `opencode_runtime_adapters.go`: OpenCode-compatible setup/runtime behavior plus OpenAI/Anthropic to Gemini compatibility transforms.

### Claude / GitLab Claude

- `provider_claude.go`, `claude_auth.go`, `claude_gitlab.go`, and `claude_gitlab_recovery.go`: Claude account parsing, auth, usage parsing, managed GitLab-Claude behavior, and recovery classification.

### Extra model-routed upstreams

- `provider_kimi.go` and `provider_minimax.go`: model-routed provider adapters behind the registry/provider contract.

## Major files, hotspots, and ownership boundaries

| Area | Primary files | Ownership boundary |
| --- | --- | --- |
| Process/config/bootstrap | `main.go`, `config.go` | Runtime config merge, provider registry, pool load, usage store, HTTP server. Should not own provider internals or dashboard rendering long-term. |
| Router/auth/admission/planning | `router.go`, `request_planning.go`, `openai_compatible.go` | Local endpoint table, admin/friend/operator auth, pool/provider admission, route plan contract, endpoint/model validation. |
| Pool/account selection | `pool.go`, `dead_cleanup.go`, provider-specific managed-seat helpers | Account model, seat health, headroom/cooldown, stickiness, inflight, persisted account/runtime state. |
| Provider adapters | `provider.go`, `provider_codex.go`, `provider_claude.go`, `provider_gemini.go`, `provider_kimi.go`, `provider_minimax.go`, `provider_api_key.go` | Upstream-specific account parsing, auth, refresh, URL/path normalization, SSE detection, usage extraction. |
| Codex/OpenAI-compatible | `codex_models.go`, `openai_compatible.go`, `codex_chat_responses_adapter.go`, `codex_api_keys.go`, `codex_egress_proxy.go` | OpenAI-compatible endpoint facade, chat-to-responses adapter, managed API-key seats, selected egress transport. |
| Streaming/WebSocket delivery | `main.go`, `sse.go`, `response_usage_stream.go`, `stream_continuation.go`, WebSocket helpers in `main.go` | Response copy, SSE framing, usage interception, partial/estimated usage, experimental continuation, WebSocket tunnel. |
| Usage/storage | `storage.go`, `usage.go`, `usage_tracking.go`, `usage_state.go`, `usage_attribution.go`, `partial_usage.go` | bbolt buckets, account/user/token aggregates, snapshots, capacity samples, non-secret attribution. |
| Operator/admin/frontend | `frontend.go`, `status.go`, `admin_codex.go`, `admin_claude.go`, `admin_pool_users.go`, `operator_*.go`, `templates/*.html` | Dashboard/status DTOs, account onboarding/deletion, setup/config downloads, local operator flows. |
| Observability/error taxonomy | `metrics.go`, `request_trace.go`, `recent.go`, `upstream_error.go`, `egress_health.go`, `status_display.go` | Metrics, request traces, recent errors, upstream classification, redacted status display. |
| Dev/test assets | `*_test.go`, `scripts/*`, `docs/development.md`, `.gitleaks.toml`, `Makefile`, CI files | Repo safety gates and fixtures. No live credentials. |

Current static size hotspots by line count:

- `templates/local_landing.html`: 4421 lines.
- `main.go`: 4064 lines.
- `status_dashboard_test.go`: 3970 lines.
- `pool.go`: 3144 lines.
- `gemini_operator.go`: 2844 lines.
- `proxy_buffered_test.go`: 2675 lines.
- `templates/friend_landing.html`: 2377 lines.
- `status.go`: 2354 lines.
- `frontend.go`: 2310 lines.
- `main_test.go`: 2291 lines.

These are review/refactor hotspots, not automatic rewrite targets.

## State and secrets boundary

Secret-adjacent state includes:

- provider account files under the configured pool directory;
- `config.toml` and environment variables;
- bbolt DB configured by `PROXY_DB_PATH` / config;
- pool-user storage and token metadata;
- generated setup/config outputs;
- Hermes local session/config artifacts outside product source;
- private egress/proxy configuration.

Repository guardrails:

- `.gitignore` excludes local secret/state surfaces such as `.env`, pool/data/config/db/log/build/local Hermès artifacts and timestamped backups.
- `docs/security-redaction.md` defines forbidden publish material.
- `.gitleaks.toml` preserves default gitleaks rules and uses narrow allowlists for local devloop artifacts, timestamped local backups, and known public fixture material.

Design guardrails:

- virtual user-facing keys may be reduced to ids, names, hash/prefix metadata, policy, and usage attribution; raw keys must not be persisted/logged/displayed after creation.
- upstream account credentials stay in provider/account storage and provider auth-header code; they must not enter status pages, metrics, request traces, docs, or durable Kanban metadata.
- request/response body logging is optional, bounded, and redaction-sensitive.
- reports should use aggregate counts, ids only where operationally non-secret, hashes, prefixes, and status/error classes.

## Recommended strangler-refactor seams

These are future seams; they are not behavior changes from this documentation task.

1. Route table seam: replace the long `ServeHTTP` switch with declarative route registration grouped by public, operator, admin, setup/config, local compatibility, and proxy fallback.
2. Proxy engine seam: split `main.go` proxy lifecycle into admission, planning, selection, upstream attempt, delivery, and finalization modules with narrow contracts.
3. Buffered retry seam: isolate pre-first-byte retry classification and bounded body/status inspection from response delivery.
4. Pool selector seam: isolate account eligibility/scoring from account persistence and provider-specific state mutation in `pool.go`.
5. Response delivery seam: separate buffered delivery, streamed delivery, SSE usage interception, continuation, and WebSocket tunneling.
6. Provider adapter seam: keep `Provider` as the stable port and move managed-seat special cases into provider modules.
7. Status/dashboard seam: keep status DTO assembly separate from HTML/template/dashboard rendering.
8. Storage seam: hide bbolt bucket details behind repository-like methods so usage aggregation can be tested without full proxy setup.
9. Error taxonomy seam: centralize upstream classifications and downstream retry decisions so pre-first-byte vs post-first-byte behavior stays auditable.
10. Test fixture seam: keep high-entropy-looking secret fixtures generated/low-entropy and route all new dev-loop artifacts through scanner gates.

## Non-goals for this map

- no code refactor;
- no service restart, deploy, or live config change;
- no raw credential inspection;
- no account file dump, environment dump, bbolt dump, or Hermes config dump;
- no commit, push, reset, stash, or cleanup of existing dirty worktree entries;
- no broad deletion of suspected dead code without a dedicated evidence/test task.

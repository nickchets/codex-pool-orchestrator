# Unified Observability Wave — Codex и Gemini/Antigravity

## Цель
- Довести observability для `codex` и `gemini` до того же уровня, что уже есть у `claude`, не создавая второй trace-схемы.
- Сохранить один request-scoped spine: `trace start -> route -> response -> chunk/sse -> finish`, а provider-specific события добавлять поверх него.
- Нормализовать локальные correlation headers под provider-neutral нейминг без поломки текущих Claude wrapper flows.

## Scope этой волны
- Provider-neutral inbound trace headers: primary `X-Pool-*`, legacy alias `X-Claude-*`.
- Provider-scoped trace events для:
  - token refresh,
  - auth fallback,
  - probe / seat health classification,
  - Gemini facade transform,
  - buffered retry disposition.
- Стилевое выравнивание только в тронутых функциях: `snake_case key=value`, один `[{reqID}] trace {event}` формат, без новых ad-hoc `Printf`.

## Не-цели
- Новый metrics backend, OpenTelemetry, Prometheus или sampling.
- Большой рефактор operator/admin flows.
- Массовая переделка passthrough/websocket/cache путей в одном diff.

## Required event schema
- Общий prefix: `[{reqID}] trace {event}`
- Обязательные поля по возможности:
  - `provider`
  - `account`
  - `auth_mode`
  - `result`
  - `latency_ms`
  - `error`
- Дополнительные provider-specific поля:
  - `token_refresh`: `profile`
  - `auth_fallback`: `attempted_profile`, `fallbackable`, `next_profile`
  - `probe`: `result`
  - `facade_transform`: `original_path`, `target_path`, `requested_model`, `rewritten_model`, `project_id`
  - `retry_disposition`: `attempt`, `attempt_count`, `status`, `retryable`, `reason`, `refresh_failed`

## Header migration contract
- Primary read/write surface: `X-Pool-Trace-Id`, `X-Pool-Wrapper-Mode`, `X-Pool-Wrapper-Started-At`, `X-Pool-Wrapper-Output-Format`.
- Legacy compatibility: читать и strip'ать `X-Claude-Pool-Trace-Id`, `X-Claude-Wrapper-*`.
- В логах поля остаются provider-neutral: `wrapper_trace`, `wrapper_mode`, `wrapper_started_at`, `wrapper_output`.

## Secret safety
- Не логировать значения `Authorization`, `X-Api-Key`, `x-goog-api-key`, `access_token`, `refresh_token`, `client_secret`.
- В provider events разрешены только id, статусы, классификация ошибок и latency.

## Минимальный verify slice
- `go test -count=1 -run 'TestRequestTrace.*|TestMaybeBuildGeminiCodeAssistFacadeRequest.*|TestGeminiProviderRefreshToken.*|TestCodexProviderRefreshTokenLogsTrace' ./...`
- `go build ./...`

## Acceptance
- `PROXY_TRACE_REQUESTS=1` даёт provider-scoped trace events для Codex/Gemini refresh/probe/facade/retry путей.
- `X-Pool-*` и `X-Claude-*` оба принимаются на входе.
- Новые trace events не меняют control flow.
- Regression coverage фиксирует header compatibility и хотя бы по одному trace-bearing path для Codex и Gemini.

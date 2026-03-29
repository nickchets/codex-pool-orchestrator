# Отчет — REPO-CPO-UX-P3-T59

## Scope

Закрывающий screenshot-first отчет по волне `/` + `/status` после handoff/TZ:

- проверить live landing и diagnostics surface по скриншотам;
- сверить их с `/status?format=json`;
- зафиксировать, что именно вошло в implementation slice;
- оставить repo-local documentation вместо висящего handoff-only хвоста.

## Источники истины

1. Скриншоты live landing после рестарта:
   - `screenshots/status-ui-audit-20260329/landing-desktop.png`
   - `screenshots/status-ui-audit-20260329/landing-mobile.png`
2. Live runtime truth:
   - `GET /status?format=json`
3. Реальная markup/JS реализация:
   - `templates/local_landing.html`
   - `status.go`

## Current-State IA Matrix

### Landing `/`

- provider tabs и summary cards;
- routing focus cards;
- operator actions;
- workspace summary;
- account summary with action buttons;
- setup/export entrypoints.

### Diagnostics `/status`

- dense seat/account truth;
- JSON-aligned deep detail;
- read-only diagnostics wording;
- direct links to dashboard/json/health;
- no onboarding/editorial duplication from the landing surface.

## Mismatch List From The Audit

### Было

1. Длинные Codex seat email/name ломали `Routing Focus`.
2. На landing не было явной поверхности для quota freshness/reset timing по Codex.
3. `/status` по wording и общей подаче оставался промежуточной смесью operator UI и diagnostics detail.
4. OpenCode/Gemini surface по умолчанию жила на `gemini-3.1-pro-high`, но exported model surface оставался уже фактической runtime truth.
5. Codex sticky selection слишком агрессивно выкидывал refreshable expired seat из active/sticky reuse и fallback логики.

### Теперь

1. `Routing Focus` на `/` использует identity-safe cards с переносом длинного seat identity.
2. Codex accounts table на landing получила отдельный `Quota Snapshot` столбец с `updated`, `5h reset`, `7d reset`, `recovery`, `auth`.
3. `/status` переименован в diagnostics surface и явно отделен от dashboard/onboarding wording.
4. OpenCode export по-прежнему держит canonical default `codex-pool/gemini-3.1-pro-high`, но catalog уже показывает более полный Gemini surface, включая `gemini-3.1-pro-low`.
5. Codex sticky/fallback selector теперь сохраняет refreshable expired seat и держит highest available tier вместо старого bucket-drain поведения.

## What Landed In This Slice

### UI / Operator Surface

- `templates/local_landing.html`
  - новые `metric-card-identity` / `routing-card-grid`;
  - Codex `Quota Snapshot`;
  - better mobile/desktop rendering for long seat identity;
  - clearer copy around canonical Gemini path and surfaced Gemini models.

### Diagnostics Surface

- `status.go`
  - `/status` теперь подается как `Pool Diagnostics`;
  - diagnostics wording/tests выровнены с read-only deep-ops ролью.

### Gemini/OpenCode Contract

- `opencode_contract.go`
- `gemini_code_assist_facade.go`
- `opencode_runtime_adapters.go`

Итог:
- default model не менялся;
- `gemini-3.1-pro-low` и другие модели surfaced/exported;
- low-model direct path больше не получает high-only forced thinking/stream behavior.

### Codex Selector Tail

- `pool.go`
- `pool_test.go`

Итог:
- sticky/active Codex seat не выбрасывается только из-за expired access token, если seat refreshable;
- Codex fallback сохраняет highest available tier.

## Residual Risk

- `/status` все еще плотный intentionally; этот срез не превращал его в новый landing.
- Если понадобится следующая IA wave, она должна идти как новый bounded repo-local card, а не как повторное открытие `T59`.

## Closure Statement

`REPO-CPO-UX-P3-T59` больше не является handoff-only карточкой. Screenshot-first audit, report, реализация первой полезной UI slice и release sync на `0.9.0` теперь живут в repo-local SSOT и не висят как незакрытый хвост.

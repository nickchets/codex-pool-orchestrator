# Gemini / Antigravity Audit Plan

Дата: 2026-03-25
Репозиторий: `/home/lap/projects/codex-pool-orchestrator`
Статус: planning packet + `T46` closed before `T47`, без live seat mutation

## 0. Update after first `T46` quota-foundation slice

- Уже приземлен backend-first typed foundation для Gemini provider truth: persisted `protected_models`, typed per-model quota/reset snapshots, protocol-cap fields (`thinking_budget`, `max_output_tokens`, `max_tokens`, `supports_*`), и additive status projection через nested `provider_truth`.
- Срез остался совместимым: legacy `antigravity_quota` и уже существующие flat provider fields не удалялись и не переименовывались.
- Следующий bounded шаг после этого пакета уже частично закрыт: managed Gemini add/probe перестал называть seat просто `healthy`, если refresh прошел, а provider truth остался `missing_project_id` или `validation_blocked`.
- Этот bounded шаг уже закрыт: freshness/staleness policy приземлена additively через `provider_truth.freshness_state`, `provider_truth.stale`, `provider_truth.stale_reason` и `provider_truth.fresh_until`. До `T47` stale truth остается наблюдаемым статусом, а не selector-block reason.

## 1. Нормализованная директива

Провести полный аудит текущей Gemini/Antigravity архитектуры в репо, сверить ее с `Antigravity-Manager` и локальной установленной Antigravity runtime, выделить вероятные концептуальные ошибки вокруг seats, provenance, quota/limits display, Gemini CLI и OpenCode, а затем внести в repo-план только следующие фазы работ, не прерывая активный `T46` и не удаляя текущие Gemini seats на этом шаге.

## 2. Почему это complex

- Задето больше одной поверхности: runtime routing, seat persistence, operator/status truth, Gemini facade, Gemini CLI, OpenCode, live local Antigravity installation.
- Нужна архитектурная сверка, а не один багфикс.
- Есть отложенная destructive-фаза с live seat delete/reimport, которую нельзя запускать без более сильного контракта и rollback.
- Источник правды размазан между текущим Go-репо, локальной Antigravity runtime и upstream `Antigravity-Manager`.

## 3. Scope / non-goals

### Scope

- Gemini seat/account model в нашем репо.
- Provider-truth persistence, routing eligibility, limits/quota display.
- Gemini CLI и OpenCode connectivity contract.
- План controlled reset/reimport current Gemini seats.

### Non-goals

- Никакого удаления Gemini seats в этой волне.
- Никакого live service restart ради этого planning sync.
- Никакого копирования Antigravity как второй control plane внутрь Go-репо.
- Никакой маскировки архитектурной неопределенности красивым UI.

## 4. Current architecture map in our repo

- В репо Gemini seat сейчас по сути является обычным `Account` с примесью `gemini_*` и `antigravity_*` полей, а не отдельной quota-first сущностью.
- `/v1beta/models/*` обычно не идет в “чистый” Gemini Developer API; он переписывается через facade в Google Code Assist `v1internal`, если у seat есть достаточная provider truth, особенно `project_id`.
- На момент аудита оператор действительно нес три Gemini lane: manual import, managed OAuth и Antigravity-flavored OAuth/import. После cleanup public contract остался browser-first через Antigravity auth, а legacy local/service-owned paths рассматриваются только как internal maintenance/runtime compatibility state внутри общего Gemini pool.
- Status и routing пока в значительной степени перегружают одни и те же coarse поля (`health_status`, `rate_limit_until`, generic cooldown/dead state) для задач, которые у Antigravity разведены на quota truth, runtime locks и protocol caps.

## 5. Antigravity Manager comparison findings

- В `Antigravity-Manager` seat фактически равен `Account + TokenData + QuotaData`, а quota и tier truth являются first-class частью account payload, а не опциональным украшением.
- Важные поля там живут как runtime-истина: `project_id`, `quota.subscription_tier`, `quota.models[].percentage`, `quota.models[].reset_time`, `protected_models`, `validation_blocked*`, `proxy_disabled`.
- UI лимитов строится из per-model quota процентов и reset time, а не из generic health.
- Runtime selection у них жестче UI: модель должна быть доступна у аккаунта, затем учитываются tier, quota target model, health score и reset proximity.
- Gemini CLI и OpenCode рассматриваются как отдельные sync/export contracts:
  - Gemini CLI ожидает base URL без `/v1` и `gemini-api-key` auth mode.
  - OpenCode sync нормализует base URL к `/v1`, экспортирует provider config и отдельный `antigravity-accounts.json`, сохраняя часть существующего state.
- У них также явно разведены UI quota%, runtime rate-limit locks и protocol caps (`max_output_tokens`, thinking budget).

## 6. Likely mismatches / risks

1. Мы пока слишком сильно моделируем Antigravity как “обычный Gemini OAuth seat + extra metadata”, а не как quota-first state machine.
2. `project_id` у нас еще недостаточно first-class: для реальной facade/CLI/OpenCode корректности это не просто metadata.
3. У нас provider truth во многом one-shot: onboarding/import может гидратировать поля, но refresh/success recovery не всегда поддерживает свежесть quota/project/validation truth.
4. `/v1beta -> /v1internal` shim пока выглядит как фактический production bridge, но это еще не зафиксировано как explicit contract; отсюда риск misleading dual-lane semantics.
5. Status/UI пока смешивает readiness managed OAuth lane и imported/Antigravity lane, из-за чего operator truth по Gemini limits и readiness получается неполным.
6. Без явного разведения quota truth, runtime locks и protocol caps мы рискуем снова чинить симптомы вместо модели.
7. Если удалить и заново импортировать seats сейчас, мы не сможем надежно объяснить before/after diff по quotas, reset windows, CLI/OpenCode behavior и routing.

## 7. Plan additions

### Фаза A: завершить `T46` как quota-first persistence foundation

Acceptance:

- Seat persistence хранит `project_id`, `subscription_tier`, `protected_models`, per-model quota/reset snapshots, provider freshness timestamps и protocol caps.
- Warm-seat admission и provider truth становятся core runtime data, а не sidecar-логикой.
- `/status?format=json` экспонирует эти поля additively, не ломая текущий контракт.

### Фаза B: выполнить `T47` как sticky-until-pressure routing

Acceptance:

- Gemini candidate pre-filter блокирует `validation_blocked`, not-warmed, stale provider truth, missing `project_id`, quota pressure и cooldown до входа в generic selector.
- Rotation происходит по provider-aware pressure, а не по near-even drain на generic recency.
- Routing reasons выводятся как first-class truth, а не как логовые догадки.

### Фаза C: новый `T49` — freeze quota-first contract

Acceptance:

- В репо явно разведены четыре слоя: auth seat state, provider-truth snapshot, runtime rate-limit locks, protocol caps.
- Зафиксирован статус `/v1beta -> /v1internal`: permanent compatibility shim или temporary bridge.
- Partial-truth состояния описаны явно: `missing_project_id`, `project_only_unverified`, stale provider truth, no target-model quota.

### Фаза D: новый `T50` — Gemini CLI / OpenCode parity

Acceptance:

- Для Gemini CLI и OpenCode зафиксированы и проверены разные base URL rules, auth mode, backup/restore expectations и exported limit semantics.
- Status truth показывает, какой compatibility lane реально активен и чем runtime caps отличаются от static catalog.
- Есть docs/probe hooks, которые ловят drift между sync config и реальным runtime contract.

### Фаза E: вернуть `T44` как truthful operator/status parity

Acceptance:

- Dashboard показывает не только managed/imported counts, но и per-model quota/reset truth, protected groups, project readiness и real lane readiness.
- Imported/Antigravity lane больше не наследует managed-OAuth-only note/copy.
- UI остается projection of runtime truth, а не отдельной семантикой.

### Фаза F: новый `T51` — controlled seat reset / reimport

Acceptance:

- Перед delete есть backup/export, sanitized inventory, before-state `status?format=json`, rollback artifacts и agreed verify gates.
- После reimport `/status`, Gemini CLI и OpenCode показывают ожидаемые quotas, limits, reset windows и compatibility lane.
- Если parity не достигнута, rollback path проверен и понятен.

## 8. Implement / defer / block matrix

### Implement now

- Закрытый pre-`T47` boundary `T46` как уже выполненный additive runtime slice.
- `T47` как ближайший runtime successor.
- Плановые cards `T49/T50/T51` и этот analyst packet.

### Defer

- Любые live Gemini seat deletes.
- Любой reimport ради “посмотреть, станет ли лучше”.
- UI polish поверх неточного runtime contract.

### Block

- Controlled reset/reimport до завершения `T46/T47/T49/T50/T44`.
- Любые окончательные выводы по Gemini limits display без per-model quota/reset truth.
- Любую попытку считать Gemini CLI и OpenCode “просто еще одним клиентом” без выделенного sync contract.

## 9. Verify / evidence hooks

- Repo-local plan sync:
  - `rg -n 'REPO-CPO-ARCH-P1-T49|REPO-CPO-ALIGN-P1-T50|REPO-CPO-VERIFY-P1-T51' ACTION_PLAN.md PROJECT_MANIFEST.md docs/GEMINI_ANTIGRAVITY_AUDIT_PLAN_20260325.ru.md`
- Live local Antigravity truth:
  - `python3 /home/lap/tools/antigravity_pool_guard.py status`
  - `systemctl --user status antigravity-tools.service --no-pager`
- Future runtime proof after implementation:
  - `curl -fsS http://127.0.0.1:8989/status?format=json`
  - targeted `go test` suites for provider truth, facade, dashboard, and CLI/OpenCode contract
  - real Gemini CLI smoke through the pool
  - real OpenCode sync/export proof through the pool

## 10. Main cautions before touching live seats

- Не удалять текущие Gemini seats, пока runtime contract не умеет правдиво показать quota/reset/project truth до и после.
- Не смешивать auth recovery и quota truth в один generic `healthy/unhealthy` flag.
- Не считать `project_id` необязательным полем для imported/Antigravity lane.
- Не обещать operator/UI parity раньше, чем runtime truth действительно готов.
- Не запускать destructive reset/reimport без rollback artifacts и без клиентских proof points для Gemini CLI и OpenCode.

## Источники

- Локальный repo audit: `ACTION_PLAN.md`, `PROJECT_MANIFEST.md`, `status.go`, `provider_gemini.go`, `gemini_operator.go`, `gemini_code_assist_facade.go`.
- Локальная Antigravity runtime: `python3 /home/lap/tools/antigravity_pool_guard.py status`, `systemctl --user status antigravity-tools.service --no-pager`, sanitized reads under `/home/lap/.antigravity_tools/`.
- Upstream `Antigravity-Manager`:
  - `https://github.com/lbjlaq/Antigravity-Manager/blob/main/README_EN.md`
  - `https://github.com/lbjlaq/Antigravity-Manager/blob/main/src-tauri/src/modules/quota.rs`
  - `https://github.com/lbjlaq/Antigravity-Manager/blob/main/src-tauri/src/proxy/token_manager.rs`
  - `https://github.com/lbjlaq/Antigravity-Manager/blob/main/src-tauri/src/proxy/mappers/gemini/wrapper.rs`
  - `https://github.com/lbjlaq/Antigravity-Manager/blob/main/src-tauri/src/proxy/cli_sync.rs`
  - `https://github.com/lbjlaq/Antigravity-Manager/blob/main/src-tauri/src/proxy/opencode_sync.rs`
  - `https://github.com/lbjlaq/Antigravity-Manager/blob/main/src-tauri/src/proxy/common/client_adapters/opencode.rs`

## Примечание по Opus

Opus audit lane был запущен через `/home/lap/.local/bin/opus-run-stream` для этого же брифа. Он успел собрать внешние материалы и локальную карту, но завис после stream-fallback и не отдал финальный analyst packet в рамках этого planning sync. Поэтому текущий документ синтезирован по завершенным explorer lanes, live local evidence и уже существующим governed comparison artifacts; когда Opus lane восстановится, этот пакет можно лишь уточнить, но не переписать с нуля.

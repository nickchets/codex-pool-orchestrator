# Status Truth / Probe Resilience Wave

Дата: 2026-03-25
Репозиторий: `/home/lap/projects/codex-pool-orchestrator`

## Контекст

После live rollout observability-волны остались два резидуальных сигнала:

- `openai_api_pool.healthy_keys = 0` при `eligible_keys = 1` и `health_error = context deadline exceeded`
- `gemini_operator.managed_oauth_available = false` при `managed_seat_count = 0` и `imported_seat_count = 4`

Opus и локальные проверки сошлись: это не баги роутинга, а правдивые degraded/config states.

## Цель Волны

Сделать `/status` самодокументируемым для этих состояний и слегка укрепить probe-path для transient timeout/error без изменения selector/fallback semantics.

## In Scope

- Обогатить `/status` для OpenAI API fallback-пула:
  - показать различие между `selector eligible` и `last probe healthy`
  - добавить явную семантику probe-state для fallback key
  - показать penalty в status-данных
- Сделать `managed_oauth_note` для Gemini operator human-readable и явно указать, что imported seats остаются рабочими
- Поднять timeout probe OpenAI API fallback с 5s до 10s
- Ограничить рост penalty для transient transport/generic probe errors
- Добавить таргетные регрессионные тесты

## Non-Goals

- Любые изменения selector/routing логики fallback key
- Настройка `GEMINI_OAUTH_*` env vars
- Новая метрика/telemetry backend интеграция
- Дублирование seat-level Gemini данных в новый top-level blob, если нужные поля уже есть в `accounts[]`

## План Исполнения

1. Добавить additive status fields и UI copy для truthful degraded state.
2. Обновить Gemini managed OAuth note без изменения operator flow.
3. Усилить только transient probe path: timeout + capped penalty.
4. Прогнать таргетные тесты по dashboard/probe semantics.
5. Записать evidence после верификации.

## Acceptance

- `/status?format=json` явно объясняет, что `healthy_keys` считает только свежий успешный probe, а `eligible_keys` следует selector eligibility.
- Для fallback key в status-данных есть probe-state и penalty.
- При `managed_oauth_available=false` и `imported_seat_count>0` note явно говорит, что imported seats не затронуты.
- Transport/generic probe errors больше не раздувают penalty бесконечно.
- Таргетные тесты на status truth и probe resilience проходят.

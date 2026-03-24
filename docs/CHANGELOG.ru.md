# Журнал изменений

В этом файле фиксируются все заметные изменения форка.

Этот репозиторий является standalone extracted fork поверх `darvell/codex-pool`.
Git-история upstream здесь не сохранена. Документированная база импортированного
Go-ядра: `darvell/codex-pool@4570f6b`.

Правила версионирования описаны в [`VERSIONING.ru.md`](./VERSIONING.ru.md).

## [0.6.1] - 2026-03-24

### Изменено
- Gemini operator surfaces теперь разделяют managed OAuth onboarding и ручной импорт `oauth_creds.json`, а не выдают их за один смешанный flow.
- `/status?format=json` теперь явно показывает Gemini operator truth через `gemini_operator` и `operator_source` у аккаунтов, так что landing и `/status` описывают один и тот же состав пула.
- Если на локальном сервисе не сконфигурирован Gemini OAuth client, managed Gemini CTA теперь честно деградирует в unavailable note вместо того, чтобы выглядеть как ещё один рабочий onboarding path.

### Исправлено
- Ручной импорт Gemini `oauth_creds.json` больше не помечается как fallback/API pool. Импортированные credentials теперь показываются как обычные Gemini seat’ы с явной source-меткой.

## [0.6.0] - 2026-03-24

### Добавлено
- Managed Gemini onboarding на `/` и `/status`: loopback OAuth start/callback, popup/manual-open recovery и fallback-паста `oauth_creds.json`.
- Явная quarantine-логика для long-dead seat’ов и operator-видимость quarantine state в status JSON, status HTML и overview на landing.
- Персистентные Codex usage snapshots с restore на старте, а также локальный cached `/backend-api/codex/models`, чтобы metadata lane меньше зависел от хрупких upstream round-trip’ов.
- Дополнительные focused regression tests для Gemini persistence, Codex usage restore/rotation, quarantine visibility и fallback/request-path поведения.

### Изменено
- Локальная главная страница теперь truthfully зеркалит `/status?format=json`, а не живет как отдельная setup-only поверхность. На `/` собраны live dashboards для `Codex`, `Claude` и `Gemini`, cleanup state, operator actions и delete controls.
- Codex routing теперь удерживает один active local seat до порога, честнее восстанавливает usage state после рестарта, уважает local cooldown окна и не дает retry-path poisoning ломать active lease.
- Codex fallback вынесен в явную operator-логику: fallback API keys health-probe’ятся, отображаются отдельно от local seats и доказанно принимают live traffic, когда локальные Codex seat’ы временно недоступны.
- Managed Gemini и GitLab Claude save/load/reload path теперь сохраняют больше operator-visible runtime state между рестартами и hot reload.

### Исправлено
- Codex seat’ы больше не забывают недавнее quota state после рестарта и не возвращаются сразу в ротацию из-за stale/missing usage snapshots.
- Локальные Codex seat’ы, поймавшие live cooldown, теперь реально выходят из fresh rotation вместо debug-only bypass.
- Active Codex lease больше не переписывается retry-only кандидатами, которые не завершили успешный запрос.
- Landing/status surfaces больше не прячут quarantine и dead-seat cleanup truth только в глубоком `/status`.
- Managed Gemini OAuth client credentials больше не зашиты в репозитории; operator flow теперь ожидает их из локального service environment.

## [0.5.1] - 2026-03-23

### Изменено
- Buffered, streamed и websocket response handling разложены на меньшие явные seam-ы, так что retryable status inspection, copied-response delivery, websocket success recovery и pooled websocket proxy execution больше не смешаны в большие inline handler-блоки.
- Общая pre-copy inspection/replay логика теперь разделяется между streamed и websocket path, при этом transport-specific отличия оставлены явными.
- В repo-local SSOT гидрирован следующий websocket follow-up (`T31`), так что текущая refactor-волна прослеживается от плана до evidence.
- README и install/docs переведены на text-first операторский формат без logo/screenshot-шума и синхронизированы с текущим dashboard-first UI.

### Добавлено
- Focused buffered regression coverage для managed API и GitLab Claude retry/failover путей.
- Общие proxy account snapshot helpers для buffered, streamed и websocket response-path тестов.
- Дополнительное websocket finalizer покрытие для non-`101` successful recovery и failed-handshake no-op поведения.

## [0.5.0] - 2026-03-23

### Добавлено
- GitLab-backed Claude pool с managed Duo direct-access minting.
- Operator-facing onboarding GitLab Claude токенов и видимость пула в `/status`.
- Dashboard-first локальная главная страница с live-вкладками `Codex`, `Claude` и `Gemini`, питающимися от `/status?format=json`.
- Дополнительные operator controls для fallback API keys, GitLab Claude токенов и ручного удаления аккаунтов на локальных dashboard surfaces.
- GitLab-specific поля в status/admin для cooldown, quota backoff counters и direct-access rate-limit сигналов.
- Repo-local инженерные файлы: `ACTION_PLAN.md`, `DEBUG.md`,
  `EVIDENCE_LOG.md`, `PROJECT_MANIFEST.md`.

### Изменено
- Логика proxy admission вынесена из основного request handler.
- Введены явные request-planning контракты для выбора маршрута.
- Для Codex seat включен cutoff при `>= 90%` usage и sticky reuse.
- Объединен ingestion usage из body, headers и stream-путей.
- Вынесена общая логика учета usage из response stream.
- Переиспользована общая retry/error/finalization логика для buffered, streamed и websocket proxy path.
- Локальная главная страница переведена с setup-first режима на provider-dashboard-first operator surface, декоративный hero-блок удален.
- Managed GitLab Claude persistence переведен на один canonical fail-closed serializer, а status/admin rendering — на snapshot-based проход с более короткими lock scope.

### Исправлено
- Обычные non-stream Claude `/v1/messages` ответы теперь попадают в локальные usage totals.
- Streamed и websocket inspection managed-upstream ошибок теперь сохраняет полный client-visible body, не ломая retryable classification.
- Обработка GitLab Claude gateway `402/401/403` теперь корректно ротирует токены, сохраняет cooldown state и не убивает живые source tokens по ложному сценарию.
- Битые успешные GitLab direct-access refresh ответы теперь переводят токен в явный `error` state и очищают stale gateway auth, а не оставляют его ложно healthy.

## [0.4.0] - 2026-03-22

### Добавлено
- OpenAI API fallback pool для Codex.
- Health probing и статус API-ключей.
- Operator UI для добавления и удаления OpenAI API-ключей.
- Маршрутизация для fallback-only managed API accounts.

### Изменено
- Codex routing теперь может переключаться в API key pool, когда subscription seats недоступны.
- `/status` получил operator-видимость и управление API pool.

## [0.3.0] - 2026-03-21

### Изменено
- Уточнены wording и operator-логика `/status`.
- Улучшено отображение auth/refresh timestamps.
- Убран лишний raw/internal шум на локальной operator-странице.

## [0.2.0] - 2026-03-21

### Добавлено
- Поддержка websocket authentication для pooled Codex seats.
- Обнаружение dead seats и автоматический failover для деактивированных Codex-аккаунтов.

### Изменено
- Усилена обработка Codex websocket requests и recovery path.

## [0.1.0] - 2026-03-19

### Добавлено
- Standalone operator-ready обвязка вокруг upstream proxy core.
- `orchestrator/codex_pool_manager.py`.
- `systemd/codex-pool.service`.
- Локальная install/security документация.
- Operator-oriented landing и status flows для локального deployment.

## Заметки о расхождении с upstream

- Импортированная upstream-база: `darvell/codex-pool@4570f6b`
- Актуальный upstream на момент сравнения: `darvell/codex-pool@cf782a7`
- Форк намеренно более operator-centric и Codex-centric, чем upstream.
- В upstream могут быть более новые generic provider features, которые сюда не переносились.

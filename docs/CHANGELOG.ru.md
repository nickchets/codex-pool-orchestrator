# Журнал изменений

В этом файле фиксируются все заметные изменения форка.

Этот репозиторий является standalone extracted fork поверх `darvell/codex-pool`.
Git-история upstream здесь не сохранена. Документированная база импортированного
Go-ядра: `darvell/codex-pool@4570f6b`.

Правила версионирования описаны в [`VERSIONING.ru.md`](./VERSIONING.ru.md).

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

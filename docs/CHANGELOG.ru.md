# Журнал изменений

В этом файле фиксируются все заметные изменения форка.

Этот репозиторий является standalone extracted fork поверх `darvell/codex-pool`.
Git-история upstream здесь не сохранена. Документированная база импортированного
Go-ядра: `darvell/codex-pool@4570f6b`.

Правила версионирования описаны в [`VERSIONING.ru.md`](./VERSIONING.ru.md).

## [Unreleased] - 0.5.0-dev

### Изменено
- Логика proxy admission вынесена из основного request handler.
- Введены явные request-planning контракты для выбора маршрута.
- Для Codex seat включен cutoff при `>= 90%` usage и sticky reuse.
- Объединен ingestion usage из body, headers и stream-путей.
- Вынесена общая логика учета usage из response stream.

### Добавлено
- Регрессионные тесты для admission, request planning и usage ingestion.
- Repo-local инженерные файлы: `ACTION_PLAN.md`, `DEBUG.md`,
  `EVIDENCE_LOG.md`, `PROJECT_MANIFEST.md`.

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

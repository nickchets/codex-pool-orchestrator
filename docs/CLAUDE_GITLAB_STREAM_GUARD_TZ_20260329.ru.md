# Claude GitLab Stream Guard TZ — 2026-03-29

## Контекст
- Симптом в Claude Code: ответ визуально уже готов, но CLI еще десятки секунд "думает" и выглядит зависшим.
- Живые логи пула показали отдельный класс GitLab Claude SSE-потоков, где полезные `content_block_delta` уже пришли, но upstream не присылает terminal `message_stop` и продолжает слать только `ping`.
- Старый `stream_idle_timeout` был байтовым watchdog'ом: `ping` считался активностью и продлевал stream, поэтому пул не завершал деградировавший хвост.

## Цель
- Убрать ложное "подвисание после готового ответа" для GitLab Claude в pooled path без регрессии для штатных Claude SSE-потоков.

## Функциональные требования
1. Semantic stall guard включается только для `AccountTypeClaude` с `auth_mode=gitlab_duo`.
2. Guard не вмешивается в нормальный поток, если terminal `message_stop` пришел штатно.
3. Guard может завершить только ping-only tail после уже увиденных `content_block_delta` + `content_block_stop`.
4. Ранний cutoff считается успешным завершением ответа, а не транспортной ошибкой.
5. Успешный cutoff не должен загрязнять `recent` errors, error metrics или dead/rate-limit bookkeeping.

## Нефункциональные требования
1. Решение должно быть прозрачно диагностируемым:
   - startup-конфиг публикует `claude_ping_tail_timeout`,
   - request trace фиксирует `claude_tail_guard_enabled`,
   - request trace фиксирует `claude_tail_cutoff` с `account`, `stalled_ms`, `last_non_ping_type`, `timeout_ms`,
   - финальный лог содержит явную причину успешного раннего завершения.
2. Решение должно быть узким:
   - не менять поведение OAuth Claude,
   - не менять Codex/Gemini stream semantics в этой волне.
3. Решение должно быть безопасным:
   - cutoff не должен срабатывать до `content_block_stop`,
   - cutoff не должен срабатывать после `message_stop`,
   - non-ping событие после `content_block_stop` должно сбрасывать stall timer.

## Acceptance / Verify
- Focused Go tests на watcher/finalizer проходят.
- Full `go test ./...` и `go build ./...` проходят.
- После `systemctl --user restart codex-pool.service` сервис поднимается здоровым.
- Live Claude smoke через pooled token возвращает `200`.
- Live trace показывает `claude_tail_guard_enabled` для GitLab Claude и не показывает ложный cutoff на штатном `message_stop` запросе.

## Non-goals этой волны
- Не лечить общий класс GitLab upstream `502/504`.
- Не смешивать эту работу с унификацией Codex/Gemini observability; это отдельная следующая волна.

## Residual Risk
- Остается отдельный инцидентный класс: upstream `502/504` и сервисные рестарты могут выглядеть как зависание, но это уже не тот же semantic tail path.

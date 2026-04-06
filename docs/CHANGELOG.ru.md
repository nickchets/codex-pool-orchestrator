# Журнал изменений

В этом файле фиксируются все заметные изменения форка.

Этот репозиторий является standalone extracted fork поверх `darvell/codex-pool`.
Git-история upstream здесь не сохранена. Документированная база импортированного
Go-ядра: `darvell/codex-pool@4570f6b`.

Правила версионирования описаны в [`VERSIONING.ru.md`](./VERSIONING.ru.md).

## [0.10.4] - 2026-04-06

### Изменено
- Public bundle export теперь использует детерминированный tar-based copy path вместо прежнего `rsync`, который на этой машине мог зависать в uninterruptible IO wait во время локальной release-проверки.
- Сборка dashboard теперь разрезана на меньшие helper-stage для current-seat selection, Gemini/GitLab enrichment, workspace grouping, provider summaries и fallback pool bookkeeping без изменения контракта `/status` JSON/HTML.

### Исправлено
- Buffered retry handling теперь использует общий helper bookkeeping, уменьшая branch drift в prestream retry-путях.
- GitLab Claude shared-cooldown reads теперь идут через общий snapshot helper, так что recovery polling, cooldown gating и status path больше не дублируют lock/read/filter logic.

### Внутреннее
- Kimi и MiniMax теперь делят общий API-key provider base, а повторяющиеся setup/opencode response-body helpers сведены к общим utility.
- Persisted account health fields теперь записываются через один shared helper вместо повторной сериализации в нескольких save-path.

## [0.10.3] - 2026-04-06

### Добавлено
- Для GitLab Claude shared-TPM recovery появился per-scope canary schedule, а dashboard и account-status теперь показывают последний canary result прямо в operator-visible surface.
- Runtime metrics теперь считают именованные retry/recovery события и provider TTFB buckets, чтобы оператор мог отличать prestream retry churn от downstream latency.

### Изменено
- Fresh-выбор Codex seat'а теперь резервирует выбранный seat до старта caller work и предпочитает меньший inflight для новой работы, чтобы не было duplicate concurrent picks на одном seat'е.
- Health rendering для GitLab Claude теперь очищает stale shared-cooldown noise, как только seat снова становится eligible, но сохраняет видимый recovery-canary state.

### Исправлено
- Local Codex streamed usage-limit failures теперь переходят в persist'ed cooldown state вместо того, чтобы оставлять seat ложно healthy после SSE failure event.
- Managed OpenAI API usage-limit text теперь классифицируется как retryable rate-limit, а не схлопывается в dead-key style quota failure.

## [0.10.1] - 2026-03-30

### Изменено
- Опубликованное дерево репозитория стало стерильнее: repo-local governance, handoff, audit и closure-spec документы больше не поставляются в `main`.
- Правила экспорта public bundle теперь совпадают с опубликованным деревом и сохраняют документированный helper `orchestrator/codex_pool_manager.py`, а не считают его private packaging residue.

## [0.10.2] - 2026-03-30

### Исправлено
- Propagation shared org-TPM cooldown для GitLab Claude теперь опирается на live entitlement headers, а не схлопывает все `gitlab.com` Claude Duo seat'ы в один синтетический cooldown bucket.
- Pool-side GitLab Claude routing больше не объявляет здоровые sibling seat'ы недоступными только потому, что другой seat из другой entitlement-группы поймал shared TPM limiter.

### Проверено
- Live per-seat probes теперь честно отделяют один реально мертвый `claude_gitlab` seat (`402 insufficient_credits`) от остальных здоровых seat'ов вместо flattening всего provider lane в `rate_limited`.

## [0.10.0] - 2026-03-30

### Добавлено
- Опциональный выделенный GitLab Codex sidecar-лейн для Codex CLI: `PROXY_FORCE_CODEX_REQUIRED_PLAN=gitlab_duo`, discovery-backed каталог моделей GitLab Codex, `systemd/codex-pool-gitlab.service` и изолированный `clcode` setup/bootstrap без мутации основного `~/.codex`.
- Onboarding pool-user теперь отдает отдельный `clcode_setup` URL, а `orchestrator/codex_pool_manager.py` умеет напрямую bootstrap'ить этот изолированный лейн.

### Изменено
- Экспорт OpenCode теперь по умолчанию ставит `codex-pool/gemini-3.1-flash-lite` как более безопасный pooled Gemini baseline, при этом сохраняя в каталоге `gemini-3.1-pro-high`, `gemini-3.1-pro-low` и более широкий live Gemini model catalog.
- Выделенный GitLab Codex sidecar теперь локально обслуживает auxiliary endpoint'ы Codex CLI для plugins/connectors/WHAM вместо того, чтобы прокидывать их в upstream GitLab.

### Исправлено
- GitLab Claude org-level TPM `429` теперь превращается в scoped shared cooldown с честным `Retry-After` вместо fan-out по sibling GitLab Claude seat'ам и деградации в `503 no live claude accounts`.
- Ошибки GitLab Codex `402 USAGE_QUOTA_EXCEEDED` и gateway `403` теперь классифицируются как cooldown state; когда все пригодные GitLab Codex seat'ы находятся в cooldown, sidecar возвращает локальный `429`, а не шумный `502`/`503`.
- Refresh model-catalog в `clcode` теперь читает вложенный `tokens.access_token` из `auth.json`, а `/v1/models` теперь идет через тот же cached GitLab Codex catalog path, что и `/backend-api/codex/models`.
- Truth для fallback-project в Antigravity Gemini теперь точнее сохраняется для restricted/projectless seat'ов, а OpenCode bundle metadata удерживает канонические названия для известных Gemini моделей.

## [0.9.0] - 2026-03-29

### Изменено
- Локальный operator contract теперь считает `/` канонической onboarding/dashboard surface, а `/status` закреплен как read-only diagnostics page, работающая от того же `/status?format=json` truth.
- Landing теперь корректно держит длинные Codex seat identity, показывает отдельный `Quota Snapshot` с freshness/reset timing и оставляет OpenCode на `codex-pool/gemini-3.1-pro-high`, одновременно экспортируя более полный live Gemini model catalog.

### Исправлено
- Refreshable expired Codex seat'ы больше не выпадают из sticky reuse и best-seat fallback только из-за expired access token; fallback теперь сохраняет highest eligible tier вместо прежнего более равномерного drain по слабым seat'ам.
- Screenshot-first closure wave больше не висит на temp-only proof: release docs/evidence теперь ссылаются на постоянные артефакты `screenshots/status-ui-audit-20260329/`, а не на `.tmp`-хвосты.

## [0.8.7] - 2026-03-28

### Исправлено
- Канонический маршрут `/operator/gemini/oauth-start` теперь действительно ведет в browser-auth Gemini onboarding handler, а не в retired legacy Gemini OAuth path, так что опубликованный исходник снова совпадает с уже проверенными operator UI и живым бинарем.
- Public bundle export теперь исключает и оставшиеся closure-spec артефакты вместе с прочим planning residue, чтобы публикуемая поверхность оставалась чище.

## [0.8.6] - 2026-03-28

### Изменено
- Выбор Codex seat'а теперь использует стабильный score-first порядок для одинаково пригодных subscription seat'ов, поэтому cold-start round-robin offset больше не сжигает другой seat, пока лучший seat все еще находится в пределах задуманной sticky/headroom policy.
- Operator-facing Gemini/OpenCode surface теперь последовательно описывают канонический browser-auth Gemini lane и экспортируют `codex-pool/gemini-3.1-pro-high` через `pool-gemini-accounts.json`, а legacy Gemini auth path оставлен только как compatibility alias.

### Исправлено
- Codex OAuth seat'ы больше не помечаются dead вслепую на `invalid_grant` или `refresh_token_reused`; теперь пул сначала пробует текущее `/backend-api/codex/models` access и сохраняет `health_status=refresh_invalid` для seat'ов, которые еще реально живы.
- Codex OAuth health/runtime state теперь truthfully переживает save, reload, force-refresh и status rendering, включая сохранение `last_used`, `last_healthy_at` и operator-visible health lines.
- Канонический маршрут `POST /operator/gemini/oauth-start` теперь действительно запускает обещанный operator UI browser-auth Gemini flow, а не проваливается в старый managed-OAuth handler.

## [0.8.5] - 2026-03-28

### Изменено
- Refresh truth для browser-auth Gemini теперь становится proactive за один poll interval до `fresh_until`, поэтому ready seat’ы больше не выпадают в `stale_provider_truth` между плановыми refresh-циклами.
- `/status`, `/status?format=json` и родственные status-style JSON surface теперь поднимают Gemini cooldown seat’ы в top-level `health_status="cooldown"` вместо misleading generic `healthy`.

### Исправлено
- Warmed browser-auth Gemini seat’ы, которые всё ещё сходятся в `provider_truth.state=missing_project_id`, теперь truthfully экспортируются и в status, и в OpenCode quota rows: они остаются `degraded_enabled` с fallback-project reason, а их Gemini quota models сохраняют `routable=true`.
- Operator-facing Gemini status больше не противоречит runtime truth, показывая `health_status=healthy` для seat’ов, которые реально находятся в `operational_truth.state=cooldown` и `routing.state=degraded_enabled`.

## [0.8.4] - 2026-03-28

### Изменено
- `/status?format=json` теперь экспортирует `provider_truth.rate_limit_reset_times` для Gemini seat’ов, а объединенные quota rows напрямую показывают live reset time для model-specific cooldown из runtime state.
- Gemini operator `seat-smoke` теперь возвращает `requested_model_key`, `requested_model_limited`, `requested_model_recovery_at` и live-карту `rate_limit_reset_times`, так что aliasing модели и cooldown именно запрошенной модели видны в одном ответе.

### Исправлено
- Gemini `429 RESOURCE_EXHAUSTED` на одной routed-модели больше не отравляет весь seat глобальным cooldown state, если seat остается пригодным для других Gemini моделей.
- Export для OpenCode теперь сохраняет такие seat’ы включенными и передает model-specific reset windows вместо отключения всего seat’а из-за cooldown одной модели.

## [0.8.3] - 2026-03-27

### Изменено
- Warmed browser-auth Gemini seat’ы с `provider_truth_state=missing_project_id` теперь остаются в `degraded_enabled`, если fallback Code Assist project реально пригоден для работы, вместо жесткой блокировки despite successful operational proof.

### Исправлено
- Routing truth, `/status` и downstream exports больше не противоречат live Gemini seat smoke для fallback-project seat’ов, которые реально отвечают на запросы даже без сохраненного provider project id.

## [0.8.2] - 2026-03-27

### Изменено
- HTML-дашборд `/status` теперь показывает те же Gemini per-model quota rows, что и локальный landing: reset time, ключевые model limits/capabilities, provider tags и явные state-метки `routable` / `seat-blocked` / `catalog-only` для каждой модели.

### Исправлено
- Видимость Gemini quota на `/status` больше не обрывается на summary-строке, из-за чего раньше было не видно, какие модели реально routable, какие блокируются состоянием seat’а, а какие остаются только catalog-only.

## [0.8.1] - 2026-03-27

### Изменено
- Локальный landing теперь показывает Gemini quota не только как summary-счетчики, но и как per-model rows с reset time, состоянием `routable`/`catalog-only`, protected-флагами и ключевыми model capabilities.

### Исправлено
- Нормализация browser-auth Gemini quota теперь считает каноничным outer key из `fetchAvailableModels`, поэтому placeholder-поля `model` внутри entry больше не схлопывают живые quota snapshots в `0 models captured`.
- Логи refresh-пути Gemini теперь показывают реальное число hydrated quota models, а не вводящий в заблуждение count top-level quota keys.

## [0.8.0] - 2026-03-27

### Добавлено
- Browser-first Gemini onboarding через Gemini Browser Auth на `/` и `/status` с сохранением provider-truth для project identity, subscription tier, protected models, typed quota snapshots и warm-seat state.
- OpenCode export и guided setup surfaces (`/config/opencode/<token>` и `/setup/opencode/<token>`) плюс Anthropic-compatible Gemini adapters для `/v1/messages` и `/v1/chat/completions`.
- Loopback-only Gemini operator diagnostics и reset tooling: seat smoke, `reset-bundle`, `reset-delete`, `reset-rollback` с manifest snapshots и rollback artifacts.
- Provider-scoped request tracing для OAuth exchange, token refresh, health probe, facade routing и metadata-cache событий across Codex, Claude и Gemini lanes.

### Изменено
- Gemini routing теперь работает в sticky-until-pressure режиме и перед ротацией проверяет provider truth, warm-seat state, quota pressure, project availability и observed operational failure.
- `/status`, `/status?format=json`, landing, direct Gemini API-key setup и OpenCode export теперь проецируют один и тот же контракт: `provider_truth`, `operational_truth`, `routing.state`, `gemini_pool`, `provider_quota_summary` и compatibility lane.
- Прямой Gemini API-key setup теперь удерживает клиентов на pool root URL в external API-key mode, а legacy local/manual Gemini import убран с operator surface в пользу Gemini Browser Auth.
- Codex route readiness, models-cache fetches, OAuth exchange, API-key probes и refresh flows теперь публикуют trace data и сохраняют точный cutoff `>= 90%` вместе со sticky reuse seat'а.

### Исправлено
- Gemini seat'ы после рестарта теперь честнее обновляют stale provider truth и empty-quota snapshots вместо того, чтобы ронять eligible seats в stale-routing dead end.
- OpenCode export больше не позволяет blocked `missing_project_id` seat'у захватывать `activeIndex`; disabled seat остаётся видимым, но не становится активным аккаунтом.
- Gemini reset rollback теперь валидирует только operator-managed paths перед delete/restore, закрывая path-traversal gap в reset tooling.
- Restricted browser-auth Gemini seat'ы теперь можно диагностировать и, когда это допустимо, прогонять через fallback project без схлопывания provider restrictions в generic operational failure.

## [0.7.0] - 2026-03-25

### Добавлено
- Gemini Browser Auth onboarding на `/` и `/status`: browser OAuth start/callback, нормализация импорта browser-auth account JSON, bootstrap Code Assist project и сохранение provider-truth полей, нужных для маршрутизации.
- Facade для pooled Gemini `/v1beta/models/*:generateContent|streamGenerateContent`, который переписывает поддержанные запросы в Code Assist `v1internal` lane для импортированных browser-auth Gemini seat’ов.
- Сквозная Claude request-trace корреляция от wrapper до pool: trace headers, счетчики SSE/usage events, детекция `chunk_gap` и явная диагностика idle-timeout.
- Focused regression coverage для Gemini provider persistence, Gemini Browser Auth onboarding, dashboard/operator JSON truth, setup scripts, facade transforms и request-trace поведения.

### Изменено
- Setup scripts для прямого Gemini API-key клиента теперь удерживают клиент в external API key mode через `GEMINI_API_KEY` и `GOOGLE_GEMINI_BASE_URL`, а не через прежнюю OAuth-bypass схему env-переменных.
- Gemini seat persistence и routing теперь сохраняют provenance/operator source и provider block-state поля: `proxy_disabled`, `validation_blocked`, quota-forbidden state, subscription tier и validation metadata.
- `/status`, `/status?format=json` и landing теперь описывают Gemini seat’ы с browser-auth provenance и provider-truth полями вместо того, чтобы сводить все non-managed seat’ы к одной generic manual-import lane.
- Явные operator force-refresh path теперь обходят Gemini per-account refresh throttle, когда оператор действительно просит реальный refresh.

### Исправлено
- Gemini `v1beta` запросы больше не попадают на импортированные seat’ы без provider project id, который обязателен для Code Assist facade.
- Детекция idle SSE timeout теперь сохраняет реальный timeout state вместо того, чтобы растворяться в generic downstream `context canceled`.
- Local Claude tracing теперь можно коррелировать end-to-end без утечки wrapper-only headers в upstream.

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
- Следующий websocket follow-up (`T31`) теперь задокументирован так, чтобы текущая refactor-волна прослеживалась end-to-end.
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
- Standalone deployment assets вокруг upstream proxy core.
- `systemd/codex-pool.service`.
- Install/security документация.
- Operator-oriented landing и status flows для self-hosted deployment.

## Заметки о расхождении с upstream

- Импортированная upstream-база: `darvell/codex-pool@4570f6b`
- Актуальный upstream на момент сравнения: `darvell/codex-pool@cf782a7`
- Форк намеренно более operator-centric и Codex-centric, чем upstream.
- В upstream могут быть более новые generic provider features, которые сюда не переносились.

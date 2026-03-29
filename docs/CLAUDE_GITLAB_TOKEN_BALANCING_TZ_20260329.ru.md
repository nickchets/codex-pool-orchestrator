# Claude GitLab Token Balancing TZ — 2026-03-29

## Контекст
- Для `gitlab_duo` Claude seat'ов текущий unpinned selector исторически лип к `LastUsed`: самый недавно использованный токен выбирался снова и снова.
- Это приводило к неравномерному burn одного GitLab токена, хотя пул должен распределять нагрузку между живыми seat'ами ровнее.
- Параллельный аудит показал, что GitLab-поля `RateLimit-Limit/Remaining/Reset` у нас отражают direct-access/token-mint слой и не являются надежной метрикой serving quota для `/v1/messages`.

## Цель
- Для unpinned GitLab Claude запросов распределять нагрузку по токенам равномернее и предсказуемее, не ломая Codex/Gemini и не вводя ложные quota assumptions.

## Безопасные routing inputs
1. `Dead`
2. `Disabled`
3. `missing_gateway_state`
4. `RateLimitUntil` после настоящего `429`/rate-limit disposition
5. `Inflight`
6. `LastUsed`
7. локальный round-robin start order

## Небезопасные routing inputs
1. `GitLabRateLimitName`
2. `GitLabRateLimitLimit`
3. `GitLabRateLimitRemaining`
4. `GitLabRateLimitResetAt`
5. `GitLabQuotaExceededCount`
6. `GitLabLastQuotaExceededAt`

Эти поля годятся для observability/status, но не как primary balancing truth для `/v1/messages`.

## Policy
1. Conversation pinning сохраняется без изменений.
2. Для unpinned `gitlab_duo` Claude отключается generic sticky reuse по “самый свежий `LastUsed`”.
3. GitLab Claude selector выбирает seat по следующему порядку:
   - меньший `Inflight`,
   - более старый `LastUsed`,
   - затем обычный score/RR tie behavior.
4. Selector не блокирует seat только потому, что direct-access headers показали `GitLabRateLimitRemaining=0`.
5. Реальный `429` продолжает выставлять `RateLimitUntil`, и именно это остается hard cooldown truth.

## Hardening
1. GitLab cooldown теперь использует общий `Retry-After` parser, а не только seconds-only parsing.
2. Это делает cooldown path корректным и для секунд, и для HTTP-date формата.

## Acceptance
- Unit tests подтверждают:
  - LRU selection для GitLab Claude,
  - rotation across never-used seats,
  - lower `Inflight` priority,
  - отсутствие ложной блокировки только из-за direct-access header snapshot,
  - корректный `Retry-After` HTTP-date cooldown parsing.
- Full `go test ./...`, `go build ./...`, итоговая сборка бинарника и сервисный restart проходят.

## Residual Risk
- Текущий live `503 no live claude accounts` может происходить не из-за selector-а, а из-за реального org-level `429` cooldown на всех GitLab seat'ах одновременно.
- Это отдельный operational path; balancing fix его не маскирует.

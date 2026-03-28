# ТЗ: Финальное закрытие repo-local волны T57

Дата: `2026-03-28`

## Цель

Закрыть текущую Gemini pool/OpenCode волну `T57` полностью на уровне репозитория, без ложных открытых хвостов в board/docs/git.

## In Scope

- подтвердить, что все repo-local изменения `T57` уже реализованы, проверены и опубликованы в `0.8.6`;
- перевести `T57` из `DOING` в `DONE` в [ACTION_PLAN.md](/home/lap/projects/codex-pool-orchestrator/ACTION_PLAN.md);
- зафиксировать в evidence, что оставшийся live `missing_project_id` seat не является repo-local defect;
- довести git до состояния `main...origin/main` без незакоммиченных хвостов.

## Non-Goals

- не пытаться “чинить” сам живой аккаунт с `provider_truth.state=missing_project_id`;
- не менять selector/routing только ради более красивой картинки;
- не открывать новую широкую волну по historical/model-specific cooldown visibility.

## Правило истины

Repo-local `T57` считается закрытым, если одновременно верны все пункты:

1. live pool truth стабилен:
   - `gemini_pool.total_seats=5`
   - `gemini_pool.eligible_seats=5`
   - cooldown seats показываются как `health_status="cooldown"`
   - warmed fallback-project seat остаётся `degraded_enabled`
2. export/status truth не противоречит runtime truth.
3. изолированный `opencode` smoke через экспортируемый pool bundle проходит на свежем бинаре.
4. рабочее дерево чистое, commit опубликован в `origin/main`.

## Критерии приемки

- [ACTION_PLAN.md](/home/lap/projects/codex-pool-orchestrator/ACTION_PLAN.md) не содержит ложного `DOING` по `T57`;
- [EVIDENCE_LOG.md](/home/lap/projects/codex-pool-orchestrator/EVIDENCE_LOG.md) содержит явную closure-запись;
- git status чистый;
- `origin/main` указывает на опубликованный closure-commit;
- оставшийся `missing_project_id` residue явно классифицирован как operational/provider follow-up, а не как незавершённая работа репозитория.

## Verify Hook

```bash
cd /home/lap/projects/codex-pool-orchestrator
git status --short --branch
curl -fsS http://127.0.0.1:8989/status?format=json | jq '{gemini_pool:.gemini_pool,accounts:[.accounts[]|select(.type=="gemini")|{id,health_status,routing:.routing,provider_state:.provider_truth.state,operational_state:.operational_truth.state}]}'
python3 /home/lap/tools/codex_pool_manager.py status --strict
TMPDIR="$(mktemp -d)"
env XDG_CONFIG_HOME="$TMPDIR/config" XDG_DATA_HOME="$TMPDIR/data" AGCODE_RECREATE_USER=1 agcode --agcode-setup-only
env XDG_CONFIG_HOME="$TMPDIR/config" XDG_DATA_HOME="$TMPDIR/data" timeout 180s opencode run -m codex-pool/gemini-3.1-pro-high 'Reply with exactly T57_CLOSE_OK.'
git rev-list --left-right --count origin/main...main
```

## Решение по остаточному residue

Один live seat с `provider_truth.state=missing_project_id` остаётся частью runtime truth, но больше не считается repo-local tail, потому что:

- fallback-project lane уже реализован;
- status/export truth уже согласованы;
- routing больше не ломается из-за этого состояния;
- дальнейшее исправление зависит от повторной provider-side/project-side авторизации, а не от нового code diff в репозитории.

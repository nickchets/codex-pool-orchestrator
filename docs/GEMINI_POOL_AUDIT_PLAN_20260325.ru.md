# Gemini Pool Audit Plan

Дата: `2026-03-25`
Репозиторий: `/home/lap/projects/codex-pool-orchestrator`
Статус: historical planning packet, superseded by the current pool-backed OpenCode contract

## Назначение

Сохранить сжатую архитектурную выжимку по той planning wave, которая привела к нынешнему Gemini pool contract, не оставляя устаревшие manager-specific названия в активной документации.

## Актуальный контракт

- Канонический операторский execution path: `opencode run -m codex-pool/gemini-3.1-pro-high ...`.
- `agcode` допускается только как локальный setup/convenience shim для получения экспортируемого OpenCode bundle.
- Единственный поддерживаемый operator-facing onboarding path для Gemini seats: `Gemini Browser Auth`.
- Runtime truth для Gemini должна быть разведена по трем слоям:
  - `provider_truth`
  - `operational_truth`
  - `routing.state`

## Что эта planning wave зафиксировала

1. Gemini seat нельзя трактовать как generic account c несколькими косметическими полями. Для корректного pool routing нужны first-class `project_id`, typed quota snapshots, protected-model truth и freshness timestamps.
2. Status/UI не должен изобретать отдельную семантику поверх runtime. Он обязан быть projection of runtime truth, а не второй control plane.
3. Клиентские контракты различаются:
   - direct Gemini API-key lane работает через pool root URL;
   - OpenCode lane работает через `/v1` export + отдельный pool accounts file.
4. Любая дальнейшая работа по quota, cooldown и compatibility должна оставаться quota-first, а не health-label-first.

## Принятые successor phases

### Phase A

Закрыть quota-first persistence foundation так, чтобы per-model quota/reset truth и provider freshness стали persisted runtime data.

### Phase B

Сделать provider-aware routing: stale truth, missing project, cooldown и quota pressure должны блокировать seat до generic selector phase.

### Phase C

Заморозить явный contract между `provider_truth`, `operational_truth`, `routing.state` и exported client config.

### Phase D

Проверить client parity:

- direct Gemini API-key lane
- OpenCode lane
- exported limits / active-seat truth

### Phase E

Держать operator/dashboard parity как следствие runtime truth, а не как самостоятельный источник решений.

## Что считается вне scope

- Любые destructive seat reset/reimport операции без rollback artifacts.
- Любая попытка вернуть retired manager-specific lane как active operator contract.
- Любая документация, где wrapper или historical comparison tooling выглядит как primary execution path.

## Проверка

- `rg -n 'REPO-CPO-ARCH-P1-T49|REPO-CPO-ALIGN-P1-T50|REPO-CPO-VERIFY-P1-T51' ACTION_PLAN.md PROJECT_MANIFEST.md docs/GEMINI_POOL_AUDIT_PLAN_20260325.ru.md`
- `curl -fsS http://127.0.0.1:8989/status?format=json`
- targeted `go test` suites for provider truth, dashboard truth, and OpenCode export
- isolated OpenCode smoke through the exported pool bundle

## Источники

- `ACTION_PLAN.md`
- `PROJECT_MANIFEST.md`
- `status.go`
- `provider_gemini.go`
- `gemini_operator.go`
- `gemini_code_assist_facade.go`
- runtime probes captured in repo-local evidence for the same wave

## Примечание

Этот пакет сохранен только как historical architecture checkpoint. Если он расходится с текущими `README.md`, `docs/install.md`, `ACTION_PLAN.md` или live runtime proof, приоритет всегда у текущего pool-backed OpenCode contract.

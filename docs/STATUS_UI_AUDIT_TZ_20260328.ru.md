# ТЗ — REPO-CPO-UX-P3-T59

## Цель

Собрать screenshot-first аудит веб-интерфейса `codex-pool-orchestrator`, чтобы перестать смешивать две разные поверхности:

- `/` как dashboard-first landing с вкладками и краткой операторской сводкой;
- `/status` как плотный deep-ops экран для детальной диагностики.

## Исходный операторский запрос

- Основной источник истины для этого среза: скриншоты, а не DOM.
- `/status` сейчас воспринимается как мешанина.
- Ключевая информация должна быть видна на главной странице, во вкладках.
- Нужно сохранить truthfulness по Gemini/Codex/Claude limits/accounts/health и не придумывать вторую семантику поверх runtime truth.

## Что входит в срез

1. Снять screenshot-first инвентаризацию `/` и `/status` на desktop/mobile.
2. Выписать, какая информация:
   - должна жить на `/` как operator summary;
   - должна остаться только на `/status` как deep-ops detail;
   - дублируется зря или конфликтует по wording/иерархии.
3. Собрать mismatch-список между:
   - live runtime truth (`/status?format=json`);
   - текущей landing/tab IA;
   - текущим `/status` HTML.
4. Сформулировать целевую IA:
   - верхнеуровневые вкладки на `/`;
   - обязательные summary blocks на landing;
   - что переносится/сворачивается/переименовывается на `/status`.
5. Разбить внедрение на маленькие execution slices без giant redesign.

## Не входит

- Немедленный большой UI rewrite.
- Изменение routing/runtime semantics ради красоты.
- Новая параллельная operator truth-модель вне `/status?format=json`.

## Артефакты этого среза

- screenshot bundle по `/` и `/status`;
- краткая current-state IA matrix;
- mismatch list;
- target IA sketch;
- phased implementation plan.

## Acceptance

- Есть один repo-local card для этой волны: `REPO-CPO-UX-P3-T59`.
- Root handoff больше не держит этот запрос на `ROOT-E30-S1-T5`.
- Следующий агент может продолжить UI-аудит из repo-local SSOT без возврата в synthetic root auto-intake.

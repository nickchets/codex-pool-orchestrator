# Handoff Packet — REPO-CPO-UX-P3-T59

- Сформирован: `2026-03-28`
- Активный репозиторий: `/home/lap/projects/codex-pool-orchestrator`
- Repo-local card: `REPO-CPO-UX-P3-T59`
- Root handoff card: `ROOT-E30-S1-T6`
- Parent directive: `DIR-20260328-040`
- Базовое ТЗ: `docs/STATUS_UI_AUDIT_TZ_20260328.ru.md`

## Objective

Превратить пост-close операторский запрос про `/` + `/status` в один repo-local screenshot-first IA audit/spec: что должно жить на landing с вкладками, а что должно остаться на плотном deep-ops `/status`.

## Current Truth

- Root больше не держит этот запрос на `ROOT-E30-S1-T5`; handoff зафиксирован на `ROOT-E30-S1-T6`.
- Repo-local board держит одну следующую карточку для этой волны: `REPO-CPO-UX-P3-T59`.
- Семантический источник runtime truth не меняется: `/status?format=json` остается каноническим data surface, а UI-аудит не придумывает вторую truth-модель.

## Required Inputs

- Скриншоты `/` и `/status` важнее DOM-reading и словесных предположений.
- Нужно смотреть обе поверхности: desktop и mobile.
- Для сверки использовать три слоя одновременно:
  - screenshot bundle;
  - live runtime truth из `/status?format=json`;
  - текущую IA/markup реализацию `/` и `/status`.

## Deliverables For The Next Wave

1. Screenshot bundle для `/` и `/status`.
2. Current-state IA matrix: landing summary vs `/status` deep-ops sections.
3. Mismatch list между screenshots, `/status?format=json`, и текущим HTML.
4. Target IA sketch для landing tabs / summary blocks / relegated deep-ops detail.
5. Небольшой phased implementation plan без giant redesign.

## Verify Hook

`cd /home/lap/projects/codex-pool-orchestrator && rg -n 'REPO-CPO-UX-P3-T59|DIR-20260328-040|STATUS_UI_AUDIT_TZ_20260328|dashboard-first|/status' ACTION_PLAN.md PROJECT_MANIFEST.md docs/STATUS_UI_AUDIT_TZ_20260328.ru.md`

## Residual Risk

- Если следующий агент начнет с DOM/details вместо скриншотов, IA снова уедет в implementation-first выводы.
- Если summary blocks на landing не будут явно отделены от `/status`, операторский шум останется тем же, только в новом оформлении.

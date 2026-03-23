# Версионирование

Репозиторий использует практичную SemVer-подобную схему с `0.x`-релизами, пока форк
еще активно меняется на operator/runtime границе.

Текущая версия хранится в [`../VERSION`](../VERSION).

## Правила

### Major

Major-версия повышается только после перехода к `1.0.0` и только при намеренных
ломающих изменениях в одном из operator-facing контрактов:

- формат конфигурации
- layout хранения данных
- видимый автоматизации контракт `/status`
- семантика маршрутизации пула, требующая адаптации со стороны оператора

### Minor

Minor-версия повышается для намеренных, видимых пользователю или оператору изменений:

- новая возможность пула
- новый тип аккаунта или режим маршрутизации
- новый admin/dashboard workflow
- существенное изменение routing policy
- новое fallback-поведение

### Patch

Patch-версия повышается для изменений без намеренного внешнего контрактного эффекта:

- bug fixes
- внутреннее hardening
- тесты
- рефакторинг
- улучшение логов и наблюдаемости

## Pre-release суффиксы

Пока работа идет на ветке, используются pre-release суффиксы:

- `-dev` для активной branch work
- `-rc.1`, `-rc.2`, ... для release candidates

Примеры:

- `0.4.0`
- `0.5.0-dev`
- `0.5.0-rc.1`

В релизной автоматизации можно добавлять git metadata, например:

- `0.5.0-dev+f1fc044`

## Рекомендуемый процесс

1. `main` держит последнюю стабильную версию.
2. Активные feature branches переходят на следующий minor с суффиксом `-dev`.
3. Release candidates режутся только после smoke tests и operator checks.
4. Стабильные релизы тегируются как `vX.Y.Z`.
5. Все user-visible изменения заносятся в [`CHANGELOG.md`](../CHANGELOG.md).

## Текущая линейка версий

- `0.1.0`: standalone operator-ready fork
- `0.2.0`: websocket auth и dead-seat handling
- `0.3.0`: tighter operator dashboard logic
- `0.4.0`: OpenAI API fallback pool
- `0.5.0`: волна request-planning refactor, GitLab Claude pool lane, dashboard-first operator landing и hardening health-truth для GitLab
- `0.5.1-dev`: очистка proxy response-handling seam-ов в buffered, streamed и websocket ветках с расширенным regression coverage

---
name: release
description: >-
  Мерж → бамп → релиз brigade и ожидание готовности. Мержит текущую ветку в main (если не на
  main), гоняет `make release` (bump из аргумента, дефолт patch — инкрементит semver-тег и
  пушит, что триггерит docker-CI), затем через `gh` ждёт появления GitHub Release с новой
  версией (значит оба образа собрались и опубликовались) и шлёт desktop-уведомление
  (terminal-notifier). Только для репозитория brigade. Триггеры: /release, "релизни",
  "выкати релиз", "мерж бамп релиз", "собери релиз".
argument-hint: "[patch|minor|major]"
---

# Релиз brigade

Оркестрация «мерж/бамп/релиз» + ожидание готовности + уведомление. Выполняй строго по шагам;
на невыполненном предусловии — останавливайся с понятным сообщением, ничего не выдумывая.

Контекст версий: версия проекта — только git-тег `vMAJOR.MINOR.PATCH` (в файлах не хранится).
Бамп — только через `make release` (руками теги не создавать). CI по тегу `v*`
(`.github/workflows/docker.yml`) собирает два образа (`brigade`, `brigade-agent` в ghcr) и
ПОСЛЕ успеха создаёт GitHub Release — его появление и есть «релиз сварился».

## 1. Bump-уровень
- Из аргумента: `patch` | `minor` | `major`. Пусто → `patch`.
- Баг-фиксы → patch; новые фичи → minor; ломающие изменения → major. Если сомневаешься и
  аргумент не задан — спроси у пользователя уровень, не угадывай молча.

## 2. Предусловия
- Рабочее дерево ДОЛЖНО быть чистым (`make release` этого требует). Проверь:
  `git status --porcelain` (untracked `*.zip`/gitignored не в счёт). Если есть незакоммиченные
  изменения — НЕ коммить сам (не выдумывай сообщение): сообщи пользователю и останови скилл.

## 3. Мерж в main
- `branch=$(git branch --show-current)`.
- Если `branch` ≠ `main`: `git checkout main` и `git merge --ff-only "$branch"`. Если ff-мерж
  невозможен — останови с ошибкой (нужен явный мерж/ребейз, это решение пользователя).
- `git push origin main` (коммиты в main пушатся до тега).

## 4. Бамп + тег
- `make release BUMP=<bump>` — инкрементит последний semver-тег, делает annotated-тег, пушит
  (это триггерит docker-CI). В выводе будет `vX -> vY`.
- Зафиксируй новую версию: `VERSION=$(git describe --tags --abbrev=0)`.

## 5. Ждать готовности релиза (через gh)
Жди появления GitHub Release с этой версией. Цикл с таймаутом (~20 мин, шаг 20с). На каждой
итерации ещё проверяй, не упал ли CI-запуск по тегу — тогда релиз не появится, прекращай.

```bash
ready=""
for i in $(seq 1 60); do
  if gh release view "$VERSION" >/dev/null 2>&1; then ready=1; break; fi
  # CI по тегу упал → релиза не будет: прекращаем ожидание.
  if gh run list --workflow=docker.yml -L 10 \
       --json headBranch,status,conclusion \
       --jq ".[] | select(.headBranch==\"$VERSION\" and .status==\"completed\" and .conclusion!=\"success\")" \
     | grep -q .; then echo "CI-FAILED"; break; fi
  sleep 20
done
```
- Если появился релиз (`ready`) — переходи к шагу 6.
- Если `CI-FAILED` — покажи упавший запуск (`gh run list --workflow=docker.yml -L 5`,
  `gh run view <id> --log-failed`), сообщи пользователю и НЕ уведомляй об успехе.
- Если таймаут — сообщи, что релиз ещё собирается, дай ссылку на прогресс.

## 6. Уведомление (если есть нотификатор)
Когда релиз готов — desktop-уведомление, если инструмент доступен (иначе просто напиши в чат):
```bash
if command -v terminal-notifier >/dev/null 2>&1; then
  terminal-notifier -title "brigade" -subtitle "релиз сварился" -message "$VERSION опубликован"
elif command -v osascript >/dev/null 2>&1; then
  osascript -e "display notification \"$VERSION опубликован\" with title \"brigade\" subtitle \"релиз сварился\""
fi
```

## 7. Итог
Кратко сообщи пользователю: версия `$VERSION` собрана и опубликована; ссылка на релиз
(`gh release view "$VERSION" --json url --jq .url`). Если были долги деплоя (напр. удалить
старые контейнеры) — не изобретай, упомяни только если это зафиксировано в задаче/памяти.

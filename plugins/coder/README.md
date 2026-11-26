# Brigade Coder MCPB

Coder — MCP App для работы с проектом в правой панели Brigade. В интерфейсе
четыре вкладки с единым стилем и активным состоянием через `aria-current`:

- **Результат** — именованные web preview и terminal commands.
- **Файлы** — lazy tree на `@headless-tree/core` и просмотр текста в readonly Monaco.
- **Git** — история, refs, working tree, сравнения и file diff без изменения
  репозитория.
- **CI/CD** — статусы и deployments из аутентифицированного host/backend.

Штатный ACP-чат, история, composer, permissions, preview и PTY принадлежат host. Coder
не является отдельным клиентом и не меняет активную сессию. На mobile чат и панель Coder
располагаются один под другим.

Чтобы открыть Coder, создайте сессию типа **ACP** и выберите **Панель → Coder**. Проверка
результата: чат остаётся доступен, а справа или ниже него появляются вкладки Coder.
Смена Coder на CAD или **Без панели** доступна только в idle-состоянии. Brigade сохраняет
native session ID, разговор, workspace и контейнер, но переинициализирует адаптер с MCP
и developer instructions выбранной предметной области. Переключение вкладок Coder адаптер
не перезапускает.

Пустой workspace поддерживается сразу. Агент создаёт или клонирует проект и вызывает
`coder.select_project`; ручного выбора проекта в UI нет. Выбранный проект общий для
Files, Git и CI/CD.

Вкладка Files запрашивает каталоги по мере раскрытия дерева, по 500 записей
за запрос. Если записей больше, дерево показывает кнопку «Показать ещё». Files читает
до 1 MiB выбранного файла. UTF-8 текст открывается в Monaco; бинарные и обрезанные файлы
показывают явный статус. Редактор не сохраняет файлы. README, документы и исходники
уже доступны здесь: регистрировать `cat`, `head`, `tail` или другой terminal result для
их просмотра не нужно.

Вкладка Git встраивает `web-git-graph` 1.0.7, а file diff отрисовывает через `diff2html`.
Все Git-функции доступны только для чтения: checkout, stage, commit и push не
поддерживаются.

Сервер — `backend/cmd/brigade-coder`, Go SDK уже закреплён в `backend/go.mod`.
Зависимостей от `backend/internal`, отдельного Go module и нового backend dependency нет.

## Сборка

Требуются Bash, Go 1.26+, Node.js 20.19+ или 22.12+ (Docker: Node22), npm, Git и zip.
Python/uv не используются. Для `npm ci` и Go downloads нужен доступ к registry/module
cache. UI toolchain тот же, что у CAD; 3D/React dependencies не добавлены.

```bash
VERSION=v1.2.3 bash plugins/coder/build.sh
```

Без `VERSION` берётся последний достижимый Git tag. Без tag/явной версии сборка
завершается ошибкой, не подставляет фиктивную версию. В Docker source stage без `.git`
обязательно передавать `VERSION` из версии Brigade. Версия одна для manifest, Go
implementation и UI. Результат: **`plugins/coder/brigade-coder.mcpb`**.

Для source stage достаточно всего `plugins/`, `backend/go.mod`, `backend/go.sum`
и `backend/cmd/brigade-coder/`. Скрипт выполняет `npm ci`, `tsc --noEmit`, Vite singlefile
и `CGO_ENABLED=0 go build -mod=readonly`, затем zip. Поддерживает `GOOS`/`GOARCH`
для darwin/linux, один bundle на платформу/архитектуру. Runtime требует только
совместимый binary и Git; Node/npm на runtime не нужны.

UI фиксирует `@web-git-graph/protocol` и `@web-git-graph/web` на версии 1.0.7;
per-file diff использует `diff2html` 3.4.56 из lockfile. Сборка устанавливает
зависимости через `npm ci`.

`ui/patches/@web-git-graph+web+1.0.7.patch` не заменяет граф. Он добавляет режим
`details-position="bottom"`, slot `file-preview` над нижней панелью details и событие её закрытия.
Тот же patch уточняет CSS-селектор для ширины до 760 px, чтобы заголовки скрытых колонок
не занимали вторую строку. `patch-package` применяет patch при `npm ci` и завершает установку
ошибкой, если он перестал применяться. При обновлении библиотеки проверьте оба режима
компоновки и narrow-panel regression; удалите patch, если изменения вошли в upstream.

UI собирается без CDN. CSP разрешает `worker-src blob:` для Monaco и `font-src data:`
для встроенных шрифтов; `connect-src` и `frame-src` остаются `none`.

## Проверка

```bash
env -u GOROOT go -C backend test ./cmd/brigade-coder
env -u GOROOT go -C backend test -race ./cmd/brigade-coder
env -u GOROOT go -C backend vet ./cmd/brigade-coder
VERSION=v1.2.3 bash plugins/coder/build.sh
brigade plugin validate plugins/coder/brigade-coder.mcpb
```

Тесты покрывают shared state двух процессов, upsert, corruption/cancellation,
MCP resource/tools/error responses, Git argv/pathspec, merge-base + uncommitted,
проект в подкаталоге, bounded untracked text/binary/symlink и запрет Git filters.

Ручная проверка host: вкладка Git показывает историю, refs и working tree; выбор commit
и compare открывает список файлов; per-file diff отрисовывается через `diff2html`;
ошибка, отмена и смена `projectPath` не оставляют устаревший diff. Проверьте Files: каталоги
догружаются при раскрытии, а для каталога из 501 записи кнопка «Показать ещё»
догружает оставшуюся запись. Текст открывается без возможности сохранить, а для binary и файла
больше 1 MiB виден статус. Проверьте, что проект выбирает только агент через
`coder.select_project`, а Git UI не предлагает
checkout, stage, commit или push. Регистрация того же имени обновляет открытый preview
без смены фокуса; terminal registration/update не запускают и не перезапускают команду;
после exit shell и WebSocket закрываются, а вкладка результата, вывод и exit code остаются
до ручного закрытия; reconnect не повторяет команду;
кнопка «В запрос» дописывает file diff в composer без отправки; CI без
интеграции/сборок не «зелёный».

Согласованные имена, shapes, capability handshake и ограничения безопасности:
[HOST_API.md](HOST_API.md).

## Browser smoke реального bundle

После сборки `node plugins/coder/smoke-browser.mjs` распаковывает bundle во временный
каталог и выводит loopback URL. Используются настоящий MCP binary, официальный
stdio Client и browser AppBridge/PostMessageTransport, без ACP и mock MCP protocol.
`?connect=before` подключает host до назначения iframe `srcdoc`;
`?connect=load` воспроизводит прежний порядок host с риском потери раннего `ui/initialize`.
`window.audit` содержит порядок load/connect/MCP messages. CI capability объявлена,
но fixture разрешает только read-only Coder tools: empty workspace не должен
вызывать provider CI. Fixture не использует установленный bundle cache или рабочий
сервер Brigade. Проверять нужно именно свежесобранный архив: повторная версия не
обновляет immutable cache установленного плагина автоматически.

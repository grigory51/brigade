# Coder ↔ Brigade host

Coder — правая панель split layout с вкладками «Результат», «Файлы», «Git» и «CI/CD».
Штатный ACP-чат, история, composer, permissions, preview и PTY принадлежат host. Coder
не является альтернативным клиентом, не меняет активную сессию и не предоставляет
переходы из неё. На mobile host размещает чат и Coder один под другим. MCP transport —
официальный Go SDK из `backend/go.mod`. Ни JWT, ни preview tokens, ни CI credentials не
передаются в HTML, state или tool results.

Все четыре tab buttons используют общий `brigade-plugin-button`; активная вкладка отмечена
`aria-current="page"`.

Смена визуальной вкладки Coder не меняет выбранный плагин и не переинициализирует
ACP-адаптер. Смена Coder на другую предметную панель выполняется host только в idle:
MCP и developer instructions меняются, адаптер возобновляет тот же native session ID,
а история, workspace и контейнер сохраняются.

## MCP tools

- `coder.open({})` → `State`; metadata `ui.resourceUri = ui://coder/app.html`.
- `coder.state({})` → `State`, всегда свежее чтение workspace.
- `coder.register_result(Result)` → новый `State`; upsert по точному `name`.
- `coder.select_project({projectPath})` → новый `State`; каталог должен существовать
  внутри workspace, но может быть пустым и не содержать Git. Агент вызывает после clone/create.
- `coder.changes({projectPath?: string, scope?: "all" | "unstaged" | "staged" | "branch", base?: string, path?: string})` → `Changes`.
- `coder.git_history(GitHistoryInput)` → `GitGraphPage`.
- `coder.git_details(GitDetailsInput)` → `GitGraphCommitDetails`.
- `coder.git_compare(GitCompareInput)` → `GitGraphComparison`.
- `coder.git_diff(GitDiffInput)` → `GitGraphFileDiff`.

```ts
type Result = { name: string } & (
  | { kind: "web"; url: string }
  | { kind: "terminal"; command: { argv: string[]; cwd: string } }
);
type State = { schemaVersion: 1; revision: number; projectPath: string; results: Result[] };
```

State хранится в `<workspace>/.brigade/coder/state.json`: flock на отдельном
`state.lock` охватывает read-modify-write, временный файл в той же директории,
fsync файла → atomic rename → fsync директории. Чтение не кешируется в процессе.
Повреждённый/неподдерживаемый state — ошибка, не сброс. До 100 результатов и 1 MiB.
Регистрация не запускает сервер, shell или terminal command.

`State.projectPath` по умолчанию `.`; в старом state без этого поля нормализуется в `.`.
Регистрация проекта не заменяет результаты. UI подхватывает выбор агента через обычный
polling state; ручного выбора проекта нет. Во всех Git tools пропущенный `projectPath`
означает каталог из state; разрешён проект внутри workspace, включая клонированный
подкаталог. `path` — literal path относительно проекта.

## Files и host workspace tools

Вкладка Files строит lazy tree через `@headless-tree/core`: первая страница каталога
запрашивается при раскрытии узла. Если в ответе есть `nextPageToken`, дерево добавляет
строку-кнопку «Показать ещё». Следующая страница запрашивается только по клику и
добавляется к уже загруженным записям. Файл открывается в readonly Monaco. В UI нет сохранения и host не
предоставляет workspace mutation tools плагину. README, документы и исходники не требуют
`coder.register_result` или terminal results с `cat`, `head` или `tail`.

Host перехватывает два server tools; в MCP server Coder они не пересылаются:

```ts
type WorkspaceListArguments = { path: string; pageToken?: string };
type WorkspaceEntry = {
  name: string;
  path: string;
  mode: number;
  size: string;
  modifiedAt: string;
  directory: boolean;
  symlink: boolean;
};
type WorkspaceListResult = { entries: WorkspaceEntry[]; nextPageToken: string };

{ name: "brigade.workspace.list", arguments: { path: "repo/src" } }
// structuredContent: WorkspaceListResult

{ name: "brigade.workspace.read", arguments: { path: "repo/src/main.ts" } }
// structuredContent = { text: string, binary: boolean, truncated: boolean }
```

Для `brigade.workspace.list` `arguments` — strict object с `path` и необязательным `pageToken`
длиной до 7 символов.
Для `brigade.workspace.read` допустим только `path`. Host подставляет `sessionId` активной сессии;
iframe не может передать другой `sessionId` или задать `pageSize`. `path` должен быть непустым,
длиной не более 4 096,
относительным к workspace и не содержать `\`, NUL или сегмент `..`. Абсолютные пути
отклоняются.

`brigade.workspace.list` вызывает `WorkspaceService.List` с `pageSize: 500` и передаёт
`pageToken` без изменений. Клиент должен возвращать только `nextPageToken` из
предыдущего ответа; пустое значение означает конец каталога. Backend отклоняет
недесятичный token и смещение больше 1 000 000, но для клиента token остаётся непрозрачным.
Корневой узел Files не показывает записи `.git` и `.brigade`.
`brigade.workspace.read` сначала вызывает `WorkspaceService.Stat`, отклоняет каталоги, затем читает
байты `[0, 1 MiB)` через `WorkspaceService.Read`. `truncated: true` возвращается, если размер файла
больше 1 MiB или ответ `Read` содержит ровно 1 MiB. Файл считается бинарным при NUL-байте
или невалидном UTF-8; для него
возвращается `binary: true` и пустой `text`. Если лимит обрезал UTF-8 внутри последнего символа,
файл не помечается бинарным, а неполный хвост не попадает в `text`.

## Git graph

Вкладка Git использует `@web-git-graph/web` и `@web-git-graph/protocol` 1.0.7.
UI передаёт компоненту provider с историей, деталями commit, compare, working tree
и file diff. Stash и любые изменения репозитория не поддерживаются. Checkout, stage,
commit и push отсутствуют и в UI, и в `coder.git_*` tools.

Provider объявляет protocol version 1, `history`, `details`, `compare`, `diff` и
`workingTree`; `stashes` отключён. Максимальный размер страницы — 100 коммитов,
file diff — 262 144 байта.

```ts
type GitGraphRevision =
  | { kind: "commit"; oid: string }
  | { kind: "working-tree"; oid?: never };

type GitGraphAuthor = { name: string; email?: string };
type GitGraphWorktree = { staged: number; unstaged: number; untracked: number };
type GitGraphCommit = {
  oid: string;
  parents: string[];
  message: string;
  kind: "commit" | "working-tree";
  author?: GitGraphAuthor;
  authoredAt?: string;
  committedAt?: string;
  changedFiles?: number;
  worktree?: GitGraphWorktree;
};
type GitGraphRef = {
  name: string;
  target: string;
  kind: "head" | "remote" | "tag" | "current";
};
type GitGraphChange = {
  path: string;
  kind: "add" | "delete" | "modify" | "unknown";
  additions: number;
  deletions: number;
  binary?: boolean;
  staged?: boolean;
  unavailableReason?: string;
};

type GitGraphPage = {
  commits: GitGraphCommit[];
  refs: GitGraphRef[];
  head?: string;
  cursor?: string;
  hasMore: boolean;
  repositoryId?: string;
  repositoryName?: string;
};
type GitGraphCommitDetails = {
  commit: GitGraphCommit;
  refs: GitGraphRef[];
  changes: GitGraphChange[];
  body?: string;
};
type GitGraphComparison = {
  base: GitGraphRevision;
  head: GitGraphRevision;
  changes: GitGraphChange[];
  additions: number;
  deletions: number;
  truncated?: boolean;
};
type GitGraphFileDiff = {
  base: GitGraphRevision;
  head: GitGraphRevision;
  path: string;
  patch?: string;
  binary?: boolean;
  truncated?: boolean;
  unavailableReason?: string;
};
```

### `coder.git_history`

```ts
type GitHistoryInput = {
  projectPath?: string;
  ref?: string;
  refs?: string[];
  cursor?: string;
  limit?: number;
  includeWorkingTree?: boolean;
};
```

История отсортирована топологически. `refs` задаёт объединение tips и имеет приоритет
над `ref`; без обоих полей сервер читает все локальные и remote branches и tags.
`limit` — от 1 до 100, по умолчанию 50. При `includeWorkingTree: true` первая страница
начинается с псевдо-commit working tree, только если есть staged, unstaged или untracked
файлы. Каталог без Git возвращает пустую страницу, а не ошибку.

Cursor привязан к snapshot репозитория, refs, текущей ветке и working tree. После
изменения snapshot сервер возвращает `snapshot_expired`; клиент должен загрузить
историю заново без cursor. Stash refs не поддерживаются.

### `coder.git_details`

```ts
type GitDetailsInput = {
  projectPath?: string;
  revision: GitGraphRevision;
};
```

Для commit ответ содержит metadata, body, refs на этот commit и изменения относительно
первого parent. Для root commit базой служит empty tree. Для working tree ответ содержит
staged, unstaged и untracked файлы относительно `HEAD`; поле `staged` отмечает изменения
index. Если `HEAD` ещё не создан, базой служит empty tree.

### `coder.git_compare`

```ts
type GitCompareInput = {
  projectPath?: string;
  base: GitGraphRevision;
  head: GitGraphRevision;
};
```

`base` должен быть commit; `head` может быть commit или working tree. Ответ содержит
список файлов и суммарные additions/deletions. Rename detection и submodule diff
отключены. Working tree нельзя использовать как `base`.

### `coder.git_diff`

```ts
type GitDiffInput = {
  projectPath?: string;
  base?: GitGraphRevision;
  head: GitGraphRevision;
  path: string;
  context?: number;
};
```

Tool возвращает unified diff одного literal project-relative файла. Каталоги, traversal,
`.git` и `.brigade/coder` запрещены. `context` — от 0 до 100 строк, по умолчанию 3.
Если `base` пропущен, сервер использует первый parent для commit, `HEAD` для working tree
или empty tree для root commit и unborn `HEAD`. Размер patch ограничен 256 KiB;
`truncated`, `binary` и `unavailableReason` объясняют отсутствие полного текстового diff.
External diff, textconv, rename detection и submodule diff отключены.

UI всегда запрашивает три строки контекста и перехватывает file-open у
`web-git-graph`. Полученный patch отрисовывает `diff2html` в line-by-line режиме.
Symlink и special untracked files не читаются, а untracked regular files больше
256 KiB возвращают `unavailableReason`.

Локальный patch для `@web-git-graph/web` добавляет только компоновку: Coder устанавливает
`details-position="bottom"`, помещает file preview в slot `file-preview` над details dock и слушает
`gitgraph-details-close`. История, provider и выбор commit остаются в `web-git-graph`; Coder не
переписывает граф.

### Ошибки и лимиты

Каждый Git graph tool ограничен 10 секундами. Git metadata ограничены 1 MiB,
file patch — 256 KiB, размер страницы истории — 100 commit. Ошибка возвращается как
MCP `isError` с JSON в текстовом content:

```json
{"error":{"code":"snapshot_expired","message":"Repository changed; reload history","retryable":true}}
```

Коды: `bad_request`, `internal_error`, `output_limit`, `repository_not_found`,
`revision_not_found`, `snapshot_expired`, `unsupported`. Только `snapshot_expired`
имеет `retryable: true`.

## `coder.changes` и CI probe

По умолчанию `scope: "all"` включает staged, unstaged и untracked файлы.

Если доступный каталог не содержит `.git` (`Lstat` возвращает `os.ErrNotExist`),
`Changes.status` равен `not_repository`, массивы пусты. Ошибки доступа/пути и повреждённый
`.git`, в том числе broken symlink, остаются ошибками. Для рабочего Git `status: ready`.
`branch` требует явный `base` (например `origin/main`); неизвестный ref, отсутствие
HEAD/merge-base — ошибки. Diff от merge-base до текущего worktree включает
committed + staged + unstaged; новые regular text files дописываются как additions.
Никакого fetch и выбора базы по предположению. Binary/symlink/special files не читаются
как текст. Лимиты: 10 секунд, 256 KiB diff, 64 KiB status, 64 KiB на новый файл,
8 KiB stderr; обрезка отмечается флагами, ошибки чтения — `warnings`.

## Capability handshake

Host отправляет через стандартный `sendToolResult`:

```json
{
  "content": [],
  "structuredContent": {
    "brigadeHost": {
      "openResult": true,
      "appendPrompt": true,
      "coding": { "getCI": true },
      "workspace": { "list": true, "read": true }
    }
  }
}
```

Не объявлять capability до реализации соответствующего обработчика. При отсутствии
capability Coder показывает недоступность интеграции, а не пример успешного результата.
Это весь snapshot `brigadeHost`: host не посылает `generating`, `lastMessage`, `prompts`
или `submitMode`. Для режима отображения host context использует
`displayMode: "inline"` и `availableDisplayModes: ["inline"]`.

Стандартный MCP Apps `sendMessage` остаётся доменным действием и отправляет сообщение в
штатный ACP-thread. Coder для действия «В запрос» использует `brigade.appendPrompt`, чтобы
только дополнить штатный composer. Совместимое host action `brigade.cancel` останавливает
turn, но не объявляется как tool MCP-сервера Coder.

## Привязка UI MCP к панели

Каждый UI-запрос Coder к `PluginService.MCP` передаёт:

```json
{
  "sessionId": "<session-id>",
  "expectedPlugin": { "id": "coder", "version": "<pinned-version>" },
  "method": "tools/call",
  "paramsJson": "<encoded-json>"
}
```

Host сравнивает `expectedPlugin` с текущей панелью после ожидания session lock. Если
панель сменилась, запрос завершается `FailedPrecondition` и не попадает в новый MCP
runtime. Поле необязательное: старые клиенты без него сохраняют прежнее поведение.

## `brigade.openResult`

```ts
// app.callServerTool, перехватывается host; не пересылать в MCP server Coder.
{ name: "brigade.openResult", arguments: {
  stateTool: "coder.state", name: "Frontend", revision: 4, activate: true
} }
// Ответ MCP:
{ content: [], structuredContent: { opened: true } }
```

1. Host использует активную сессию и строит allowlist по `tools/list`: допускаются
   только tools с `annotations.readOnlyHint === true`. До вызова проверяет membership
   `stateTool`; непроверенный/mutating tool не выполняет. Для Coder читает `coder.state`.
2. Проверяет точное имя и ревизию. Устаревший snapshot → `isError`, не запускает
   команду из iframe и не подменяет её незаметно более новой.
3. Web URL разрешает только после сопоставления с `SessionService.ListPreviews`
   текущей сессии: protocol/origin/port/path-prefix и routing query `id`. Перед
   регистрацией агент вызывает Brigade RegisterPreview и использует возвращённый URL.
   Coder разрешает только HTTP(S), без userinfo/fragment и без query, кроме `id`.
   Host не загружает произвольные URL сервером. Preview изолирован от host origin;
   same-origin preview не получает `allow-same-origin` и доступ к токенам host.
4. Для terminal показывает argv и cwd, затем отдельную кнопку запуска. Host повторно
   проверяет cwd внутри workspace. Запуск в runtime текущей сессии, без shell-конкатенации
   аргументов; если применяется существующий shell PTY, каждый аргумент должен быть
   корректно quoted. Команда не исполняется при регистрации, открытии или polling.
   PTY транспорт и авторизация остаются вне iframe. Отказ/отмена не являются успехом.
После подтверждения host отправляет terminal command в shell один раз. Reconnect не повторяет команду;
перезагрузка снова показывает подтверждение, но не запускает её. Wrapper передаёт фактический
exit code в host; после exit host закрывает WebSocket и shell, но сохраняет xterm, вывод,
exit code и вкладку именованного результата до её ручного закрытия. Закрытие
вкладки, смена приложения или перезагрузка также закрывают shell WebSocket.

Идентичность открытой вкладки — **`stateTool + name`**, не revision. Coder опрашивает
state раз в 3 секунды при видимом документе. Для уже открытых результатов при новой
ревизии вызывает `brigade.openResult` с `activate:false`; ошибки обновления повторяются
на следующем polling. Такой вызов обновляет только существующую вкладку, не создаёт
вкладку и не переключает фокус. Повторная регистрация того же web URL также означает
новую ревизию и обновление preview. Terminal получает новую pending command, но
существующий PTY не перезапускается; restart только по явному действию пользователя.
Host должен сохранять MCP iframe mounted при просмотре preview/PTY.

## `brigade.coding.getCI`

```ts
{ name: "brigade.coding.getCI", arguments: { projectPath: "repo" } }
```

Host подставляет активный `sessionId` и вызывает аутентифицированный
`CodingService.GetCI` с тем же `projectPath`. Ответ: `structuredContent` равен
`GetCIResponse.toJson()` (camelCase, включая строковый int64 `fetchedAt`). Контракт
определён в `proto/brigade/v1/coding.proto`: provider/repository/commitSha,
runs/jobs/deployments, partial/warnings, fetchedAt/refreshAfterSeconds.

До вызова CI Coder делает `coder.changes` probe для текущего `projectPath`. Только
`status: not_repository` отключает запрос CI и показывает нормальный empty state.
Тексты ошибок не парсятся, другие ошибки не маскируются под отсутствие репозитория.
Новых protobuf-полей для этого probe не требуется.

Coder показывает provider vocabulary без выдуманных статусов и выводов об успехе,
пустые runs означают отсутствие полученных сборок. При ошибке provider или чтения
сохраняет старые данные с timestamp; ошибки prerequisites показывает как empty state.
Фоновое обновление не заменяет контент loader-ом.
Выбор проекта един для Git и CI; credentials и provider API находятся в backend.
Действий retry/deploy/cancel в текущей версии нет.

Backend проверяет ownership, workspace, Git metadata, endpoint и наличие обязательного токена
до provider limiter. Эти локальные проверки не занимают provider budget. Limiter начинается
непосредственно перед GitHub или GitLab request.

`CodingService` записывает машиночитаемую причину в Connect metadata `coding-reason`:

| `reason` | Значение | UI Coder |
|---|---|---|
| `no-remote` | Нет `origin` | Empty state с действием |
| `no-head` | Не удалось определить commit `HEAD` | Empty state до первого commit |
| `not-configured` | Нет endpoint или обязательного токена | Empty state с подсказкой о настройке |
| `unsupported-workspace` | Git metadata не поддерживаются | Явная ошибка с подсказкой |
| `workspace-unavailable` | Workspace или Git metadata недоступны | Явная ошибка с подсказкой |
| `rate-limited` | Provider limiter отклонил запрос | Предыдущие данные сохраняются, показан cooldown |
| `credentials` | Provider отклонил токен или его права | Явная ошибка с подсказкой |
| `upstream` | Ошибка provider | Предыдущие данные сохраняются, показан cooldown |
| `canceled` | Context отменён | Для локальной отмены — статус отмены |
| `deadline-exceeded` | Истёк deadline запроса; внутренний timeout `GetCI` — 20 секунд | Явная ошибка и cooldown |
| `internal` | Неклассифицированная ошибка backend | Сообщение backend |

На границе запроса также возможны `session-not-found` и `invalid-request`. При успехе backend
возвращает `refreshAfterSeconds: 15`. Для `rate-limited` `CodingService` также устанавливает
metadata `retry-after: 15`; host передаёт его как `retryAfterSeconds: 15`. При любой ошибке CI UI
вводит cooldown не менее 30 секунд и на это время отключает ручное обновление. Для ошибок
без metadata `retry-after` host возвращает `retryAfterSeconds: 0`.

## `brigade.appendPrompt`

```ts
{ name: "brigade.appendPrompt", arguments: {
  text: "Проект: repo\nФайл: src/main.ts\n\n<unified patch>"
} }
// Ответ:
{ content: [], structuredContent: { appended: true } }
```

Добавить открытый file diff в существующий composer, сохранив уже набранный текст.
**Не отправлять** turn автоматически. Coder передаёт до 8 000 символов patch вместе
с путём проекта и файла. Host валидирует тип и размер текста.

Ошибки host tools возвращаются через MCP `isError: true`. Для ConnectRPC ошибок host также
передаёт структурированные поля:

```ts
{
  isError: true,
  content: [{ type: "text", text: "<message>" }],
  structuredContent: {
    error: {
      code: number,
      message: string,
      reason: string,
      retryAfterSeconds: number
    }
  }
}
```

`code` — числовой Connect code, `reason` — metadata `coding-reason` или пустая строка,
`retryAfterSeconds` — metadata `retry-after`, приведённая к числу, или `0`. UI сначала читает
`structuredContent.error`, затем переходит к тексту из `content` для ошибок без этой структуры.
UI не вызывает backend через fetch, не читает cookie/storage родителя и не хранит
собственный чат. Shared `@brigade/plugin-ui/styles.css` используется напрямую.

## CSP

UI не загружает код, стили или шрифты с CDN. CSP сохраняет `default-src 'none'`,
`connect-src 'none'` и `frame-src 'none'`; для Monaco разрешены `worker-src blob:` и `font-src data:`.

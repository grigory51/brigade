# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Что это

brigade — сервис для запуска кодинг-агентов (целевой — Claude Code) в сессиях. Каждая
сессия — это либо **CLI** (pty + xterm в браузере), либо **ACP** (структурированные
события AG-UI через чат-интерфейс). Агент спавнится **локально** (процесс на хосте в pty)
или в **Docker**-контейнере на сессию. Бэкенд — единый Go-бинарь со встроенным фронтендом;
есть мобильный KMP-клиент.

## Команды

Всё гоняется из корневых `Makefile` (полная сборка единого бинаря) и доменных
`backend/Makefile`, `mobile/Makefile`.

```bash
# Полная пересборка единого бинаря: кодген proto → сборка web → go:embed → go build.
make build                  # → bin/brigade (фронт встроен)
make run                    # запустить bin/brigade -config config.yaml (НЕ пересобирает)
make build-all              # build + build-mobile

make proto                  # кодген Go+TS из proto/ через buf
make build-web              # npm ci && vite build → web/dist
make test                   # делегирует в backend/Makefile (go test ./...)
make vet                    # go vet ./...
make tidy                   # go mod tidy в backend/

# Backend напрямую (из backend/):
go test ./...                          # все тесты
go test ./internal/auth/ -run TestJWT  # один тест/пакет
make -C backend run                    # build (web+go) + запуск

# Web (из web/):
npm run dev        # Vite dev-сервер с proxy на backend (localhost:8080)
npm run build      # tsc -b && vite build → web/dist
npm run buf:generate

# Mobile (из mobile/):
./gradlew :shared:assemble                       # собрать shared KMP-модуль
./gradlew :shared:linkDebugFrameworkIosArm64     # iOS-фреймворк
./gradlew :composeApp:installDebug               # Android
```

Замечание по окружению: если `go build` падает с `compile: version does not match go tool
version` — в шелл-профиле экспортирован `GOROOT`, указывающий на другую версию Go. Убрать
его export (GOROOT определяется самим `go` автоматически).

## Релиз и версии

Версия проекта — **только git-тег** (`vMAJOR.MINOR.PATCH`), в файлах не хранится
(`web/package.json` = `0.0.0`, ldflags нет). Бампить версию — **только** через целевой
Makefile-таргет, руками теги не создавать:

```bash
make release              # patch: v0.1.0 → v0.1.1
make release BUMP=minor   # v0.1.1 → v0.2.0
make release BUMP=major   # v0.2.0 → v1.0.0
```

`release` требует чистого рабочего дерева, инкрементит последний semver-тег, делает
**annotated**-тег и пушит его. На push тега срабатывает docker-CI (собирает и публикует
образ этой версии). Коммиты в `main` пушатся отдельно (обычным `git push`) до `make release`.

## Источник истины контракта

`proto/brigade/v1/*.proto` — **единственный** источник истины для API. Сгенерированный код
(`backend/gen/go`, `web/src/api/gen`) — производное, руками не правится. Меняешь контракт →
правь `.proto` → `make proto`. Кодген — локальными buf-плагинами:

- Go: `protoc-gen-go`, `protoc-gen-connect-go` из `$GOPATH/bin`.
- TS: `protoc-gen-es`, `protoc-gen-connect-es` из `web/node_modules/.bin` (нужен
  `npm install` в web/ перед `make proto`).

Remote-плагины buf.build не используются (BSR-эндпоинт отдаёт 403).

## Архитектура: единый бинарь

`backend/cmd/brigade/main.go` собирает всё: конфиг (koanf, YAML + env с префиксом
`BRIGADE_` и разделителем `__`) → SQLite store → домены → один `http.ServeMux` на `addr`:

- **ConnectRPC** — единственный санкционированный API-слой brigade для собственных
  request/response ручек: `AuthService`/`SessionService`/`AgentService`/`AcpService`
  (пользовательские, JWT через `auth.Interceptor` из Bearer или cookie) и
  `AgentBridgeService` (вызовы ИЗ сессии — скилл в контейнере; per-session HMAC-токен,
  без JWT-интерсептора, проверка в хендлере). Управляющие ручки ACP-чата (история,
  статус, workflow, отмена, опции, ответ на permission) — методы `AcpService`.
- **WS-терминал** — `/ws/terminal/{sessionId}` (CLI-режим). Аутентификация **одноразовым
  тикетом** в query (`issueStreamTicket` → ticket). Прямой проброс pty ↔ WebSocket.
- **AG-UI поверх SSE** — `POST /api/ag-ui/run` (ACP-режим, потоковый turn). Аутентификация
  **Bearer/cookie на запрос**; `threadId` = идентификатор сессии.
- **Встроенный SPA** — `mux.Handle("/")`, фронт через `go:embed` из
  `backend/internal/web/dist`.

> **Правило API.** По умолчанию — ConnectRPC (proto — источник истины). Сырой HTTP/WS
> допустим ТОЛЬКО там, где Connect физически невозможен: (1) сторонний потоковый
> wire-протокол — AG-UI SSE `/api/ag-ui/run` (`@ag-ui/client`); (2) стриминговый
> транспорт — WS-терминалы `/ws/*`. Новую ручку добавляешь как RPC в `.proto`, не как
> `mux.Handle`. Вызов из скилла курлом — не повод для REST: Connect-протокол это обычный
> `POST /pkg.Service/Method` с JSON-телом (см. `AgentBridgeService`).

> **AG-UI ≠ A2UI** (не миграция, а слои): **AG-UI** (`internal/agui`, `@ag-ui/client`) —
> протокол всего чата (потоковые события `RUN_STARTED/TEXT_MESSAGE_*/TOOL_CALL_*/CUSTOM/
> RUN_FINISHED`), хребет ACP-режима. **A2UI** (`internal/a2ui`, `@a2ui/react`) — одна
> фича поверх: интерактивные генеративные карточки-поверхности, доставляемые ВНУТРИ
> AG-UI как CUSTOM-событие `{name:"a2ui"}` (см. `translate.go` → `agui.CustomA2UIName`).

### Домены (`backend/internal/`)

| Пакет | Роль |
|-------|------|
| `config` | koanf-конфиг, режимы `local`/`docker`, валидация |
| `store` | SQLite (mattn/go-sqlite), модели, queries, миграции в `store/migrations/` (применяются автоматически на старте) |
| `auth` | JWT (golang-jwt v5), refresh-токены, сид-юзер, одноразовые WS-тикеты (`TicketStore`) |
| `spawn` | интерфейс `Spawner` + `Handle`; `local.go` (creack/pty), `docker.go` (контейнер на сессию по label `brigade.session.id`) |
| `acp` | ACP-клиент: спавнит subprocess adapter (`claude-agent-acp` поверх Claude Agent SDK), coder/acp-go-sdk, транслирует события ACP → AG-UI |
| `agui` | модель событий AG-UI |
| `session` | `Registry` — связующее звено: пишет сессию в store, спавнит агента, держит живой объект (CLI: `spawn.Handle`, ACP: `*acp.Client`) в памяти |
| `agent` | реестр типов агентов |
| `transport/{connect,agui,termws}` | транспортные хендлеры; `connect/mapping.go` маппит store-строки ↔ proto-enum |
| `web` | `go:embed` фронтенда |

### Persist / resume (ключевое)

`session.Registry` персистит каждую сессию в store со статусом и resume-полями
(`agent_session_id`, `container_label`). При старте `RestoreAll` поднимает `running`-сессии
заново; упавшие при восстановлении помечаются `failed` и **не роняют старт**.

- **local resume**: `claude --resume <agent_session_id>` (перезапуск процесса).
- **docker resume**: attach к существующему контейнеру по label `brigade.session.id`.

store хранит `mode`/`kind` строками (`"local"`/`"docker"`/`"cli"`/`"acp"`); маппинг в
proto-enum делает транспортный слой (`transport/connect`), а не реестр.

## Архитектура: web (`web/`)

React 18 + TS + Vite 6 + Tailwind 4 + shadcn. Встраивается в бинарь через `dist` → go:embed.

- `src/api/client.ts` — Connect-клиенты; `src/api/ws.ts` — URL стрима с тикетом;
  `src/api/gen/` — сгенерированные protobuf-коды (не править).
- `src/features/cli/CliPage.tsx` — xterm.js (`@xterm/xterm`) + WS `/ws/terminal/:id`.
- `src/features/acp/` — AG-UI-чат: `@ag-ui/client` `HttpAgent` + `@assistant-ui/react-ag-ui`,
  POST на `/api/ag-ui/run`. Human-in-the-loop permission-диалоги и usage приходят как
  CUSTOM-события AG-UI. frontend-tools регистрируются и пробрасываются агенту.
- `src/features/auth/AuthContext.tsx` — login → httpOnly-cookie + Bearer-JWT в памяти.
  Connect-вызовы шлют cookie; AG-UI приоритезирует Bearer, fallback на cookie.
- Dev: `vite.config.ts` proxy на `localhost:8080` для `/brigade.v1.*`, `/ws/`, `/api/ag-ui`.

## Архитектура: mobile (`mobile/`)

Kotlin Multiplatform 2.0 + Compose Multiplatform. Модули: `shared` (общий код, публикуется
как iOS-фреймворк), `composeApp` (UI, каркас), `iosApp` (Xcode-обёртка, каркас).

- Сеть — Ktor HttpClient (Connect-**JSON**, без connect-kotlin), движки: OkHttp (Android) /
  Darwin (iOS) через `expect`/`actual` `httpClientEngine()`.
- `commonMain`: `net/{ConnectClient,Services}`, `acp/AcpClient` (SSE, ручной разбор
  `text/event-stream`), `stream/StreamClient` (WS-терминал), `model/Models` (@Serializable
  по proto), `auth/{TokenStore,SessionManager}`, `BrigadeClient` (фасад).
- Состояние: сетевой слой готов; Compose-экраны и Android-`actual` (OkHttp, защищённое
  хранилище) — каркас/заглушки. iOS `TokenStore` пока in-memory (Keychain — позже).
- Полную карту контракта см. `mobile/README.md`.

## Поиск по коду

Сначала **ast-index** — см. `.claude/rules/ast-index.md`. Репозиторий полиглотный
(Go / TS / Kotlin / proto); grep — только когда ast-index пуст или нужны строковые
литералы/regex/комментарии.

## Конфиг и секреты

`backend/config.yaml` — локальный, в .gitignore. Шаблон — `backend/config.example.yaml`.
В проде секреты (`jwt.secret`, `claude_code_oauth_token`) задавать через env
(`BRIGADE_JWT__SECRET`, `BRIGADE_CLAUDE_CODE_OAUTH_TOKEN`), не в yaml.
```

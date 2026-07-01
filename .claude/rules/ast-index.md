# ast-index Rules

## Mandatory Search Rules

1. **ALWAYS use ast-index FIRST** for any code search task
2. **NEVER duplicate results** - if ast-index found usages/implementations,
   that IS the complete answer
3. **DO NOT run grep "for completeness"** after ast-index returns results
4. **Use grep/Search ONLY when:**
   - ast-index returns empty results
   - searching for regex patterns (ast-index uses literal match)
   - searching for string literals inside code (`"some text"`)
   - searching in comments content

## Why ast-index

ast-index is much faster than grep on large repos and returns structured,
accurate results.

## Common Command Reference

| Task | Command |
|------|---------|
| Universal search | `ast-index search "query"` |
| Find type/class | `ast-index class "Name"` |
| Find symbol | `ast-index symbol "Name"` |
| Find usages | `ast-index usages "Name"` |
| Find implementations | `ast-index implementations "Interface"` |
| Call hierarchy | `ast-index call-tree "function" --depth 3` |
| Find callers | `ast-index callers "functionName"` |
| Module deps | `ast-index deps "module-name"` |
| File outline | `ast-index outline "path/to/file.ext"` |
| File imports | `ast-index imports "path/to/file.ext"` |

## Index Management

- `ast-index rebuild` - Full reindex (run once after clone)
- `ast-index update` - After git pull/merge
- `ast-index stats` - Show index statistics

## Polyglot Repo Notes

brigade is a polyglot monorepo:

- `backend/` — Go (ConnectRPC, internal/* по доменам). Ищи `func`, интерфейс
  `Spawner`, ConnectRPC-сервисы, транспортные хендлеры через ast-index.
- `web/` — TypeScript/React (xterm.js, CopilotKit). Компоненты, хуки,
  Connect-клиент. Treat `.ts`/`.tsx` как первоклассный код.
- `mobile/` — Kotlin Multiplatform + Compose. См. KMP-заметки ниже.
- `proto/` — контракт (источник истины). Сгенеренное в `backend/gen/go` и
  `web/src/api/gen` — производное, правится через `proto/` + кодген.

## Kotlin Multiplatform Notes

- Treat `commonMain`, `commonTest`, and platform source sets (`androidMain`,
  `iosMain`, etc.) as first-class code, not support files.
- When explaining behavior, consider both Kotlin `expect`/`actual` edges and
  Swift/ObjC interop.
- Do not default to Android-only guidance in a KMP repo.

## Go Notes

- Use `ast-index symbol "Name"` / `ast-index callers "fn"` instead of grep for
  Go identifiers; `ast-index implementations "Spawner"` to find реализации
  интерфейсов.
- `ast-index outline "backend/internal/<pkg>/<file>.go"` для обзора пакета.

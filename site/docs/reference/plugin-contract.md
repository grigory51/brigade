# Контракт плагина

Brigade не вводит собственный transport или формат UI. Плагин — стандартный
[MCPB](https://github.com/modelcontextprotocol/mcpb) bundle с локальным MCP-сервером;
интерфейс — стандартный [MCP App](https://github.com/modelcontextprotocol/ext-apps).

## Manifest

Поддерживаются `manifest_version` 0.3 и 0.4, server types `binary`, `node` и `python`.
Brigade требует одно расширение manifest:

```json
{
  "manifest_version": "0.3",
  "name": "example",
  "display_name": "Example",
  "description": "Example workspace",
  "version": "1.0.0",
  "server": {
    "type": "binary",
    "entry_point": "server/example",
    "mcp_config": { "args": [], "env": {} }
  },
  "_meta": {
    "brigade": {
      "experience": {
        "entry_tool": "example.open",
        "cover": "ui/cover.svg"
      }
    }
  }
}
```

`entry_tool` должен быть MCP tool с `ui://` resource по спецификации MCP Apps. Brigade
вызывает его без аргументов, читает связанный HTML resource и размещает приложение в
sandboxed iframe. В iframe доступны стандартные MCP Apps вызовы tools, resources,
prompts, `openLink` и `downloadFile`; произвольного доступа к родительской странице нет.
Необязательный `cover` — путь внутри bundle к SVG, PNG, JPEG или WebP до 1 MiB; обложка
показывается в выборе интерфейса новой сессии.

Сервер получает `BRIGADE_SESSION_ID` и `BRIGADE_WORKSPACE`. Агент подключается к одному
экземпляру MCP-сервера, MCP Apps host — ко второму; общее состояние храните атомарно в
workspace, а не в памяти процесса.

## Общий UI SDK

Каждый релиз Brigade прикладывает `brigade-plugin-ui.tgz` — минимальный пакет общих
токенов, toolbar/button styles и фабрики MCP App:

```json
{
  "dependencies": {
    "@brigade/plugin-ui": "https://github.com/grigory51/brigade/releases/latest/download/brigade-plugin-ui.tgz",
    "@modelcontextprotocol/ext-apps": "^1.7.5"
  }
}
```

```ts
import { createBrigadeApp } from "@brigade/plugin-ui";
import "@brigade/plugin-ui/styles.css";

const app = createBrigadeApp("Example", "1.0.0");
app.ontoolresult = (result) => console.log(result.structuredContent);
void app.connect();
```

Собирайте UI в один self-contained HTML: bundle не должен зависеть от dev server или CDN.
Проверка перед установкой:

```bash
brigade plugin validate ./example.mcpb
```

## Безопасность и жизненный цикл

- MCPB устанавливает оператор, а не пользователь: бинарь исполняется с правами агента.
- Remote URL должен использовать HTTPS; symlink, path traversal и bundle больше 1 GiB
  отклоняются.
- В Docker bundle копируется в durable home сессии; отдельный plugin-контейнер не нужен.
- Сессия закрепляет точную версию. Публикуйте новую версию manifest вместо замены бинаря
  под существующей версией.

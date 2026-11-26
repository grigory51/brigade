# Контракт плагина

Brigade не вводит собственный transport или формат UI. Плагин — стандартный
[MCPB](https://github.com/modelcontextprotocol/mcpb) bundle с локальным MCP-сервером;
необязательная предметная панель — стандартный
[MCP App](https://github.com/modelcontextprotocol/ext-apps). Host всегда владеет ACP-чатом,
историей, композером и запросами разрешений. MCP App не является альтернативным клиентом
сессии.

## Manifest

Поддерживаются `manifest_version` 0.3 и 0.4, server types `binary`, `node`, `python` и `uv`.
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
        "layout": "split",
        "cover": "ui/cover.svg",
        "instructions": "Use example.build for domain tasks."
      }
    }
  }
}
```

`entry_tool` должен быть MCP tool с `ui://` resource по спецификации MCP Apps. Brigade
вызывает его без аргументов, читает связанный HTML resource и размещает приложение в
sandboxed iframe внутри `aside`. На desktop `aside` находится справа от штатного
`AcpThread`, на mobile — под ним. Панель не должна реализовывать собственную историю,
композер или UI разрешений.

`layout` сохранён для совместимости manifest. Значения `""`, `"full"` и `"split"`
нормализуются в `"split"` в `ParseManifest`; другие значения отклоняются. Отдельных
full-screen и empty-компоновок плагина больше нет.

Необязательный `cover` — путь внутри bundle к SVG, PNG, JPEG или WebP до 1 MiB; обложка
показывается при выборе панели новой ACP-сессии.

Brigade добавляет в developer instructions агента название и описание выбранной панели,
чтобы агент использовал её MCP tools. Необязательный `instructions` уточняет предметную
роль, основной tool и ожидаемый результат. Этот текст не добавляется к сообщениям
пользователя.

Сервер получает `BRIGADE_SESSION_ID` и `BRIGADE_WORKSPACE`. Агент подключается к одному
экземпляру MCP-сервера, MCP Apps host — ко второму; общее состояние храните атомарно в
workspace, а не в памяти процесса.

Выбранный bundle закрепляется за сессией по ID, версии и владельцу. Wire-поля
`experience_id` и `experience_version`, сохранённый owner bundle и RPC `SetExperience`
остаются для совместимости, но теперь описывают необязательную pane, а не вид клиента.
Смена pane разрешена только для живой idle ACP-сессии. Она меняет MCP и developer
instructions и переинициализирует адаптер с тем же native session ID; история, workspace
и Docker-контейнер, если он используется, сохраняются. Горячая смена во время генерации
не поддерживается.

Стандартный `user_config` MCPB поддерживается для типов `string`, `number`, `boolean`,
`file` и `directory`, включая `multiple` для путей, `required`, `default` и `min/max`.
Значения подставляются в `${user_config.NAME}` при запуске. Поля с `sensitive: true`
хранятся в vault и должны передаваться серверу через `mcp_config.env`.

Поддерживаются runtime-типы `binary`, `node`, `python` и `uv`. Для `uv` нужен manifest
0.4 и установленный runtime; стандартный Docker-образ Brigade уже содержит его.

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

Host объявляет только реализованные интеграции:

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

`brigadeHost` не передаёт snapshots `generating`, `lastMessage`, `prompts` или
`submitMode`. Стандартный `app.sendMessage(...)` остаётся доменным действием приложения:
он отправляет сообщение в штатный ACP-thread, но не передаёт приложению ownership чата.

Для безопасной подготовки запроса приложение вызывает
`app.callServerTool({ name: "brigade.appendPrompt", arguments: { text } })`. Host добавляет
текст в штатный композер, сохраняет уже набранный черновик и ничего не отправляет.
`brigade.cancel` пока поддерживается как совместимое host action для остановки текущего
turn; реализовывать эти `brigade.*` tools в MCP-сервере приложения не нужно.

Для режима отображения host context объявляет только `displayMode: "inline"` и
`availableDisplayModes: ["inline"]`. В iframe также доступны стандартные MCP Apps вызовы
tools, resources, prompts, `openLink` и `downloadFile`; произвольного доступа к
родительской странице нет.

Каждый новый UI-запрос `PluginService.MCP` передаёт необязательный
`expected_plugin: { id, version }`. Host отклоняет запрос, если пользователь успел сменить
панель. Старые клиенты без `expected_plugin` продолжают работать.

Собирайте UI в один self-contained HTML: bundle не должен зависеть от dev server или CDN.
Проверка перед установкой:

```bash
brigade plugin validate ./example.mcpb
```

## Безопасность и жизненный цикл

- Пользователь устанавливает только свои MCP Apps; операторские CLI-установки доступны
  всем как системные.
- В local-режиме бинарь исполняется с правами пользователя Brigade.app; устанавливайте
  только доверенные bundles. В Docker он исполняется внутри контейнера сессии.
- Remote URL должен использовать HTTPS; symlink, path traversal и bundle больше 1 GiB
  отклоняются.
- В Docker bundle копируется в durable home сессии; отдельный plugin-контейнер не нужен.
- Сессия закрепляет точную версию. Публикуйте новую версию manifest вместо замены бинаря
  под существующей версией.
- После установки новой версии явное обновление сессии применяет актуальную установленную
  версию для её runtime-платформы. До этого сессия продолжает использовать закреплённую.

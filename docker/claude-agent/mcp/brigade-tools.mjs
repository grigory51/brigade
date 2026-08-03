#!/usr/bin/env node
// brigade MCP-сервер: экспонирует кастомные UI-инструменты (render_ui, show_choice)
// модели Claude через stdio. Это единственный канал, которым brigade даёт модели
// вызываемые тулы: сток-адаптер claude-agent-acp игнорирует кастомный _meta, но
// пробрасывает ACP mcpServers в Claude Agent SDK. SDK стартует этот сервер как
// subprocess ВНУТРИ контейнера сессии.
//
// UI-инструменты долетают до клиента как tool_call (имя `mcp__brigade__<tool>`), и brigade
// рисует по ним карточку. publish_file проверяет локальный файл и возвращает защищённый URL,
// содержимое файла остаётся в workspace и отдаётся backend'ом только владельцу сессии.
import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { realpath, stat } from "node:fs/promises";
import path from "node:path";
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from "@modelcontextprotocol/sdk/types.js";

// Описание render_ui — единственный канал обучения модели формату A2UI. Держим его
// компактной спецификацией: плоский список компонентов, обязательный root, дети по id.
const RENDER_UI_DESCRIPTION = [
  "Нарисовать пользователю произвольный интерфейс прямо в чате: карточку, форму,",
  "макет, прототип. Передаётся плоский список компонентов (components) — дерево",
  "строится по ссылкам на id, НЕ инлайном.",
  "",
  "Правила:",
  '• Ровно один компонент ДОЛЖЕН иметь id:"root" — это корень дерева.',
  '• Дети указываются строковым id из этого же списка: контейнеры — children:["id1","id2"],',
  '  Card/Button — child:"id".',
  '• Пропсы компонента лежат прямо в его объекте (не в отдельном "props").',
  '• Любой пропс можно связать со значением из dataModel: вместо литерала — {"path":"/поле"}.',
  "",
  "Компоненты и их основные пропсы:",
  "• Text {text, variant?: h1|h2|h3|h4|h5|body|caption} — текст/заголовок (простой Markdown).",
  "• Column|Row {children:[ids], justify?: start|center|end|spaceBetween, align?: start|center|end|stretch}.",
  "• List {children:[ids]} — список.",
  '• Card {child:"id"} — карточка вокруг ОДНОГО ребёнка (несколько — оберни в Column/Row).',
  "• Divider {} — разделитель. Image {url}. Icon {name}.",
  '• Button {child:"id" (обычно Text), action?:{event:{name, context?}}} — кнопка.',
  "• TextField {label, value?, variant?: shortText|longText|number|obscured} — поле ввода.",
  "• CheckBox {label, value}. ChoicePicker {options:[{value,label}], value} — value связан с массивом строк.",
  "• Slider {min?, max, step?, value}.",
  "",
  "Интерактив: чтобы получить ответ пользователя, дай Button с action.event.name; при",
  "нужде положи в action.event.context значения полей ввода через {\"path\":\"/поле\"}. Клик или",
  "submit придёт тебе новым сообщением, и ты сможешь продолжить.",
  "",
  "Пример (карточка: заголовок + текст + кнопка):",
  "components: [",
  '  {"id":"root","component":"Card","child":"col"},',
  '  {"id":"col","component":"Column","children":["t","p","b"]},',
  '  {"id":"t","component":"Text","text":"Тариф Pro","variant":"h3"},',
  '  {"id":"p","component":"Text","text":"Всё из Free плюс приоритетная поддержка."},',
  '  {"id":"blabel","component":"Text","text":"Выбрать Pro"},',
  '  {"id":"b","component":"Button","child":"blabel","action":{"event":{"name":"choose","context":{"plan":"pro"}}}}',
  "]",
].join("\n");

const TOOLS = [
  {
    name: "render_ui",
    description: RENDER_UI_DESCRIPTION,
    inputSchema: {
      type: "object",
      properties: {
        components: {
          type: "array",
          description:
            'Плоский список компонентов A2UI. Ровно один обязан иметь id:"root". ' +
            'Дети — по строковому id (children:[...] или child:"id"), не инлайном. ' +
            "Пропсы компонента лежат прямо в его объекте.",
          items: {
            type: "object",
            properties: {
              id: {
                type: "string",
                description: 'Уникальный id компонента; ровно один — "root".',
              },
              component: {
                type: "string",
                enum: [
                  "Text",
                  "Card",
                  "Column",
                  "Row",
                  "List",
                  "Divider",
                  "Image",
                  "Icon",
                  "Button",
                  "TextField",
                  "CheckBox",
                  "ChoicePicker",
                  "Slider",
                  "Tabs",
                ],
                description: "Тип компонента из basicCatalog.",
              },
            },
            required: ["id", "component"],
            additionalProperties: true,
          },
        },
        dataModel: {
          type: "object",
          additionalProperties: true,
          description:
            "Опц. начальная модель данных: значения для path-биндингов и состояние полей ввода.",
        },
      },
      required: ["components"],
    },
  },
  {
    name: "show_choice",
    description:
      "Показать пользователю карточку с заголовком и набором вариантов выбора.",
    inputSchema: {
      type: "object",
      properties: {
        title: { type: "string", description: "Заголовок карточки" },
        options: {
          type: "array",
          items: { type: "string" },
          description: "Варианты, которые увидит пользователь",
        },
      },
      required: ["title", "options"],
    },
  },
  {
    name: "save_note",
    description: [
      "Показать нативную карточку «Добавить в память» с ЧЕРНОВИКОМ заметки и на этом ОСТАНОВИТЬСЯ.",
      "Пользователь сам правит поля и жмёт «Сохранить» — brigade сохраняет заметку НАПРЯМУЮ, без",
      "твоего участия. После вызова НЕ дёргай никаких API/curl и НЕ жди ответа: сохранение вне тебя.",
      "Заполни поля черновиком из накопленного контекста и промпта пользователя.",
    ].join("\n"),
    inputSchema: {
      type: "object",
      properties: {
        title: { type: "string", description: "Заголовок заметки в одну строку" },
        body: { type: "string", description: "Текст заметки (markdown)" },
        topic: { type: "string", description: "Имя темы, напр. DIY (пусто → «Общее»)" },
        sub: { type: "string", description: "Подтема, напр. 3D (пусто → «Общее»)" },
        type: {
          type: "string",
          enum: ["idea", "decision", "insight", "todo", "question", "reference"],
          description: "Тип заметки",
        },
        tags: { type: "array", items: { type: "string" }, description: "Опц. метки для поиска" },
      },
      required: ["title", "body"],
    },
  },
  {
    name: "publish_file",
    description: [
      "Сделать созданный в workspace файл доступным пользователю для скачивания.",
      "Используй этот инструмент вместо ссылок на локальные абсолютные пути и file://.",
      "Вызов отрисует карточку скачивания в чате. Он должен быть последним действием:",
      "не повторяй ссылку и не пиши текст после него.",
    ].join("\n"),
    inputSchema: {
      type: "object",
      properties: {
        path: {
          type: "string",
          description: "Путь к файлу относительно текущего workspace или абсолютный путь внутри него",
        },
      },
      required: ["path"],
    },
  },
];

const server = new Server(
  { name: "brigade", version: "1.0.0" },
  { capabilities: { tools: {} } },
);

server.setRequestHandler(ListToolsRequestSchema, async () => ({ tools: TOOLS }));

// UI-вызовы возвращают подсказку: их результат рисует клиент. Для save_note сохранение целиком
// на стороне пользователя; publish_file отдельно формирует download-ссылку выше.
server.setRequestHandler(CallToolRequestSchema, async (request) => {
  if (request.params?.name === "publish_file") {
    const requested = request.params.arguments?.path;
    const sessionID = process.env.BRIGADE_SESSION_ID;
    if (typeof requested !== "string" || !requested || !sessionID) {
      return {
        isError: true,
        content: [{ type: "text", text: "Не указан файл или идентификатор сессии." }],
      };
    }
    try {
      const cwd = await realpath(process.cwd());
      const file = await realpath(path.resolve(cwd, requested));
      const relative = path.relative(cwd, file);
      const info = await stat(file);
      if (
        !relative ||
        relative.startsWith(`..${path.sep}`) ||
        path.isAbsolute(relative) ||
        !info.isFile()
      ) {
        throw new Error("path is outside workspace or is not a file");
      }
      const urlPath = relative.split(path.sep).map(encodeURIComponent).join("/");
      const url = `/api/sessions/${encodeURIComponent(sessionID)}/files/${urlPath}`;
      return {
        content: [{ type: "text", text: JSON.stringify({ name: path.basename(file), url }) }],
      };
    } catch (error) {
      return {
        isError: true,
        content: [{ type: "text", text: `Не удалось опубликовать файл: ${error.message}` }],
      };
    }
  }
  const text =
    request.params?.name === "save_note"
      ? "Карточка добавления в память показана. Сохранение — на стороне пользователя (кнопка «Сохранить» шлёт запрос сама); больше ничего делать не нужно."
      : "Интерфейс показан пользователю в чате. Дождись его ответа или действия, прежде чем продолжать.";
  return { content: [{ type: "text", text }] };
});

await server.connect(new StdioServerTransport());

#!/usr/bin/env node
// brigade MCP-сервер: экспонирует кастомные UI-инструменты (render_ui, show_choice)
// модели Claude через stdio. Это единственный канал, которым brigade даёт модели
// вызываемые тулы: сток-адаптер claude-agent-acp игнорирует кастомный _meta, но
// пробрасывает ACP mcpServers в Claude Agent SDK. SDK стартует этот сервер как
// subprocess ВНУТРИ контейнера сессии.
//
// Инструменты ничего не делают серверно — важно лишь, что вызов состоялся: он долетает
// до клиента как tool_call (имя `mcp__brigade__<tool>`), и brigade рисует по нему карточку
// (show_choice) или A2UI-поверхность из аргументов (render_ui). Результат — заглушка,
// подсказывающая модели дождаться ответа пользователя. Сервер stateless.
import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
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
  "• Column|Row {children:[ids], justify?: start|center|end|spaceBetween, alignItems?: start|center|end}.",
  "• List {children:[ids]} — список.",
  '• Card {child:"id"} — карточка вокруг ОДНОГО ребёнка (несколько — оберни в Column/Row).',
  "• Divider {} — разделитель. Image {url}. Icon {name}.",
  '• Button {child:"id" (обычно Text), action?:{event:{name, context?}}} — кнопка.',
  "• TextField {label, value?, variant?: shortText|longText|number|obscured} — поле ввода.",
  "• CheckBox {label, value}. ChoicePicker {options:[{value,label?}]}. Slider {min?, max, step?}.",
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
];

const server = new Server(
  { name: "brigade", version: "1.0.0" },
  { capabilities: { tools: {} } },
);

server.setRequestHandler(ListToolsRequestSchema, async () => ({ tools: TOOLS }));

// Любой вызов — no-op с подсказкой: рисует клиент, а модель должна дождаться реакции
// пользователя, а не продолжать генерировать UI в цикле.
server.setRequestHandler(CallToolRequestSchema, async () => ({
  content: [
    {
      type: "text",
      text: "Интерфейс показан пользователю в чате. Дождись его ответа или действия, прежде чем продолжать.",
    },
  ],
}));

await server.connect(new StdioServerTransport());

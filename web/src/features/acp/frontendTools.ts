// Контракт кастомного frontend-сниппета: имя, описание и JSON Schema аргументов.
// Передаётся агенту через RunAgentInput.tools[] (его регистрирует AcpToolUI), вызов
// агентом транслируется в TOOL_CALL_* и рендерится карточкой в AcpThread.
export type FrontendToolSpec = {
  name: string;
  description: string;
  parameters: Record<string, unknown>;
};

// Демонстрационный кастомный сниппет.
export const DEMO_FRONTEND_TOOLS: FrontendToolSpec[] = [
  {
    name: "show_choice",
    description:
      "Показать пользователю карточку с заголовком и набором вариантов выбора.",
    parameters: {
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

// Имена сниппетов, для которых есть собственный рендер.
export const FRONTEND_TOOL_NAMES = new Set(
  DEMO_FRONTEND_TOOLS.map((t) => t.name),
);

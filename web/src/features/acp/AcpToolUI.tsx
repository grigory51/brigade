import { useAssistantTool } from "@assistant-ui/react";
import type { JSONSchema7 } from "json-schema";
import { DEMO_FRONTEND_TOOLS } from "./frontendTools";

// AcpToolUI регистрирует демонстрационный frontend-tool show_choice на время жизни
// ACP-экрана: assistant-ui кладёт его в RunAgentInput.tools[], откуда бэкенд берёт
// контракт и транслирует вызов в TOOL_CALL_*. Рендер вызова берёт на себя
// ToolFallback в AcpThread (по имени из FRONTEND_TOOL_NAMES), поэтому здесь render
// не задаём — регистрируем только определение инструмента для модели.
//
// type "frontend" без execute означает: модель видит инструмент и вызывает его,
// а UI отображает результат вызова (карточку), не выполняя ничего на клиенте.
export function AcpToolUI() {
  const spec = DEMO_FRONTEND_TOOLS[0];
  useAssistantTool({
    toolName: spec.name,
    type: "frontend",
    description: spec.description,
    parameters: spec.parameters as JSONSchema7,
  });
  return null;
}

// Имена кастомных UI-инструментов, которые brigade рендерит своими карточками. Сами
// инструменты доставляются модели MCP-сервером сессии (docker/claude-agent/mcp/
// brigade-tools.mjs), поэтому до клиента они доходят с префиксом mcp__brigade__; в
// ToolFallback имя сопоставляется по «голому» виду (bareToolName). См. AcpThread.tsx.

// RENDER_UI_TOOL_NAME — generative UI от агента (рендер — RenderUiCard → A2uiSurface).
export const RENDER_UI_TOOL_NAME = "render_ui";

// FRONTEND_TOOL_NAMES — инструменты, которые рисует SnippetCard (простой рендер по имени).
export const FRONTEND_TOOL_NAMES = new Set(["show_choice"]);

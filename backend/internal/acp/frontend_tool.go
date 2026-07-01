package acp

// FrontendTool — описание кастомного компонента («сниппета»), зарегистрированного
// фронтендом. При старте ACP-сессии клиент присылает реестр таких инструментов по WS;
// бэкенд пробрасывает их агенту, чтобы тот мог «показать» компонент через tool_use.
//
// Контракт совпадает с тем, что фронт получает из useFrontendTool (CopilotKit):
// имя инструмента плюс JSON Schema его входных параметров (результат Zod→JSON Schema).
type FrontendTool struct {
	// Name — уникальное имя инструмента; по нему агент вызывает компонент, а фронт
	// сопоставляет tool_use с зарегистрированным рендером.
	Name string `json:"name"`
	// Description — назначение компонента; помогает агенту решить, когда его показать.
	Description string `json:"description"`
	// InputSchema — JSON Schema параметров инструмента (произвольный JSON-объект).
	InputSchema map[string]any `json:"inputSchema"`
}

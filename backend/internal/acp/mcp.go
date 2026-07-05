package acp

import acpsdk "github.com/coder/acp-go-sdk"

// ContainerMCPServerPath — путь к brigade MCP-серверу внутри образа агента
// (docker/claude-agent, см. Dockerfile и mcp/brigade-tools.mjs). Сервер экспонирует
// кастомные UI-инструменты (render_ui, show_choice) модели через stdio.
//
// Почему MCP, а не _meta: сток-адаптер @agentclientprotocol/claude-agent-acp игнорирует
// кастомные meta-ключи, но пробрасывает ACP mcpServers в Claude Agent SDK; SDK стартует
// этот сервер как stdio-subprocess внутри контейнера сессии. Это единственный канал,
// которым brigade даёт модели вызываемые тулы.
const ContainerMCPServerPath = "/opt/brigade-mcp/brigade-tools.mjs"

// BrigadeMCPServer собирает конфиг stdio MCP-сервера brigade для session/new (и load/fork).
// Имя "brigade" задаёт префикс имён инструментов — модель видит mcp__brigade__render_ui, по
// нему их и матчит web-клиент (см. ToolFallback). Stdio-транспорт обязан поддерживаться
// всеми ACP-агентами (в отличие от http/sse, зависящих от capability).
func BrigadeMCPServer() acpsdk.McpServer {
	return acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{
		Name:    "brigade",
		Command: "node",
		Args:    []string{ContainerMCPServerPath},
		Env:     []acpsdk.EnvVariable{},
	}}
}

// mcpServersOrEmpty гарантирует non-nil слайс: NewSession/LoadSession сериализуют поле как
// массив, nil ушёл бы как JSON null.
func mcpServersOrEmpty(servers []acpsdk.McpServer) []acpsdk.McpServer {
	if servers == nil {
		return []acpsdk.McpServer{}
	}
	return servers
}

// toUnstableMcpServers оборачивает стабильные McpServer в unstable-вариант для session/fork.
// brigade использует только stdio-транспорт (тип McpServerStdio общий для обоих вариантов);
// http/sse-поля unstable-типа несовместимы со стабильными и не переносятся — они не
// используются.
func toUnstableMcpServers(servers []acpsdk.McpServer) []acpsdk.UnstableMcpServer {
	out := make([]acpsdk.UnstableMcpServer, 0, len(servers))
	for _, s := range servers {
		out = append(out, acpsdk.UnstableMcpServer{Stdio: s.Stdio})
	}
	return out
}

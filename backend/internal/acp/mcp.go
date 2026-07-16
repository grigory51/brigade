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

// localMCPServerPath — путь к brigade-tools.mjs на ХОСТЕ для local/desktop-режима, где
// контейнерного /opt/brigade-mcp нет. Задаётся при старте из desktop-обёртки по бандлу
// (Resources/brigade-mcp, см. SetLocalMCPServerPath). Пусто → используется контейнерный путь
// (docker). Без валидного пути node-subprocess MCP-сервера не стартует и кастомные UI-инструменты
// (render_ui/show_choice, т.е. карточки /note) недоступны — агент падает в текстовый fallback.
var localMCPServerPath string

// SetLocalMCPServerPath задаёт хостовый путь к MCP-серверу для local/desktop-режима. Вызывается
// один раз при старте (до создания сессий), поэтому синхронизация не нужна.
func SetLocalMCPServerPath(p string) { localMCPServerPath = p }

// BrigadeMCPServer собирает конфиг stdio MCP-сервера brigade для session/new (и load/fork).
// Имя "brigade" задаёт префикс имён инструментов — модель видит mcp__brigade__render_ui, по
// нему их и матчит web-клиент (см. ToolFallback). Stdio-транспорт обязан поддерживаться
// всеми ACP-агентами (в отличие от http/sse, зависящих от capability). Путь к скрипту — по
// режиму: local/desktop (localMCPServerPath) либо контейнерный (docker) по умолчанию.
func BrigadeMCPServer() acpsdk.McpServer {
	path := ContainerMCPServerPath
	if localMCPServerPath != "" {
		path = localMCPServerPath
	}
	return acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{
		Name:    "brigade",
		Command: "node",
		Args:    []string{path},
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

// pluginsMeta собирает _meta.claudeCode.options.plugins для session/new|load|fork из путей
// локальных плагинов. Формат — как ждёт Claude Agent SDK: [{type:"local",path}]. nil, если
// плагинов нет (не шлём пустой _meta). Адаптер спредит claudeCode.options в опции SDK-запроса
// (acp-agent.js: userProvidedOptions = sessionMeta.claudeCode.options), поэтому это
// единственный канал загрузки плагина в неинтерактивный агент.
func pluginsMeta(pluginDirs []string) map[string]any {
	if len(pluginDirs) == 0 {
		return nil
	}
	plugins := make([]map[string]any, 0, len(pluginDirs))
	for _, d := range pluginDirs {
		plugins = append(plugins, map[string]any{"type": "local", "path": d})
	}
	return map[string]any{
		"claudeCode": map[string]any{
			"options": map[string]any{"plugins": plugins},
		},
	}
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

package acp

import acpsdk "github.com/coder/acp-go-sdk"

// Сервер экспонирует кастомные UI-инструменты (render_ui, show_choice) модели через stdio.
// Путь к скрипту в среде агента задаёт манифест агента (agent.Type.McpServerScript) —
// сервер приезжает в контейнер runtime-слоем, а не берётся из образа сессии.
//
// Почему MCP, а не _meta: сток-адаптер @agentclientprotocol/claude-agent-acp игнорирует
// кастомные meta-ключи, но пробрасывает ACP mcpServers в Claude Agent SDK; SDK стартует
// этот сервер как stdio-subprocess внутри контейнера сессии. Это единственный канал,
// которым brigade даёт модели вызываемые тулы.

// localMCPServerPath — путь к brigade-tools.mjs на ХОСТЕ для local/desktop-режима, где
// контейнерного /opt/brigade-mcp нет. Задаётся при старте из desktop-обёртки по бандлу
// (Resources/brigade-mcp, см. SetLocalMCPServerPath). Пусто → используется контейнерный путь
// (docker). Без валидного пути node-subprocess MCP-сервера не стартует и кастомные UI-инструменты
// (render_ui/show_choice, т.е. карточки /note) недоступны — агент падает в текстовый fallback.
var localMCPServerPath string

// SetLocalMCPServerPath задаёт хостовый путь к MCP-серверу для local/desktop-режима. Вызывается
// один раз при старте (до создания сессий), поэтому синхронизация не нужна.
func SetLocalMCPServerPath(p string) { localMCPServerPath = p }

// LocalMCPServerPath — хостовый путь к MCP-серверу (пусто, если не задан). Выбор между ним и
// контейнерным путём делает реестр по режиму сессии: в desktop оба варианта задаются
// одновременно, но хостовый путь внутри контейнера не существует.
func LocalMCPServerPath() string { return localMCPServerPath }

// BrigadeMCPServer собирает конфиг stdio MCP-сервера brigade для session/new и load.
// Имя "brigade" задаёт префикс имён инструментов — модель видит mcp__brigade__render_ui, по
// нему их и матчит web-клиент (см. ToolFallback). Stdio-транспорт обязан поддерживаться
// всеми ACP-агентами (в отличие от http/sse, зависящих от capability).
//
// script — путь к скрипту в среде агента: контейнерный (runtime-слой) либо хостовый
// (local/desktop, см. LocalMCPServerPath). sessionID передаётся именно в env MCP-процесса:
// ACP-агент не обязан наследовать окружение адаптера при запуске stdio-сервера.
func BrigadeMCPServer(script, sessionID string) acpsdk.McpServer {
	return acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{
		Name:    "brigade",
		Command: "node",
		Args:    []string{script},
		Env:     []acpsdk.EnvVariable{{Name: "BRIGADE_SESSION_ID", Value: sessionID}},
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

// sessionMeta собирает расширения Claude ACP для session/new|load: локальные плагины
// и дополнение стандартного system prompt. Другие ACP-агенты игнорируют неизвестный _meta.
func sessionMeta(pluginDirs []string, systemPrompt string) map[string]any {
	if len(pluginDirs) == 0 && systemPrompt == "" {
		return nil
	}
	meta := make(map[string]any, 2)
	if len(pluginDirs) > 0 {
		plugins := make([]map[string]any, 0, len(pluginDirs))
		for _, d := range pluginDirs {
			plugins = append(plugins, map[string]any{"type": "local", "path": d})
		}
		meta["claudeCode"] = map[string]any{
			"options": map[string]any{"plugins": plugins},
		}
	}
	if systemPrompt != "" {
		meta["systemPrompt"] = map[string]any{"append": systemPrompt}
	}
	return meta
}

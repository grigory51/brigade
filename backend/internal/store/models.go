package store

import "time"

// Доменные сущности хранилища и типизированные значения колонок-перечислений.
// В БД эти значения лежат строками в нижнем регистре; маппинг в proto-перечисления
// (brigade.v1.SessionMode/Kind/Status) выполняет транспортный слой, а не store.

// SessionMode — где исполняется агент сессии.
type SessionMode string

const (
	SessionModeLocal  SessionMode = "local"
	SessionModeDocker SessionMode = "docker"
)

// SessionKind — тип взаимодействия с агентом.
type SessionKind string

const (
	SessionKindCLI SessionKind = "cli"
	SessionKindACP SessionKind = "acp"
)

// SessionStatus — текущее состояние сессии в реестре.
type SessionStatus string

const (
	SessionStatusRunning SessionStatus = "running"
	SessionStatusStopped SessionStatus = "stopped"
	SessionStatusFailed  SessionStatus = "failed"
)

// User — учётная запись. PasswordHash — bcrypt-хеш, не сам пароль.
type User struct {
	ID           string
	Username     string
	PasswordHash string
	CreatedAt    time.Time
}

// UserSettings — персональные настройки пользователя. ClaudeToken — подписочный
// токен Claude Code; MemoryRemote — git-репо личной памяти (доступ к git@-remote идёт по
// SSH-ключу агента, см. auth.EnsureAgentSSHKey). Секреты в БД шифруются, наружу (в API)
// значения не отдаются. GetUserSettings возвращает их уже расшифрованными.
type UserSettings struct {
	UserID       string
	ClaudeToken  string
	MemoryRemote string
	// NtfyServer/NtfyTopic — адрес сервера ntfy и топик push-уведомлений (не секреты).
	// NtfyToken — токен публикации в топик (секрет, шифруется). NtfyEvents — CSV включённых
	// событий (напр. "turn_end,error").
	NtfyServer string
	NtfyTopic  string
	NtfyToken  string
	NtfyEvents string
	// AgentImages — образы контейнеров агента, доступные пользователю при создании сессии.
	// В БД лежат JSON-массивом.
	AgentImages []string
}

// Session — сессия агента. Поля agent_session_id и container_label несут
// данные для восстановления (resume) после рестарта бэкенда.
type Session struct {
	ID             string
	UserID         string
	Mode           SessionMode
	Kind           SessionKind
	AgentType      string
	AgentSessionID string
	ContainerLabel string
	Status         SessionStatus
	Cwd            string
	CreatedAt      time.Time
	// Name — пользовательское имя сессии для отображения. Пустое — клиент показывает
	// производную подпись (тип агента + вид).
	Name string
	// ParentID — сессия-родитель для веток (Fork). Пустое — корневая сессия.
	ParentID string
	// McpServers — идентификаторы MCP-серверов пользователя, включённых в этой сессии
	// (только ACP). В БД лежат CSV-строкой.
	McpServers []string
	// Image — образ контейнера сессии (docker-режим). Пусто — базовый образ brigade.
	Image string
}

// McpTransport — способ связи агента с MCP-сервером.
type McpTransport string

const (
	McpTransportStdio McpTransport = "stdio"
	McpTransportHTTP  McpTransport = "http"
	McpTransportSSE   McpTransport = "sse"
)

// McpKeyValue — переменная окружения (stdio) или HTTP-заголовок (http|sse). Value — либо
// литерал, либо ссылка "${secret.NAME}" на секрет пользователя (см. UserSecret).
type McpKeyValue struct {
	Name  string
	Value string
}

// McpServer — персональный MCP-сервер пользователя. Заполнены поля своего транспорта:
// stdio — Command/Args/Env, http|sse — URL/Headers.
type McpServer struct {
	ID        string
	UserID    string
	Name      string
	Transport McpTransport
	Command   string
	Args      []string
	Env       []McpKeyValue
	URL       string
	Headers   []McpKeyValue
	CreatedAt time.Time
}

// UserSecret — запись vault без значения: наружу отдаются только имя и время изменения.
// Значения читает лишь сервер, собирая конфиг MCP (см. Store.SecretValues).
type UserSecret struct {
	Name      string
	UpdatedAt time.Time
}

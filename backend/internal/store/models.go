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
	UserID              string
	ClaudeToken         string
	CodexAPIKey         string
	CodexAuthJSON       string
	CodexDefaultProfile string
	MemoryRemote        string
	// AgentImages — образы контейнеров агента, доступные пользователю при создании сессии.
	// В БД лежат JSON-массивом.
	AgentImages []string
}

// AgentConnection — отдельная учётная запись агента. Secret хранится зашифрованным и
// возвращается только backend-коду, в API уходит лишь флаг его наличия.
type AgentConnection struct {
	ID          string
	UserID      string
	Name        string
	AgentType   string
	AuthProfile string
	Secret      string
	CreatedAt   time.Time
}

// NotificationBackend — именованное подключение транспорта уведомлений пользователя.
// Config содержит несекретную конфигурацию реализации, Secret — расшифрованный секрет.
type NotificationBackend struct {
	ID     string
	UserID string
	Kind   string
	Name   string
	Config string
	Secret string
	Events string
}

// TelegramBot — персональный бот пользователя и шаблон ACP-сессий, создаваемых из
// Telegram. Token расшифровывается только внутри backend и никогда не возвращается в API.
type TelegramBot struct {
	ID                    string
	UserID                string
	Token                 string
	TelegramID            int64
	Username              string
	Name                  string
	OwnerTelegramID       int64
	OwnerTelegramUsername string
	AgentType             string
	AuthProfile           string
	Image                 string
	McpServers            []string
	BindTokenHash         string
	BindTokenExpiresAt    time.Time
	UpdateOffset          int64
	SupportsGuestQueries  bool
	HasTopicsEnabled      bool
	CreatedAt             time.Time
}

// TelegramUpdate — durable inbox: Telegram подтверждается после записи сюда, а не после
// долгого ACP-turn. Состояние ready означает, что ответ агента уже сохранён и его можно
// безопасно повторно доставить после рестарта.
type TelegramUpdate struct {
	BotID     string
	UpdateID  int64
	Payload   string
	State     string
	Response  string
	Error     string
	CreatedAt time.Time
}

// TelegramConversation связывает Telegram-топик с одной Brigade-сессией. Scope разделяет
// обычные и guest-чаты: Telegram предупреждает, что их chat_id могут совпадать.
type TelegramConversation struct {
	BotID     string
	Scope     string
	ChatID    int64
	ThreadID  int64
	SessionID string
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
	// AuthProfile фиксирует способ авторизации агента на весь срок жизни сессии.
	AuthProfile string
	// InstructionProfile — внутренний профиль поведения агента. Он применяется как
	// system/developer instructions и не добавляется в историю сообщений.
	InstructionProfile   string
	ResponseProfileID    string
	ResponseProfileName  string
	ResponseInstructions string
}

type ResponseProfile struct {
	ID           string
	UserID       string
	Name         string
	Instructions string
	CreatedAt    time.Time
	UpdatedAt    time.Time
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

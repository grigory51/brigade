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
	// Archived — сессия в архиве: контейнер остановлен, чат доступен только для чтения
	// из снимка истории (session_snapshots). Не показывается в основном списке.
	Archived bool
	// Summary — краткий пересказ (recap) сессии от агента, генерируется при архивации.
	Summary string
}

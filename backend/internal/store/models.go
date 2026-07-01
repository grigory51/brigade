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
}

// RefreshToken — выданный refresh-токен. TokenHash — хеш предъявляемого секрета.
type RefreshToken struct {
	ID        string
	UserID    string
	TokenHash string
	ExpiresAt time.Time
	Revoked   bool
	CreatedAt time.Time
}

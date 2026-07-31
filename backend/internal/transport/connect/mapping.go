package connectsvc

import (
	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/memory"
	"github.com/grigory51/brigade/backend/internal/store"
)

// Маппинг между proto-перечислениями и строковыми значениями store сосредоточен здесь —
// store намеренно не знает про proto, а реестр сессий оперирует store-типами.

// modeToProto — обратный маппинг режима сессии в proto (сессия несёт режим в ответах;
// выбор режима при создании убран — он берётся из конфига инстанса).
func modeToProto(m store.SessionMode) v1.SessionMode {
	switch m {
	case store.SessionModeDocker:
		return v1.SessionMode_SESSION_MODE_DOCKER
	case store.SessionModeLocal:
		return v1.SessionMode_SESSION_MODE_LOCAL
	default:
		return v1.SessionMode_SESSION_MODE_UNSPECIFIED
	}
}

func kindFromProto(k v1.SessionKind) store.SessionKind {
	switch k {
	case v1.SessionKind_SESSION_KIND_ACP:
		return store.SessionKindACP
	default:
		// UNSPECIFIED и CLI трактуются как CLI.
		return store.SessionKindCLI
	}
}

func kindToProto(k store.SessionKind) v1.SessionKind {
	switch k {
	case store.SessionKindACP:
		return v1.SessionKind_SESSION_KIND_ACP
	case store.SessionKindCLI:
		return v1.SessionKind_SESSION_KIND_CLI
	default:
		return v1.SessionKind_SESSION_KIND_UNSPECIFIED
	}
}

func statusToProto(s store.SessionStatus) v1.SessionStatus {
	switch s {
	case store.SessionStatusRunning:
		return v1.SessionStatus_SESSION_STATUS_RUNNING
	case store.SessionStatusStopped:
		return v1.SessionStatus_SESSION_STATUS_STOPPED
	case store.SessionStatusFailed:
		return v1.SessionStatus_SESSION_STATUS_FAILED
	default:
		return v1.SessionStatus_SESSION_STATUS_UNSPECIFIED
	}
}

// sessionToProto переводит доменную сессию store в proto-сообщение.
func sessionToProto(s store.Session) *v1.Session {
	return &v1.Session{
		Id:             s.ID,
		UserId:         s.UserID,
		Mode:           modeToProto(s.Mode),
		Kind:           kindToProto(s.Kind),
		AgentType:      s.AgentType,
		AgentSessionId: s.AgentSessionID,
		ContainerLabel: s.ContainerLabel,
		Status:         statusToProto(s.Status),
		Cwd:            s.Cwd,
		CreatedAt:      s.CreatedAt.Unix(),
		Name:           s.Name,
		ParentId:       s.ParentID,
		McpServerIds:   s.McpServers,
		Image:          s.Image,
		AuthProfile:    s.AuthProfile,
	}
}

// archivedToProto переводит заархивированную сессию (она живёт в личной памяти, а не в БД)
// в то же proto-сообщение Session: клиенту архив выглядит списком сессий с recap'ом.
// Полей живой сессии (режим, контейнер, cwd) у архива нет — агента за ней уже не стоит.
func archivedToProto(s memory.ArchivedSession) *v1.Session {
	return &v1.Session{
		Id:        s.ID,
		Kind:      kindToProto(store.SessionKind(s.Kind)),
		AgentType: s.AgentType,
		Status:    v1.SessionStatus_SESSION_STATUS_STOPPED,
		CreatedAt: s.Created.Unix(),
		Name:      s.Name,
		ParentId:  s.ParentID,
		Archived:  true,
		Summary:   s.Summary,
	}
}

package connectsvc

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/memory"
	"github.com/grigory51/brigade/backend/internal/preview"
	"github.com/grigory51/brigade/backend/internal/store"
)

// errSessionNotFound — общая для ACP/bridge ошибка «сессия недоступна» (неизвестна,
// чужая или не в нужном режиме). Детали наружу не раскрываются.
var errSessionNotFound = errors.New("session not found")

// sessionOwner резолвит владельца сессии по её id — чтобы запись из сессии шла в память
// ВЛАДЕЛЬЦА сессии (HMAC-токен подтверждает доступ к сессии, а не личность пользователя).
// Реализуется *store.Store.
type sessionOwner interface {
	GetSession(ctx context.Context, id string) (store.Session, error)
}

// AgentBridgeService реализует brigade.v1.AgentBridgeService — вызовы ИЗ сессии
// (агент/скилл внутри контейнера), а не от веб-клиента. Регистрируется БЕЗ
// JWT-интерсептора: авторизация — per-session HMAC-токен, который сервис проверяет сам
// по заголовку Authorization.
type AgentBridgeService struct {
	previews *preview.Service
	memory   *memory.Service
	sessions sessionOwner
}

// NewAgentBridgeService собирает реализацию AgentBridgeService.
func NewAgentBridgeService(previews *preview.Service, mem *memory.Service, sessions sessionOwner) *AgentBridgeService {
	return &AgentBridgeService{previews: previews, memory: mem, sessions: sessions}
}

// verifySession проверяет per-session HMAC-токен из заголовка Authorization против
// session_id тела. Общая авторизация для вызовов ИЗ сессии.
func (s *AgentBridgeService) verifySession(req interface{ Header() http.Header }, sessionID string) error {
	if sessionID == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("session_id required"))
	}
	token, ok := strings.CutPrefix(req.Header().Get("Authorization"), "Bearer ")
	if !ok || !s.previews.VerifyToken(sessionID, strings.TrimSpace(token)) {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("invalid preview token"))
	}
	return nil
}

// CreateMemoryNote сохраняет заметку в личную память (то же ядро, что MemoryService).
// Авторизация — per-session HMAC-токен; session_id фиксируется как провенанс.
func (s *AgentBridgeService) CreateMemoryNote(ctx context.Context, req *connect.Request[v1.CreateMemoryNoteRequest]) (*connect.Response[v1.CreateMemoryNoteResponse], error) {
	if err := s.verifySession(req, req.Msg.SessionId); err != nil {
		return nil, err
	}
	// Заметка идёт в память ВЛАДЕЛЬЦА сессии — резолвим его по session_id.
	sess, err := s.sessions.GetSession(ctx, req.Msg.SessionId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errSessionNotFound)
	}
	n, sha, err := s.memory.CreateNoteInTopic(ctx, sess.UserID, req.Msg.Topic, memory.Note{
		Title:   req.Msg.Title,
		Body:    req.Msg.Body,
		Type:    req.Msg.Type,
		Tags:    req.Msg.Tags,
		Session: req.Msg.SessionId,
		Layer:   req.Msg.Layer,
		Sub:     req.Msg.Sub,
		From:    "чат",
	})
	if err != nil {
		return nil, memoryError(err)
	}
	return connect.NewResponse(&v1.CreateMemoryNoteResponse{Id: n.ID, CommitSha: sha}), nil
}

// RegisterPreview фиксирует dev-сервер сессии (upsert по порту) и возвращает публичный
// URL. Авторизация — Bearer HMAC-токен сессии (BRIGADE_PREVIEW_TOKEN); session_id из
// тела должен соответствовать токену.
func (s *AgentBridgeService) RegisterPreview(ctx context.Context, req *connect.Request[v1.RegisterPreviewRequest]) (*connect.Response[v1.RegisterPreviewResponse], error) {
	if err := s.verifySession(req, req.Msg.SessionId); err != nil {
		return nil, err
	}
	if req.Msg.Port < 1 || req.Msg.Port > 65535 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("port out of range"))
	}

	reg := s.previews.Register(req.Msg.SessionId, int(req.Msg.Port), req.Msg.Name)
	return connect.NewResponse(&v1.RegisterPreviewResponse{Url: reg.URL}), nil
}

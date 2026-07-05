package connectsvc

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/preview"
)

// errSessionNotFound — общая для ACP/bridge ошибка «сессия недоступна» (неизвестна,
// чужая или не в нужном режиме). Детали наружу не раскрываются.
var errSessionNotFound = errors.New("session not found")

// AgentBridgeService реализует brigade.v1.AgentBridgeService — вызовы ИЗ сессии
// (агент/скилл внутри контейнера), а не от веб-клиента. Регистрируется БЕЗ
// JWT-интерсептора: авторизация — per-session HMAC-токен, который сервис проверяет сам
// по заголовку Authorization.
type AgentBridgeService struct {
	previews *preview.Service
}

// NewAgentBridgeService собирает реализацию AgentBridgeService.
func NewAgentBridgeService(previews *preview.Service) *AgentBridgeService {
	return &AgentBridgeService{previews: previews}
}

// RegisterPreview фиксирует dev-сервер сессии (upsert по порту) и возвращает публичный
// URL. Авторизация — Bearer HMAC-токен сессии (BRIGADE_PREVIEW_TOKEN); session_id из
// тела должен соответствовать токену.
func (s *AgentBridgeService) RegisterPreview(ctx context.Context, req *connect.Request[v1.RegisterPreviewRequest]) (*connect.Response[v1.RegisterPreviewResponse], error) {
	sessionID := req.Msg.SessionId
	if sessionID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("session_id required"))
	}
	token, ok := strings.CutPrefix(req.Header().Get("Authorization"), "Bearer ")
	if !ok || !s.previews.VerifyToken(sessionID, strings.TrimSpace(token)) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid preview token"))
	}
	if req.Msg.Port < 1 || req.Msg.Port > 65535 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("port out of range"))
	}

	reg := s.previews.Register(sessionID, int(req.Msg.Port), req.Msg.Name)
	return connect.NewResponse(&v1.RegisterPreviewResponse{Url: reg.URL}), nil
}

package connectsvc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/auth"
	"github.com/grigory51/brigade/backend/internal/preview"
	"github.com/grigory51/brigade/backend/internal/session"
	"github.com/grigory51/brigade/backend/internal/store"
)

// SessionService реализует brigade.v1.SessionService поверх реестра живых сессий и
// хранилища одноразовых WS-тикетов.
type SessionService struct {
	registry *session.Registry
	tickets  *auth.TicketStore
	previews *preview.Service
}

// NewSessionService собирает реализацию SessionService.
func NewSessionService(registry *session.Registry, tickets *auth.TicketStore, previews *preview.Service) *SessionService {
	return &SessionService{registry: registry, tickets: tickets, previews: previews}
}

// Create создаёт сессию для аутентифицированного пользователя и спавнит агента.
func (s *SessionService) Create(ctx context.Context, req *connect.Request[v1.CreateSessionRequest]) (*connect.Response[v1.CreateSessionResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}

	sess, err := s.registry.Create(ctx, userID,
		kindFromProto(req.Msg.Kind),
		req.Msg.AgentType, req.Msg.Cwd, req.Msg.Prompt)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&v1.CreateSessionResponse{Session: sessionToProto(sess)}), nil
}

// List возвращает сессии текущего пользователя.
func (s *SessionService) List(ctx context.Context, _ *connect.Request[v1.ListSessionsRequest]) (*connect.Response[v1.ListSessionsResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}

	sessions, err := s.registry.List(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	out := make([]*v1.Session, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, sessionToProto(sess))
	}
	return connect.NewResponse(&v1.ListSessionsResponse{Sessions: out}), nil
}

// Get возвращает одну сессию текущего пользователя.
func (s *SessionService) Get(ctx context.Context, req *connect.Request[v1.GetSessionRequest]) (*connect.Response[v1.GetSessionResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}

	sess, err := s.registry.Get(ctx, req.Msg.SessionId, userID)
	if err != nil {
		return nil, sessionError(err)
	}
	return connect.NewResponse(&v1.GetSessionResponse{Session: sessionToProto(sess)}), nil
}

// Fork создаёт ветку сессии: агент клонирует свою сессию с историей, brigade заводит
// новую запись с parent_id. Ветка живёт и продолжается независимо от родителя.
func (s *SessionService) Fork(ctx context.Context, req *connect.Request[v1.ForkSessionRequest]) (*connect.Response[v1.ForkSessionResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}

	sess, err := s.registry.Fork(ctx, req.Msg.SessionId, userID)
	if err != nil {
		return nil, sessionError(err)
	}
	return connect.NewResponse(&v1.ForkSessionResponse{Session: sessionToProto(sess)}), nil
}

// Update меняет отображаемое имя сессии пользователя.
func (s *SessionService) Update(ctx context.Context, req *connect.Request[v1.UpdateSessionRequest]) (*connect.Response[v1.UpdateSessionResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}

	sess, err := s.registry.Rename(ctx, req.Msg.SessionId, userID, req.Msg.Name)
	if err != nil {
		return nil, sessionError(err)
	}
	return connect.NewResponse(&v1.UpdateSessionResponse{Session: sessionToProto(sess)}), nil
}

// Stop останавливает сессию пользователя (агент завершается, статус → stopped).
func (s *SessionService) Stop(ctx context.Context, req *connect.Request[v1.StopSessionRequest]) (*connect.Response[v1.Empty], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}

	if err := s.registry.Stop(ctx, req.Msg.SessionId, userID); err != nil {
		return nil, sessionError(err)
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

// Delete останавливает сессию (если жива) и удаляет её запись.
func (s *SessionService) Delete(ctx context.Context, req *connect.Request[v1.DeleteSessionRequest]) (*connect.Response[v1.Empty], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}

	if err := s.registry.Delete(ctx, req.Msg.SessionId, userID); err != nil {
		return nil, sessionError(err)
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

// IssueStreamTicket выпускает одноразовый тикет для апгрейда WS к указанной сессии.
// Тикет привязан к пользователю и session_id; сессия проверяется на принадлежность.
func (s *SessionService) IssueStreamTicket(ctx context.Context, req *connect.Request[v1.IssueStreamTicketRequest]) (*connect.Response[v1.IssueStreamTicketResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}

	// Тикет имеет смысл только для существующей сессии пользователя.
	if _, err := s.registry.Get(ctx, req.Msg.SessionId, userID); err != nil {
		return nil, sessionError(err)
	}

	token, err := s.tickets.Issue(userID, req.Msg.SessionId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.IssueStreamTicketResponse{Ticket: token}), nil
}

// ListPreviews возвращает preview-эндпоинты сессии, зарегистрированные агентом.
// Сессия проверяется на принадлежность пользователю (чужая — NotFound).
func (s *SessionService) ListPreviews(ctx context.Context, req *connect.Request[v1.ListPreviewsRequest]) (*connect.Response[v1.ListPreviewsResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.registry.Get(ctx, req.Msg.SessionId, userID); err != nil {
		return nil, sessionError(err)
	}

	regs := s.previews.List(req.Msg.SessionId)
	out := make([]*v1.Preview, 0, len(regs))
	for _, reg := range regs {
		out = append(out, &v1.Preview{Port: int32(reg.Port), Name: reg.Name, Url: reg.URL})
	}
	return connect.NewResponse(&v1.ListPreviewsResponse{Previews: out}), nil
}

// requireUser извлекает аутентифицированного пользователя; иначе — Unauthenticated.
func requireUser(ctx context.Context) (string, error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	return u.ID, nil
}

// sessionError транслирует доменные ошибки реестра/store в connect-коды.
func sessionError(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	if errors.Is(err, session.ErrTeardownInProgress) {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

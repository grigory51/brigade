package connectsvc

import (
	"context"

	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/session"
)

// ArchiveService реализует brigade.v1.ArchiveService — чтение архива сессий: список
// архивных сессий (карточки title+summary) и снимок истории чата для readonly-просмотра
// без живого агента. Сам переход в архив (recap + снимок + остановка контейнера)
// выполняет SessionService.Archive.
type ArchiveService struct {
	registry *session.Registry
}

// NewArchiveService собирает реализацию ArchiveService.
func NewArchiveService(registry *session.Registry) *ArchiveService {
	return &ArchiveService{registry: registry}
}

// List отдаёт архивные сессии пользователя (новые первыми).
func (s *ArchiveService) List(ctx context.Context, _ *connect.Request[v1.ListArchivedRequest]) (*connect.Response[v1.ListArchivedResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.registry.ListArchived(ctx, userID)
	if err != nil {
		return nil, sessionError(err)
	}
	resp := &v1.ListArchivedResponse{}
	for _, sess := range list {
		resp.Sessions = append(resp.Sessions, sessionToProto(sess))
	}
	return connect.NewResponse(resp), nil
}

// GetHistory отдаёт снимок ленты чата архивной сессии из БД (без живого агента) — тот же
// формат AcpMessage, что и живая AcpService.GetHistory.
func (s *ArchiveService) GetHistory(ctx context.Context, req *connect.Request[v1.ArchivedHistoryRequest]) (*connect.Response[v1.ArchivedHistoryResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	msgs, err := s.registry.ArchivedHistory(ctx, req.Msg.SessionId, userID)
	if err != nil {
		return nil, sessionError(err)
	}
	resp := &v1.ArchivedHistoryResponse{}
	for _, m := range msgs {
		resp.Messages = append(resp.Messages, &v1.AcpMessage{
			Id: m.ID, Role: m.Role, Content: m.Content,
			ToolName: m.ToolName, ArgsText: m.ArgsText, Result: m.Result,
		})
	}
	return connect.NewResponse(resp), nil
}

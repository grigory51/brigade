package connectsvc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/memory"
)

// MemoryService реализует brigade.v1.MemoryService — канонический пользовательский API
// личной памяти (JWT): чтение и создание заметок из web/мобилы/любого JWT-клиента.
// Бизнес-логика (git + файлы) — в memory.Service; тот же экземпляр обслуживает
// сессионный вход AgentBridgeService.CreateMemoryNote.
type MemoryService struct {
	memory *memory.Service
}

// NewMemoryService собирает реализацию MemoryService.
func NewMemoryService(mem *memory.Service) *MemoryService {
	return &MemoryService{memory: mem}
}

// ListNotes возвращает заметки пользователя (с опциональным поиском по подстроке).
func (s *MemoryService) ListNotes(ctx context.Context, req *connect.Request[v1.ListNotesRequest]) (*connect.Response[v1.ListNotesResponse], error) {
	if _, err := requireUser(ctx); err != nil {
		return nil, err
	}
	notes, err := s.memory.List(ctx, req.Msg.Query)
	if err != nil {
		return nil, memoryError(err)
	}
	out := make([]*v1.Note, 0, len(notes))
	for _, n := range notes {
		out = append(out, noteToProto(n))
	}
	return connect.NewResponse(&v1.ListNotesResponse{Notes: out}), nil
}

// GetNote возвращает одну заметку по id.
func (s *MemoryService) GetNote(ctx context.Context, req *connect.Request[v1.GetNoteRequest]) (*connect.Response[v1.GetNoteResponse], error) {
	if _, err := requireUser(ctx); err != nil {
		return nil, err
	}
	n, err := s.memory.Get(ctx, req.Msg.Id)
	if err != nil {
		return nil, memoryError(err)
	}
	return connect.NewResponse(&v1.GetNoteResponse{Note: noteToProto(n)}), nil
}

// CreateNote создаёт заметку из UI/мобилы (session-провенанс пробрасывается как есть —
// из пользовательского API он обычно пуст).
func (s *MemoryService) CreateNote(ctx context.Context, req *connect.Request[v1.CreateNoteRequest]) (*connect.Response[v1.CreateNoteResponse], error) {
	if _, err := requireUser(ctx); err != nil {
		return nil, err
	}
	n, sha, err := s.memory.Create(ctx, memory.Note{
		Title:   req.Msg.Title,
		Body:    req.Msg.Body,
		Type:    req.Msg.Type,
		Tags:    req.Msg.Tags,
		Session: req.Msg.Session,
	})
	if err != nil {
		return nil, memoryError(err)
	}
	return connect.NewResponse(&v1.CreateNoteResponse{Note: noteToProto(n), CommitSha: sha}), nil
}

// noteToProto конвертирует доменную заметку в proto-форму.
func noteToProto(n memory.Note) *v1.Note {
	return &v1.Note{
		Id: n.ID, Title: n.Title, Body: n.Body, Type: n.Type,
		Tags: n.Tags, Session: n.Session, Created: n.Created, Updated: n.Updated,
	}
}

// memoryError транслирует доменные ошибки памяти в connect-коды.
func memoryError(err error) error {
	switch {
	case errors.Is(err, memory.ErrDisabled):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, memory.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

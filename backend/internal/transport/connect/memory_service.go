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
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	notes, err := s.memory.List(ctx, userID, req.Msg.Query)
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
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	n, err := s.memory.Get(ctx, userID, req.Msg.Id)
	if err != nil {
		return nil, memoryError(err)
	}
	return connect.NewResponse(&v1.GetNoteResponse{Note: noteToProto(n)}), nil
}

// CreateNote создаёт заметку из UI/мобилы (session-провенанс пробрасывается как есть —
// из пользовательского API он обычно пуст).
func (s *MemoryService) CreateNote(ctx context.Context, req *connect.Request[v1.CreateNoteRequest]) (*connect.Response[v1.CreateNoteResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	n, sha, err := s.memory.Create(ctx, userID, memory.Note{
		Title:   req.Msg.Title,
		Body:    req.Msg.Body,
		Type:    req.Msg.Type,
		Tags:    req.Msg.Tags,
		Session: req.Msg.Session,
		Layer:   req.Msg.Layer,
		TopicID: req.Msg.TopicId,
		Sub:     req.Msg.Sub,
		From:    req.Msg.From,
	})
	if err != nil {
		return nil, memoryError(err)
	}
	return connect.NewResponse(&v1.CreateNoteResponse{Note: noteToProto(n), CommitSha: sha}), nil
}

// ListTopics возвращает темы пользователя (с производными и опциональным поиском).
func (s *MemoryService) ListTopics(ctx context.Context, req *connect.Request[v1.ListTopicsRequest]) (*connect.Response[v1.ListTopicsResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	topics, err := s.memory.ListTopics(ctx, userID, req.Msg.Query)
	if err != nil {
		return nil, memoryError(err)
	}
	out := make([]*v1.Topic, 0, len(topics))
	for _, t := range topics {
		out = append(out, topicToProto(t))
	}
	return connect.NewResponse(&v1.ListTopicsResponse{Topics: out}), nil
}

// GetTopic возвращает тему с обзором и всеми её заметками.
func (s *MemoryService) GetTopic(ctx context.Context, req *connect.Request[v1.GetTopicRequest]) (*connect.Response[v1.GetTopicResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	topic, notes, err := s.memory.GetTopic(ctx, userID, req.Msg.Id)
	if err != nil {
		return nil, memoryError(err)
	}
	out := make([]*v1.Note, 0, len(notes))
	for _, n := range notes {
		out = append(out, noteToProto(n))
	}
	return connect.NewResponse(&v1.GetTopicResponse{Topic: topicToProto(topic), Notes: out}), nil
}

// CreateTopic создаёт новую тему.
func (s *MemoryService) CreateTopic(ctx context.Context, req *connect.Request[v1.CreateTopicRequest]) (*connect.Response[v1.CreateTopicResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.memory.CreateTopic(ctx, userID, req.Msg.Name, req.Msg.Color)
	if err != nil {
		return nil, memoryError(err)
	}
	return connect.NewResponse(&v1.CreateTopicResponse{Topic: topicToProto(t)}), nil
}

// UpdateTopicOverview перезаписывает synthesis-обзор темы.
func (s *MemoryService) UpdateTopicOverview(ctx context.Context, req *connect.Request[v1.UpdateTopicOverviewRequest]) (*connect.Response[v1.UpdateTopicOverviewResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.memory.UpdateTopicOverview(ctx, userID, req.Msg.Id, req.Msg.Synthesis)
	if err != nil {
		return nil, memoryError(err)
	}
	return connect.NewResponse(&v1.UpdateTopicOverviewResponse{Topic: topicToProto(t)}), nil
}

// UpdateTopic переименовывает тему / меняет цвет.
func (s *MemoryService) UpdateTopic(ctx context.Context, req *connect.Request[v1.UpdateTopicRequest]) (*connect.Response[v1.UpdateTopicResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.memory.UpdateTopic(ctx, userID, req.Msg.Id, req.Msg.Name, req.Msg.Color)
	if err != nil {
		return nil, memoryError(err)
	}
	return connect.NewResponse(&v1.UpdateTopicResponse{Topic: topicToProto(t)}), nil
}

// DeleteTopic удаляет тему целиком.
func (s *MemoryService) DeleteTopic(ctx context.Context, req *connect.Request[v1.DeleteTopicRequest]) (*connect.Response[v1.DeleteTopicResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	sha, err := s.memory.DeleteTopic(ctx, userID, req.Msg.Id)
	if err != nil {
		return nil, memoryError(err)
	}
	return connect.NewResponse(&v1.DeleteTopicResponse{CommitSha: sha}), nil
}

// UpdateNote правит поля заметки на месте.
func (s *MemoryService) UpdateNote(ctx context.Context, req *connect.Request[v1.UpdateNoteRequest]) (*connect.Response[v1.UpdateNoteResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	n, sha, err := s.memory.UpdateNote(ctx, userID, req.Msg.Id, req.Msg.Title, req.Msg.Body, req.Msg.Type, req.Msg.Sub)
	if err != nil {
		return nil, memoryError(err)
	}
	return connect.NewResponse(&v1.UpdateNoteResponse{Note: noteToProto(n), CommitSha: sha}), nil
}

// MoveNote переносит заметку в другую тему/подтему.
func (s *MemoryService) MoveNote(ctx context.Context, req *connect.Request[v1.MoveNoteRequest]) (*connect.Response[v1.MoveNoteResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	n, sha, err := s.memory.MoveNote(ctx, userID, req.Msg.Id, req.Msg.ToTopicId, req.Msg.ToSub)
	if err != nil {
		return nil, memoryError(err)
	}
	return connect.NewResponse(&v1.MoveNoteResponse{Note: noteToProto(n), CommitSha: sha}), nil
}

// DeleteNote удаляет заметку.
func (s *MemoryService) DeleteNote(ctx context.Context, req *connect.Request[v1.DeleteNoteRequest]) (*connect.Response[v1.DeleteNoteResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	sha, err := s.memory.DeleteNote(ctx, userID, req.Msg.Id)
	if err != nil {
		return nil, memoryError(err)
	}
	return connect.NewResponse(&v1.DeleteNoteResponse{CommitSha: sha}), nil
}

// SyncMemory подтягивает свежие изменения памяти с origin (git pull --rebase) — чтобы видеть
// правки из другого инстанса brigade. Клиент после успеха перечитывает темы/заметки.
func (s *MemoryService) SyncMemory(ctx context.Context, _ *connect.Request[v1.SyncMemoryRequest]) (*connect.Response[v1.SyncMemoryResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.memory.Sync(ctx, userID); err != nil {
		return nil, memoryError(err)
	}
	return connect.NewResponse(&v1.SyncMemoryResponse{}), nil
}

// noteToProto конвертирует доменную заметку в proto-форму.
func noteToProto(n memory.Note) *v1.Note {
	return &v1.Note{
		Id: n.ID, Title: n.Title, Body: n.Body, Type: n.Type,
		Tags: n.Tags, Session: n.Session, Created: n.Created, Updated: n.Updated,
		Layer: n.Layer, TopicId: n.TopicID, Sub: n.Sub, From: n.From,
	}
}

// topicToProto конвертирует доменную тему в proto-форму (включая производные и recent).
func topicToProto(t memory.Topic) *v1.Topic {
	recent := make([]*v1.Note, 0, len(t.Recent))
	for _, n := range t.Recent {
		recent = append(recent, noteToProto(n))
	}
	return &v1.Topic{
		Id: t.ID, Name: t.Name, Color: t.Color, Initial: t.Initial,
		Synthesis: t.Synthesis, Subs: t.Subs,
		NoteCount: int32(t.NoteCount), Updated: t.Updated, ChatCount: int32(t.ChatCount),
		Recent: recent,
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

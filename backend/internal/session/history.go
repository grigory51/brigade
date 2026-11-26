package session

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"
	"unicode"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/grigory51/brigade/backend/internal/acp"
	"github.com/grigory51/brigade/backend/internal/agui"
	"github.com/grigory51/brigade/backend/internal/store"
)

// HistorySnapshot — единая форма ленты независимо от того, кто хранит сообщения.
type HistorySnapshot struct {
	Messages      []acp.Message
	Revision      int64
	Commands      []agui.AvailableCommand
	ConfigOptions []acpsdk.SessionConfigOption
}

type historySource interface {
	read(context.Context, store.Session) (HistorySnapshot, error)
	status(context.Context, store.Session) (bool, int64, error)
}

type agentHistory struct{ registry *Registry }

// saveACPPreview использует уже работающий клиент: список и восстановление metadata
// не должны запускать агента ради чтения истории. Ошибка чтения сохраняет прежнее preview.
func (r *Registry) saveACPPreview(ctx context.Context, sess store.Session, client acpSession, at time.Time) {
	if sess.Kind != store.SessionKindACP || sess.ExecutionPolicy == store.SessionExecutionOnDemand {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	messages, err := client.Messages(ctx)
	if err != nil {
		log.Printf("session %s: read preview: %v", sess.ID, err)
		return
	}
	r.saveACPMessagePreview(ctx, sess, messages, at)
}

func (r *Registry) restoreACPPreview(sess store.Session, client acpSession) {
	if observer, ok := client.(interface {
		ObserveRecoveredTurn(context.Context, func(context.Context, time.Time)) error
	}); ok {
		if err := observer.ObserveRecoveredTurn(context.Background(), func(ctx context.Context, at time.Time) {
			r.saveACPPreview(ctx, sess, client, at)
		}); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("session %s: restore preview: %v", sess.ID, err)
		}
		return
	}
	r.saveACPPreview(context.Background(), sess, client, time.Time{})
}

// saveACPMessagePreview принимает готовую проекцию. Нулевое at сохраняет неизвестное
// время при backfill и reattach; для завершения turn передаётся время его события.
func (r *Registry) saveACPMessagePreview(ctx context.Context, sess store.Session, messages []acp.Message, at time.Time) {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if (message.Role != "user" && message.Role != "assistant") || strings.TrimFunc(message.Content, func(char rune) bool {
			return unicode.IsSpace(char) || unicode.IsControl(char)
		}) == "" {
			continue
		}
		author := store.MessageAuthorOwner
		if message.Role == "assistant" {
			author = store.MessageAuthorAgent
		}
		if message.CreatedAt > 0 {
			at = time.Unix(message.CreatedAt, 0)
		}
		if err := r.store.UpdateSessionPreview(ctx, sess, message.Content, at, author); err != nil {
			log.Printf("session %s: save preview: %v", sess.ID, err)
		}
		return
	}
}

func (h agentHistory) read(ctx context.Context, sess store.Session) (HistorySnapshot, error) {
	client, ok := h.registry.EnsureACPClient(ctx, sess.ID, sess.UserID)
	if !ok {
		return HistorySnapshot{}, store.ErrNotFound
	}
	_, seq := client.Status()
	messages, err := client.Messages(ctx)
	if err != nil {
		return HistorySnapshot{}, err
	}
	if sess.LastMessage == "" {
		h.registry.saveACPMessagePreview(ctx, sess, messages, time.Time{})
	}
	return HistorySnapshot{
		Messages: messages, Revision: int64(seq),
		Commands: client.Commands(), ConfigOptions: client.ConfigOptions(),
	}, nil
}

func (h agentHistory) status(_ context.Context, sess store.Session) (bool, int64, error) {
	client, ok := h.registry.ACPClient(sess.ID, sess.UserID)
	if !ok {
		return false, 0, nil
	}
	active, seq := client.Status()
	return active, int64(seq), nil
}

type storedHistory struct{ store *store.Store }

func (h storedHistory) read(ctx context.Context, sess store.Session) (HistorySnapshot, error) {
	items, err := h.store.SessionMessages(ctx, sess.ID)
	if err != nil {
		return HistorySnapshot{}, err
	}
	messages := make([]acp.Message, 0, len(items))
	for _, item := range items {
		role := "user"
		if item.Author == store.MessageAuthorAgent {
			role = "assistant"
		}
		messages = append(messages, acp.Message{
			ID: item.ID, Role: role, Content: item.Content,
			Author: string(item.Author), Source: item.Source, ExternalID: item.ExternalID,
			IncludedInContext: item.IncludedInContext, Delivery: string(item.Delivery),
			ReplyToID: item.ReplyToID, CreatedAt: item.CreatedAt.Unix(),
			HasTelegramBusinessPhoto: item.HasTelegramBusinessPhoto,
		})
	}
	return HistorySnapshot{Messages: messages, Revision: sess.HistoryRevision}, nil
}

func (h storedHistory) status(ctx context.Context, sess store.Session) (bool, int64, error) {
	_, err := h.store.ActiveSessionRun(ctx, sess.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, 0, err
	}
	return err == nil, sess.HistoryRevision, nil
}

func (r *Registry) historySource(sess store.Session) historySource {
	if sess.ExecutionPolicy == store.SessionExecutionOnDemand {
		return storedHistory{store: r.store}
	}
	return agentHistory{registry: r}
}

// History читает ленту из единственного владельца: агента либо базы Brigade.
func (r *Registry) History(ctx context.Context, sessionID, userID string) (HistorySnapshot, error) {
	sess, err := r.Get(ctx, sessionID, userID)
	if err != nil {
		return HistorySnapshot{}, err
	}
	if sess.Kind != store.SessionKindACP {
		return HistorySnapshot{}, errors.New("session: history is available only for ACP sessions")
	}
	return r.historySource(sess).read(ctx, sess)
}

// HistoryStatus не будит агента; для shadow отдаёт ревизию из базы.
func (r *Registry) HistoryStatus(ctx context.Context, sessionID, userID string) (bool, int64, error) {
	sess, err := r.Get(ctx, sessionID, userID)
	if err != nil {
		return false, 0, err
	}
	if sess.Kind != store.SessionKindACP {
		return false, 0, errors.New("session: status is available only for ACP sessions")
	}
	return r.historySource(sess).status(ctx, sess)
}

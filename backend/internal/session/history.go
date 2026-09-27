package session

import (
	"context"
	"errors"

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

func (h agentHistory) read(ctx context.Context, sess store.Session) (HistorySnapshot, error) {
	client, ok := h.registry.EnsureACPClient(ctx, sess.ID, sess.UserID)
	if !ok {
		return HistorySnapshot{}, store.ErrNotFound
	}
	_, seq := client.Status()
	return HistorySnapshot{
		Messages: client.Messages(), Revision: int64(seq),
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

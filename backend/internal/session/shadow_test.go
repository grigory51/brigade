package session

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/grigory51/brigade/backend/internal/store"
)

func TestShadowRunInputSelectionDoesNotChangeReplyTarget(t *testing.T) {
	messages := []store.SessionMessage{
		{ID: "old", Author: store.MessageAuthorContact, Source: "telegram-business/bot/conn/1", IncludedInContext: true},
		{ID: "note", Author: store.MessageAuthorOwner, IncludedInContext: false},
		{ID: "latest", Author: store.MessageAuthorContact, Source: "telegram-business/bot/conn/1", IncludedInContext: false},
		{ID: "draft", Author: store.MessageAuthorAgent, Delivery: store.MessageDeliveryDraft},
	}
	selected, replyTo, err := shadowRunInput(messages, []string{"note", "old", "latest"})
	if err != nil || len(selected) != 3 || selected[0].ID != "old" || selected[1].ID != "note" || selected[2].ID != "latest" || replyTo != "latest" {
		t.Fatalf("selection=%+v replyTo=%q err=%v", selected, replyTo, err)
	}
	if _, _, err := shadowRunInput(messages, []string{"old", "note"}); err == nil {
		t.Fatal("latest incoming message must be included in the draft context")
	}
	if _, _, err := shadowRunInput(messages, []string{"draft"}); err == nil {
		t.Fatal("draft cannot be selected as context")
	}
	if _, _, err := shadowRunInput(messages, []string{"missing"}); err == nil {
		t.Fatal("unknown message cannot be selected as context")
	}
}

func TestShadowRunInputRejectsDeletedBusinessMessage(t *testing.T) {
	messages := []store.SessionMessage{
		{ID: "old", Author: store.MessageAuthorContact, Source: "telegram-business/bot/conn/1", Delivery: store.MessageDeliveryReceived, IncludedInContext: true},
		{ID: "deleted", Author: store.MessageAuthorContact, Source: "telegram-business/bot/conn/1", Delivery: store.MessageDeliveryDeleted},
	}
	if _, _, err := shadowRunInput(messages, nil); err == nil {
		t.Fatal("a deleted latest incoming message must not produce a reply")
	}
	if _, _, err := shadowRunInput(messages, []string{"deleted"}); err == nil {
		t.Fatal("a deleted message must not be selectable as context")
	}
	messages = append(messages, store.SessionMessage{ID: "new", Author: store.MessageAuthorContact, Source: "telegram-business/bot/conn/1", Delivery: store.MessageDeliveryReceived, IncludedInContext: true})
	selected, replyTo, err := shadowRunInput(messages, nil)
	if err != nil || len(selected) != 2 || replyTo != "new" {
		t.Fatalf("selection=%+v replyTo=%q err=%v", selected, replyTo, err)
	}
}

func TestCreateShadowDraftRequiresOwnedBusinessReplyTarget(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "brigade.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	if _, err := st.DB().Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES ('owner', 'owner', '', 0)`); err != nil {
		t.Fatal(err)
	}
	sess := store.Session{ID: "shadow", UserID: "owner", Kind: store.SessionKindACP, Mode: store.SessionModeDocker,
		Status: store.SessionStatusIdle, ExecutionPolicy: store.SessionExecutionOnDemand, AgentType: "codex", CreatedAt: time.Now()}
	if err := st.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	for _, message := range []store.SessionMessage{
		{ID: "incoming", SessionID: sess.ID, Author: store.MessageAuthorContact, Content: "Привет", Source: "telegram-business/bot/conn/1", ExternalID: "7", CreatedAt: time.Now()},
		{ID: "local", SessionID: sess.ID, Author: store.MessageAuthorOwner, Content: "Заметка", Source: "brigade", CreatedAt: time.Now()},
	} {
		if _, err := st.AddSessionMessage(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	r := &Registry{store: st}
	for _, target := range []string{"local", "missing"} {
		if _, err := r.CreateShadowDraft(ctx, sess.ID, "owner", "Ответ", target); err == nil {
			t.Fatalf("target %q must be rejected", target)
		}
	}
	if _, err := r.CreateShadowDraft(ctx, sess.ID, "other", "Ответ", "incoming"); err == nil {
		t.Fatal("other user must not create a draft")
	}
	draft, err := r.CreateShadowDraft(ctx, sess.ID, "owner", "  Ответ  ", "incoming")
	if err != nil || draft.Content != "Ответ" || draft.Author != store.MessageAuthorOwner || draft.Delivery != store.MessageDeliveryDraft || draft.ReplyToID != "incoming" || draft.IncludedInContext {
		t.Fatalf("draft=%+v err=%v", draft, err)
	}
}

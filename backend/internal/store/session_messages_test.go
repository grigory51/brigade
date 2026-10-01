package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestShadowMessageRevisionAndRunExclusivity(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "brigade.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	if _, err := st.DB().Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES ('owner', 'owner', '', 0)`); err != nil {
		t.Fatal(err)
	}
	sess := Session{ID: "s1", UserID: "owner", Kind: SessionKindACP, Mode: SessionModeDocker, Status: SessionStatusIdle,
		ExecutionPolicy: SessionExecutionOnDemand, AgentType: "codex", CreatedAt: time.Now()}
	if err := st.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateShadowModel(ctx, sess.ID, "model-from-acp"); err != nil {
		t.Fatal(err)
	}
	configured, err := st.GetSession(ctx, sess.ID)
	if err != nil || configured.ModelID != "model-from-acp" {
		t.Fatalf("shadow model: %+v, %v", configured, err)
	}
	message := SessionMessage{ID: "m1", SessionID: sess.ID, Author: MessageAuthorContact, Content: "Привет", Source: "telegram-business/bot/conn/1", ExternalID: "7", IncludedInContext: true, Delivery: MessageDeliveryReceived, CreatedAt: time.Now()}
	if inserted, err := st.AddSessionMessage(ctx, message); err != nil || !inserted {
		t.Fatalf("insert: %v %v", inserted, err)
	}
	message.ID = "m2"
	if inserted, err := st.AddSessionMessage(ctx, message); err != nil || inserted {
		t.Fatalf("duplicate: %v %v", inserted, err)
	}
	got, err := st.GetSession(ctx, sess.ID)
	if err != nil || got.HistoryRevision != 1 {
		t.Fatalf("revision: %+v, %v", got, err)
	}
	if err := st.SetMessageIncluded(ctx, sess.ID, "m1", false); err != nil {
		t.Fatal(err)
	}
	previews, err := st.LastShadowMessages(ctx, "owner")
	if err != nil || previews[sess.ID].Message.Content != "Привет" || previews[sess.ID].Message.Author != MessageAuthorContact || previews[sess.ID].UnreadCount != 1 {
		t.Fatalf("shadow preview: %+v, %v", previews, err)
	}
	if err := st.MarkSessionRead(ctx, sess.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	previews, err = st.LastShadowMessages(ctx, "owner")
	if err != nil || previews[sess.ID].UnreadCount != 0 {
		t.Fatalf("read shadow preview: %+v, %v", previews, err)
	}
	got, _ = st.GetSession(ctx, sess.ID)
	if got.HistoryRevision != 2 {
		t.Fatalf("revision after inclusion: %d", got.HistoryRevision)
	}
	run := SessionRun{ID: "r1", SessionID: sess.ID, InputRevision: got.HistoryRevision, CreatedAt: time.Now()}
	if err := st.StartSessionRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	run.ID = "r2"
	if err := st.StartSessionRun(ctx, run); err != ErrSessionRunActive {
		t.Fatalf("concurrent run: %v", err)
	}
	if err := st.FinishSessionRun(ctx, "r1", "interrupted", "", "restart"); err != nil {
		t.Fatal(err)
	}
	if err := st.StartSessionRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	draft := SessionMessage{ID: "draft", SessionID: sess.ID, Author: MessageAuthorAgent, Content: "Ответ", Source: "agent", Delivery: MessageDeliveryDraft, CreatedAt: time.Now()}
	if err := st.AddSessionRunDraft(ctx, run, draft); err != nil {
		t.Fatal(err)
	}
	if persisted, err := st.SessionMessage(ctx, sess.ID, draft.ID); err != nil || persisted.Content != draft.Content {
		t.Fatalf("run draft: %+v, %v", persisted, err)
	}
	if err := st.EditSessionDraft(ctx, sess.ID, draft.ID, "Отредактированный ответ"); err != nil {
		t.Fatal(err)
	}
	if edited, err := st.SessionMessage(ctx, sess.ID, draft.ID); err != nil || edited.Source != "brigade" || edited.Author != MessageAuthorOwner || edited.Delivery != MessageDeliveryDraft {
		t.Fatalf("edited draft: %+v, %v", edited, err)
	}
}

func TestMarkSessionDraftSentAfterBusinessEcho(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "brigade.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	if _, err := st.DB().Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES ('owner', 'owner', '', 0)`); err != nil {
		t.Fatal(err)
	}
	sess := Session{ID: "s1", UserID: "owner", Kind: SessionKindACP, Mode: SessionModeDocker, Status: SessionStatusIdle,
		ExecutionPolicy: SessionExecutionOnDemand, AgentType: "codex", CreatedAt: time.Now()}
	if err := st.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	source := "telegram-business/bot/conn/1"
	for _, message := range []SessionMessage{
		{ID: "draft", SessionID: sess.ID, Author: MessageAuthorAgent, Content: "Ответ", Source: "agent", Delivery: MessageDeliveryDraft, CreatedAt: time.Now()},
		{ID: "echo", SessionID: sess.ID, Author: MessageAuthorOwner, Content: "Ответ", Source: source, ExternalID: "90", Delivery: MessageDeliveryReceived, CreatedAt: time.Now()},
	} {
		if _, err := st.AddSessionMessage(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetSessionDraftDelivery(ctx, sess.ID, "draft", MessageDeliveryDraft, MessageDeliverySending); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSessionDraftSent(ctx, sess.ID, "draft", source, "90"); err != nil {
		t.Fatal(err)
	}
	messages, err := st.SessionMessages(ctx, sess.ID)
	if err != nil || len(messages) != 1 || messages[0].ID != "draft" || messages[0].Delivery != MessageDeliverySent {
		t.Fatalf("messages: %+v, %v", messages, err)
	}
}

func TestEditAndDeleteLocalSessionMessage(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "brigade.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	if _, err := st.DB().Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES ('owner', 'owner', '', 0)`); err != nil {
		t.Fatal(err)
	}
	sess := Session{ID: "s1", UserID: "owner", Kind: SessionKindACP, Mode: SessionModeDocker, Status: SessionStatusIdle,
		ExecutionPolicy: SessionExecutionOnDemand, AgentType: "codex", CreatedAt: time.Now()}
	if err := st.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	for _, message := range []SessionMessage{
		{ID: "note", SessionID: sess.ID, Author: MessageAuthorOwner, Content: "Старая заметка", Source: "brigade", Delivery: MessageDeliveryReceived, CreatedAt: time.Now()},
		{ID: "contact", SessionID: sess.ID, Author: MessageAuthorContact, Content: "Привет", Source: "telegram-business/bot/conn/1", Delivery: MessageDeliveryReceived, CreatedAt: time.Now()},
		{ID: "draft", SessionID: sess.ID, Author: MessageAuthorOwner, Content: "Ответ", Source: "brigade", Delivery: MessageDeliveryDraft, CreatedAt: time.Now()},
	} {
		if _, err := st.AddSessionMessage(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.EditLocalSessionMessage(ctx, sess.ID, "contact", "Нельзя"); err != ErrNotFound {
		t.Fatalf("edit contact: %v", err)
	}
	if err := st.DeleteLocalSessionMessage(ctx, sess.ID, "draft"); err != ErrNotFound {
		t.Fatalf("delete draft: %v", err)
	}
	if err := st.EditLocalSessionMessage(ctx, sess.ID, "note", "Новая заметка"); err != nil {
		t.Fatal(err)
	}
	note, err := st.SessionMessage(ctx, sess.ID, "note")
	if err != nil || note.Content != "Новая заметка" {
		t.Fatalf("edited note: %+v, %v", note, err)
	}
	if err := st.DeleteLocalSessionMessage(ctx, sess.ID, "note"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SessionMessage(ctx, sess.ID, "note"); err != ErrNotFound {
		t.Fatalf("deleted note: %v", err)
	}
	got, err := st.GetSession(ctx, sess.ID)
	if err != nil || got.HistoryRevision != 5 {
		t.Fatalf("revision: %+v, %v", got, err)
	}
}

func TestDeletedBusinessMessageInvalidatesRunningDraft(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "brigade.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	if _, err := st.DB().Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES ('owner', 'owner', '', 0)`); err != nil {
		t.Fatal(err)
	}
	sess := Session{ID: "s1", UserID: "owner", Kind: SessionKindACP, Mode: SessionModeDocker, Status: SessionStatusIdle,
		ExecutionPolicy: SessionExecutionOnDemand, AgentType: "codex", CreatedAt: time.Now()}
	if err := st.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	source := "telegram-business/bot/conn/1"
	message := SessionMessage{ID: "incoming", SessionID: sess.ID, Author: MessageAuthorContact, Content: "Спокойной ночи", Source: source, ExternalID: "7", IncludedInContext: true, Delivery: MessageDeliveryReceived, CreatedAt: time.Now()}
	if _, err := st.AddSessionMessage(ctx, message); err != nil {
		t.Fatal(err)
	}
	run := SessionRun{ID: "run", SessionID: sess.ID, InputRevision: 1, CreatedAt: time.Now()}
	if err := st.StartSessionRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteExternalSessionMessage(ctx, sess.ID, source, "7"); err != nil {
		t.Fatal(err)
	}
	deleted, err := st.SessionMessage(ctx, sess.ID, message.ID)
	if err != nil || deleted.Delivery != MessageDeliveryDeleted || deleted.IncludedInContext || deleted.Content == message.Content {
		t.Fatalf("deleted message: %+v, %v", deleted, err)
	}
	if err := st.UpdateExternalSessionMessage(ctx, sess.ID, source, "7", "edited text"); err != nil {
		t.Fatal(err)
	}
	deleted, _ = st.SessionMessage(ctx, sess.ID, message.ID)
	if deleted.Content == "edited text" {
		t.Fatal("an edit must not revive a deleted message")
	}
	draft := SessionMessage{ID: "draft", SessionID: sess.ID, Author: MessageAuthorAgent, Content: "Сладких снов", Source: "agent", Delivery: MessageDeliveryDraft, CreatedAt: time.Now()}
	if err := st.AddSessionRunDraft(ctx, run, draft); err != ErrSessionHistoryChanged {
		t.Fatalf("stale draft: %v", err)
	}
	if _, err := st.SessionMessage(ctx, sess.ID, draft.ID); err != ErrNotFound {
		t.Fatalf("stale draft was persisted: %v", err)
	}
}

func TestAgentDraftBecomesStaleAfterContextChanges(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "brigade.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	if _, err := st.DB().Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES ('owner', 'owner', '', 0)`); err != nil {
		t.Fatal(err)
	}
	sess := Session{ID: "s1", UserID: "owner", Kind: SessionKindACP, Mode: SessionModeDocker, Status: SessionStatusIdle,
		ExecutionPolicy: SessionExecutionOnDemand, AgentType: "codex", CreatedAt: time.Now()}
	if err := st.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	incoming := SessionMessage{ID: "incoming", SessionID: sess.ID, Author: MessageAuthorContact, Content: "Привет", Source: "telegram-business/bot/conn/1", ExternalID: "7", IncludedInContext: true, Delivery: MessageDeliveryReceived, CreatedAt: time.Now()}
	if _, err := st.AddSessionMessage(ctx, incoming); err != nil {
		t.Fatal(err)
	}
	run := SessionRun{ID: "run", SessionID: sess.ID, InputRevision: 1, CreatedAt: time.Now()}
	if err := st.StartSessionRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	draft := SessionMessage{ID: "draft", SessionID: sess.ID, Author: MessageAuthorAgent, Content: "Спокойной ночи", Source: "agent", Delivery: MessageDeliveryDraft, CreatedAt: time.Now()}
	if err := st.AddSessionRunDraft(ctx, run, draft); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishSessionRun(ctx, run.ID, "completed", draft.ID, ""); err != nil {
		t.Fatal(err)
	}
	if current, err := st.SessionMessage(ctx, sess.ID, draft.ID); err != nil || current.Delivery != MessageDeliveryDraft {
		t.Fatalf("fresh draft: %+v, %v", current, err)
	}
	if err := st.DeleteExternalSessionMessage(ctx, sess.ID, incoming.Source, incoming.ExternalID); err != nil {
		t.Fatal(err)
	}
	stale, err := st.SessionMessage(ctx, sess.ID, draft.ID)
	if err != nil || stale.Delivery != MessageDeliveryStale {
		t.Fatalf("stale draft: %+v, %v", stale, err)
	}
	messages, err := st.SessionMessages(ctx, sess.ID)
	if err != nil || len(messages) != 2 || messages[1].Delivery != MessageDeliveryStale {
		t.Fatalf("history: %+v, %v", messages, err)
	}
	if err := st.EditSessionDraft(ctx, sess.ID, draft.ID, "new text"); err != ErrNotFound {
		t.Fatalf("stale draft should not be editable: %v", err)
	}
	previews, err := st.LastShadowMessages(ctx, "owner")
	if err != nil || previews[sess.ID].Message.Content != "[Сообщение удалено в Telegram]" {
		t.Fatalf("stale draft should not be previewed: %+v, %v", previews, err)
	}
}

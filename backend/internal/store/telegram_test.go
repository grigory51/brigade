package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTelegramStore(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	bot := TelegramBot{
		ID: "bot-1", UserID: "u1", Token: "secret", TelegramID: 42,
		Username: "brigade_bot", Name: "Brigade", AgentType: "codex",
		AuthProfile: "chatgpt", McpServers: []string{"mcp-1", "mcp-2"}, CreatedAt: time.Now(),
	}
	if err := st.SaveTelegramBot(ctx, bot); err != nil {
		t.Fatalf("SaveTelegramBot: %v", err)
	}
	got, err := st.GetTelegramBot(ctx, bot.ID)
	if err != nil || got.Token != bot.Token || len(got.McpServers) != 2 {
		t.Fatalf("GetTelegramBot: %+v, %v", got, err)
	}

	inserted, err := st.InsertTelegramUpdate(ctx, bot.ID, 7, `{"update_id":7}`)
	if err != nil || !inserted {
		t.Fatalf("InsertTelegramUpdate: inserted=%v err=%v", inserted, err)
	}
	inserted, err = st.InsertTelegramUpdate(ctx, bot.ID, 7, `{"update_id":7}`)
	if err != nil || inserted {
		t.Fatalf("duplicate update: inserted=%v err=%v", inserted, err)
	}
	if err := st.SetTelegramUpdatePayload(ctx, bot.ID, 7, `{"update_id":7,"brigade_guest_inline_message_id":"inline"}`); err != nil {
		t.Fatalf("SetTelegramUpdatePayload: %v", err)
	}
	if err := st.SetTelegramUpdateState(ctx, bot.ID, 7, "ready", "answer", ""); err != nil {
		t.Fatalf("SetTelegramUpdateState: %v", err)
	}
	updates, err := st.ListTelegramUpdates(ctx, bot.ID, "ready")
	if err != nil || len(updates) != 1 || updates[0].Response != "answer" || !strings.Contains(updates[0].Payload, `"brigade_guest_inline_message_id":"inline"`) {
		t.Fatalf("ListTelegramUpdates: %+v, %v", updates, err)
	}
	if err := st.DeleteTelegramUpdate(ctx, bot.ID, 7); err != nil {
		t.Fatalf("DeleteTelegramUpdate: %v", err)
	}

	conversation := TelegramConversation{BotID: bot.ID, Scope: "chat", ChatID: -100, ThreadID: 9, SessionID: "session-1"}
	if err := st.SetTelegramConversation(ctx, conversation); err != nil {
		t.Fatalf("SetTelegramConversation: %v", err)
	}
	gotConversation, err := st.TelegramConversation(ctx, bot.ID, "chat", -100, 9)
	if err != nil || gotConversation.SessionID != conversation.SessionID {
		t.Fatalf("TelegramConversation: %+v, %v", gotConversation, err)
	}
	if err := st.DeleteTelegramBot(ctx, "u1", bot.ID); err != nil {
		t.Fatalf("DeleteTelegramBot: %v", err)
	}
	if _, err := st.TelegramConversation(ctx, bot.ID, "chat", -100, 9); !errors.Is(err, ErrNotFound) {
		t.Fatalf("conversation must cascade with bot: %v", err)
	}
}

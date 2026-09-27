package telegram

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/grigory51/brigade/backend/internal/store"
)

func TestSecretaryStoresMessagesWithoutStartingAgent(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "brigade.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	if _, err := st.DB().Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES ('owner', 'owner', '', 0)`); err != nil {
		t.Fatal(err)
	}
	bot := store.TelegramBot{ID: "bot", UserID: "owner", Token: "secret", Username: "secretary", TelegramID: 100,
		OwnerTelegramID: 42, AgentType: "codex", AuthProfile: "api-key", Purpose: "secretary",
		BusinessConnectionID: "connection", BusinessOwnerID: 42, BusinessEnabled: true, BusinessCanReply: true}
	if err := st.SaveTelegramBot(ctx, bot); err != nil {
		t.Fatal(err)
	}
	service := New(st, nil, nil, "webhook", "", nil)
	t.Cleanup(service.Close)
	registry := &telegramTestRegistry{sessions: make(map[string]store.Session), shadowStore: st}
	service.registry = registry
	var sentBody map[string]any
	service.api.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(req.Body).Decode(&sentBody); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(req.URL.Path, "/sendMessage") {
			t.Fatalf("unexpected method: %s", req.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":90}}`))}, nil
	})
	queue := func(update telegramUpdate) {
		t.Helper()
		payload, err := json.Marshal(update)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.InsertTelegramUpdate(ctx, bot.ID, update.UpdateID, string(payload)); err != nil {
			t.Fatal(err)
		}
		updates, err := st.ListTelegramUpdates(ctx, bot.ID, "queued")
		if err != nil || len(updates) != 1 {
			t.Fatalf("queued: %v, %v", updates, err)
		}
		if err := service.processSecretaryQueued(bot, updates[0]); err != nil {
			t.Fatal(err)
		}
	}
	message := &telegramMessage{MessageID: 7, Date: time.Now().Unix(), BusinessConnectionID: "connection", Chat: telegramChat{ID: 123, Type: "private", Username: "contact"}, From: &telegramUser{ID: 55}, Text: "Привет"}
	queue(telegramUpdate{UpdateID: 1, BusinessMessage: message})
	queue(telegramUpdate{UpdateID: 2, BusinessMessage: message})
	if len(registry.created) != 1 || len(registry.prompts) != 0 {
		t.Fatalf("agent was started: created=%v prompts=%v", registry.created, registry.prompts)
	}
	sessionID := registry.created[0]
	messages, err := st.SessionMessages(ctx, sessionID)
	if err != nil || len(messages) != 1 || messages[0].Author != store.MessageAuthorContact {
		t.Fatalf("stored messages: %v, %v", messages, err)
	}
	if got, err := st.GetSession(ctx, sessionID); err != nil || got.HistoryRevision != 1 {
		t.Fatalf("revision: %+v, %v", got, err)
	}
	draft := store.SessionMessage{ID: uuid.NewString(), SessionID: sessionID, Author: store.MessageAuthorAgent, Content: "Здравствуйте!", Source: "agent", Delivery: store.MessageDeliveryDraft, ReplyToID: messages[0].ID, CreatedAt: time.Now()}
	if _, err := st.AddSessionMessage(ctx, draft); err != nil {
		t.Fatal(err)
	}
	if err := service.SendDraft(ctx, "owner", sessionID, draft.ID); err != nil {
		t.Fatal(err)
	}
	if sentBody["business_connection_id"] != "connection" || sentBody["chat_id"] != float64(123) {
		t.Fatalf("wrong target: %v", sentBody)
	}
	gotDraft, err := st.SessionMessage(ctx, sessionID, draft.ID)
	if err != nil || gotDraft.Delivery != store.MessageDeliverySent || gotDraft.ExternalID != "90" {
		t.Fatalf("draft: %+v, %v", gotDraft, err)
	}
}

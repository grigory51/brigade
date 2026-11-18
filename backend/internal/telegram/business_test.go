package telegram

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/grigory51/brigade/backend/internal/store"
)

func TestOneBotHandlesBusinessAndAssistantMessages(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "brigade.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	if _, err := st.DB().Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES ('owner', 'owner', '', 0)`); err != nil {
		t.Fatal(err)
	}
	bot := store.TelegramBot{ID: "bot", UserID: "owner", Token: "secret", Username: "helper", TelegramID: 100,
		OwnerTelegramID: 42, AgentType: "codex", AuthProfile: "api-key",
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
		response := `{"ok":true,"result":true}`
		if strings.HasSuffix(req.URL.Path, "/sendMessage") {
			if err := json.NewDecoder(req.Body).Decode(&sentBody); err != nil {
				t.Fatal(err)
			}
			response = `{"ok":true,"result":{"message_id":90}}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
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
		service.process(bot.ID)
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
	draft := store.SessionMessage{ID: uuid.NewString(), SessionID: sessionID, Author: store.MessageAuthorAgent, Content: "Здравствуйте!", Source: "agent", Delivery: store.MessageDeliveryDraft, CreatedAt: time.Now()}
	if _, err := st.AddSessionMessage(ctx, draft); err != nil {
		t.Fatal(err)
	}
	if err := service.SendDraft(ctx, "owner", sessionID, draft.ID, draft.ID); err == nil {
		t.Fatal("draft cannot be used as a reply target")
	}
	if err := service.SendDraft(ctx, "owner", sessionID, draft.ID, messages[0].ID); err != nil {
		t.Fatal(err)
	}
	if sentBody["business_connection_id"] != "connection" || sentBody["chat_id"] != float64(123) {
		t.Fatalf("wrong target: %v", sentBody)
	}
	gotDraft, err := st.SessionMessage(ctx, sessionID, draft.ID)
	if err != nil || gotDraft.Delivery != store.MessageDeliverySent || gotDraft.ExternalID != "90" {
		t.Fatalf("draft: %+v, %v", gotDraft, err)
	}
	queue(telegramUpdate{UpdateID: 3, Message: &telegramMessage{
		MessageID: 8, Date: time.Now().Unix(), Chat: telegramChat{ID: 42, Type: "private"},
		From: &telegramUser{ID: 42}, Text: "Помоги с задачей",
	}})
	if len(registry.created) != 2 || len(registry.prompts) != 1 {
		t.Fatalf("normal chat on the same bot: created=%v prompts=%v", registry.created, registry.prompts)
	}
}

func TestBusinessPhotoAccessAndLifecycle(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "brigade.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	if _, err := st.DB().Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES ('owner', 'owner', '', 0)`); err != nil {
		t.Fatal(err)
	}
	bot := store.TelegramBot{ID: "bot", UserID: "owner", Token: "secret", TelegramID: 100, OwnerTelegramID: 42,
		BusinessConnectionID: "connection", BusinessOwnerID: 42, BusinessEnabled: true}
	if err := st.SaveTelegramBot(ctx, bot); err != nil {
		t.Fatal(err)
	}
	service := New(st, nil, nil, "webhook", "", nil)
	t.Cleanup(service.Close)
	registry := &telegramTestRegistry{sessions: make(map[string]store.Session), shadowStore: st}
	service.registry = registry
	service.api.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/getFile") {
			var in struct {
				FileID string `json:"file_id"`
			}
			if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
				t.Fatal(err)
			}
			body := `{"ok":true,"result":{"file_id":"` + in.FileID + `","file_size":5,"file_path":"photos/` + in.FileID + `.jpg"}}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(filepath.Base(req.URL.Path)))}, nil
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
		service.process(bot.ID)
	}
	message := func(photoID string) *telegramMessage {
		message := &telegramMessage{MessageID: 7, Date: time.Now().Unix(), BusinessConnectionID: "connection", Chat: telegramChat{ID: 123}, From: &telegramUser{ID: 55}, Text: "Фото"}
		if photoID != "" {
			message.Photo = []telegramFile{{FileID: photoID, Width: 100, Height: 100}}
		}
		return message
	}
	queue(telegramUpdate{UpdateID: 1, BusinessMessage: message("first")})
	sessionID := registry.created[0]
	messages, err := st.SessionMessages(ctx, sessionID)
	if err != nil || len(messages) != 1 || !messages[0].HasTelegramBusinessPhoto {
		t.Fatalf("initial photo: %+v, %v", messages, err)
	}
	data, _, err := service.BusinessPhoto(ctx, "owner", sessionID, messages[0].ID)
	if err != nil || string(data) != "first.jpg" {
		t.Fatalf("initial fetch: %q, %v", data, err)
	}
	if _, _, err := service.BusinessPhoto(ctx, "other", sessionID, messages[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign owner access: %v", err)
	}
	queue(telegramUpdate{UpdateID: 2, EditedBusinessMessage: message("second")})
	messages, err = st.SessionMessages(ctx, sessionID)
	if err != nil || !messages[0].HasTelegramBusinessPhoto {
		t.Fatalf("replaced photo: %+v, %v", messages, err)
	}
	data, _, err = service.BusinessPhoto(ctx, "owner", sessionID, messages[0].ID)
	if err != nil || string(data) != "second.jpg" {
		t.Fatalf("replaced fetch: %q, %v", data, err)
	}
	if sess, err := st.GetSession(ctx, sessionID); err != nil || sess.HistoryRevision != 2 {
		t.Fatalf("revision after photo replacement: %+v, %v", sess, err)
	}
	queue(telegramUpdate{UpdateID: 3, EditedBusinessMessage: message("")})
	messages, err = st.SessionMessages(ctx, sessionID)
	if err != nil || messages[0].HasTelegramBusinessPhoto {
		t.Fatalf("removed photo: %+v, %v", messages, err)
	}
	if _, _, err := service.BusinessPhoto(ctx, "owner", sessionID, messages[0].ID); err == nil {
		t.Fatal("removed photo remains available")
	}
	if sess, err := st.GetSession(ctx, sessionID); err != nil || sess.HistoryRevision != 3 {
		t.Fatalf("revision after photo removal: %+v, %v", sess, err)
	}
	unknown := message("unknown")
	unknown.MessageID = 999
	queue(telegramUpdate{UpdateID: 4, EditedBusinessMessage: unknown})
	updates, err := st.ListTelegramUpdates(ctx, bot.ID, "queued")
	if err != nil || len(updates) != 0 {
		t.Fatalf("unknown edit remains queued: %+v, %v", updates, err)
	}
	queue(telegramUpdate{UpdateID: 5, EditedBusinessMessage: message("third")})
	queue(telegramUpdate{UpdateID: 6, DeletedBusinessMessages: &telegramBusinessMessagesDeleted{BusinessConnectionID: "connection", Chat: telegramChat{ID: 123}, MessageIDs: []int64{7}}})
	messages, err = st.SessionMessages(ctx, sessionID)
	if err != nil || messages[0].HasTelegramBusinessPhoto || messages[0].Delivery != store.MessageDeliveryDeleted {
		t.Fatalf("deleted photo: %+v, %v", messages, err)
	}
	if _, _, err := service.BusinessPhoto(ctx, "owner", sessionID, messages[0].ID); err == nil {
		t.Fatal("deleted photo remains available")
	}
	if sess, err := st.GetSession(ctx, sessionID); err != nil || sess.HistoryRevision != 5 {
		t.Fatalf("revision after photo deletion: %+v, %v", sess, err)
	}
}

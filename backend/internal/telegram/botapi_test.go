package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grigory51/brigade/backend/internal/session"
	"github.com/grigory51/brigade/backend/internal/store"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestBotAPIErrorDoesNotExposeToken(t *testing.T) {
	const token = "123456:secret"
	api := newBotAPI()
	api.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Post", URL: request.URL.String(), Err: errors.New("connection refused")}
	})
	_, err := api.getMe(t.Context(), token)
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("token leaked in error: %v", err)
	}
}

func TestGuestCallerAndMessageSplit(t *testing.T) {
	owner := &telegramUser{ID: 42}
	in := inboundFrom(telegramUpdate{UpdateID: 1, GuestMessage: &telegramMessage{
		From: &telegramUser{ID: 7}, GuestBotCallerUser: owner,
		Chat: telegramChat{ID: -100}, Text: "task", GuestQueryID: "query",
	}})
	if !in.guest || in.from != owner || in.scope != "guest" {
		t.Fatalf("unexpected guest route: %+v", in)
	}
	bot := store.TelegramBot{Username: "brigade_bot"}
	if got := telegramSessionGroupLabel(bot); got != "Telegram · @brigade_bot" {
		t.Fatalf("telegram group label: %q", got)
	}
	in.message.Chat = telegramChat{ID: 77, Type: "private", Username: "alice"}
	in.chatID = 77
	if got := telegramSessionName(bot, in); got != "Telegram · @alice" {
		t.Fatalf("guest session name: %q", got)
	}
	in.message.Chat.Username = ""
	if got := telegramSessionName(bot, in); got != "Telegram · 77" {
		t.Fatalf("guest session name fallback: %q", got)
	}
	chunks := splitMessage(strings.Repeat("я", 4097), 4096)
	if len(chunks) != 2 || len([]rune(chunks[0])) > 4096 || strings.Join(chunks, "") != strings.Repeat("я", 4097) {
		t.Fatalf("unexpected chunks: %d", len(chunks))
	}
	service := New(nil, nil, nil, "webhook", "https://example.com/api/telegram", []byte("instance-secret"))
	defer service.Close()
	if service.webhookSecret("bot", "old-token") == service.webhookSecret("bot", "new-token") {
		t.Fatal("webhook secret must rotate with BotFather token")
	}
}

func TestReplyPreservesAssistantMessagesAndTracksConversation(t *testing.T) {
	service := New(nil, nil, nil, "webhook", "", nil)
	defer service.Close()
	var requests []string
	nextMessageID := int64(12)
	service.api.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		requests = append(requests, string(body))
		response := fmt.Sprintf(`{"ok":true,"result":{"message_id":%d}}`, nextMessageID)
		nextMessageID++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})
	in := inbound{message: &telegramMessage{MessageID: 10}, chatID: 42, threadID: 7}
	service.rememberMessage("bot", in.chatID, in.threadID, 11)
	encoded := encodeReply(session.PromptResult{Messages: []string{"Первое", "Второе"}}, "session")
	if err := service.reply(t.Context(), store.TelegramBot{ID: "bot", Token: "token"}, in, encoded); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 ||
		!strings.Contains(requests[0], `"text":"Первое"`) ||
		!strings.Contains(requests[0], `"reply_parameters":{"message_id":10}`) ||
		!strings.Contains(requests[1], `"text":"Второе"`) ||
		strings.Contains(requests[1], `"reply_parameters"`) {
		t.Fatalf("unexpected replies: %#v", requests)
	}
}

func TestInboundContentSupportsMediaAndGuestMessages(t *testing.T) {
	var update telegramUpdate
	err := json.Unmarshal([]byte(`{
		"update_id": 8,
		"guest_message": {
			"message_id": 12,
			"guest_query_id": "query",
			"guest_bot_caller_user": {"id": 42},
			"chat": {"id": -100, "type": "supergroup"},
			"caption": "@brigade_bot что на фото?",
			"photo": [
				{"file_id": "small", "file_size": 10, "width": 100, "height": 100},
				{"file_id": "large", "file_size": 20, "width": 1000, "height": 1000}
			],
			"location": {"latitude": 55.75, "longitude": 37.62}
		}
	}`), &update)
	if err != nil {
		t.Fatal(err)
	}
	in := inboundFrom(update)
	files := telegramMessageFiles(in.message)
	if !in.guest || !in.hasContent() || in.text != "@brigade_bot что на фото?" || len(files) != 1 || files[0].fileID != "large" {
		t.Fatalf("unexpected inbound: %+v files=%+v", in, files)
	}
	if !addressedTo(store.TelegramBot{Username: "brigade_bot"}, in.message) {
		t.Fatal("caption mention must address the bot")
	}
	if details := telegramMessageDetails(in.message); len(details) != 1 || !strings.Contains(details[0], `"latitude": 55.75`) {
		t.Fatalf("unexpected details: %#v", details)
	}
}

func TestMessageFilesIncludesNestedAndLiveMedia(t *testing.T) {
	var message telegramMessage
	err := json.Unmarshal([]byte(`{
		"message_id": 13,
		"live_photo": {
			"file_id": "live-video",
			"photo": [{"file_id": "live-small", "width": 10, "height": 10}, {"file_id": "live-large", "width": 100, "height": 100}]
		},
		"rich_message": {"blocks": [{"photo": [{"file_id": "rich-small", "file_size": 10}, {"file_id": "rich-large", "file_size": 20}]}]}
	}`), &message)
	if err != nil {
		t.Fatal(err)
	}
	files := telegramMessageFiles(&message)
	var ids []string
	for _, file := range files {
		ids = append(ids, file.fileID)
	}
	if strings.Join(ids, ",") != "live-large,live-video,rich-large" {
		t.Fatalf("unexpected files: %#v", files)
	}
}

func TestDownloadFile(t *testing.T) {
	api := newBotAPI()
	api.baseURL = "https://telegram.test"
	api.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body string
		switch request.URL.Path {
		case "/bottoken/getFile":
			body = `{"ok":true,"result":{"file_id":"photo","file_size":3,"file_path":"photos/photo.jpg"}}`
		case "/file/bottoken/photos/photo.jpg":
			body = "jpg"
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	data, name, err := api.downloadFile(t.Context(), "token", "photo", 20<<20)
	if err != nil || string(data) != "jpg" || name != "photo.jpg" {
		t.Fatalf("downloadFile: data=%q name=%q err=%v", data, name, err)
	}
}

func TestRepliesUseRichMarkdown(t *testing.T) {
	api := newBotAPI()
	var requests []string
	api.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		requests = append(requests, request.URL.Path+" "+request.Header.Get("Content-Type")+" "+string(body))
		response := `{"ok":true,"result":{"inline_message_id":"guest-inline"}}`
		if strings.HasSuffix(request.URL.Path, "/sendRichMessage") {
			response = `{"ok":true,"result":{"message_id":21}}`
		} else if strings.HasSuffix(request.URL.Path, "/sendPhoto") {
			response = `{"ok":true,"result":{"message_id":12,"photo":[{"file_id":"small"},{"file_id":"large"}]}}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(response)),
		}, nil
	})
	sent, err := api.sendMessage(context.Background(), "token", 42, 7, 9, "**жирный**")
	if err != nil || sent.MessageID != 21 {
		t.Fatal(err)
	}
	inlineMessageID, err := api.answerGuest(context.Background(), "token", "query", "# Заголовок")
	if err != nil || inlineMessageID != "guest-inline" {
		t.Fatal(err)
	}
	if err := api.editGuest(context.Background(), "token", inlineMessageID, "**Готово**"); err != nil {
		t.Fatal(err)
	}
	if err := api.setReaction(context.Background(), "token", 42, 9, "👀"); err != nil {
		t.Fatal(err)
	}
	if err := api.setReaction(context.Background(), "token", 42, 9, ""); err != nil {
		t.Fatal(err)
	}
	photo, err := api.sendPhoto(context.Background(), "token", 42, 7, 9, "generated.png", []byte("png"), false)
	if err != nil || len(photo.Photo) != 2 {
		t.Fatal(err)
	}
	if err := api.editGuestImages(context.Background(), "token", inlineMessageID, "**Готово**", []string{"large"}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 7 ||
		!strings.Contains(requests[0], "/sendRichMessage ") ||
		!strings.Contains(requests[0], `"message_thread_id":7`) ||
		!strings.Contains(requests[0], `"reply_parameters":{"message_id":9}`) ||
		!strings.Contains(requests[0], `"markdown":"**жирный**"`) ||
		!strings.Contains(requests[1], "/answerGuestQuery ") ||
		!strings.Contains(requests[1], `"rich_message":{"markdown":"# Заголовок"}`) ||
		!strings.Contains(requests[2], "/editMessageText ") ||
		!strings.Contains(requests[2], `"inline_message_id":"guest-inline"`) ||
		!strings.Contains(requests[2], `"rich_message":{"markdown":"**Готово**"}`) ||
		!strings.Contains(requests[3], `"message_id":9,"reaction":[{"emoji":"👀","type":"emoji"}]`) ||
		!strings.Contains(requests[4], `"message_id":9,"reaction":[]`) ||
		!strings.Contains(requests[5], "/sendPhoto multipart/form-data;") ||
		!strings.Contains(requests[5], `name="photo"; filename="generated.png"`) ||
		!strings.Contains(requests[5], `name="reply_parameters"`) ||
		!strings.Contains(requests[5], `{"message_id":9}`) ||
		!strings.Contains(requests[6], `"media":[{"id":"image0","media":{"media":"large","type":"photo"}}]`) {
		t.Fatalf("unexpected rich requests: %#v", requests)
	}
}

func TestPlainRepliesSkipRichMessage(t *testing.T) {
	api := newBotAPI()
	var requests []string
	api.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		requests = append(requests, request.URL.Path+" "+string(body))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":21,"inline_message_id":"guest-inline"}}`)),
		}, nil
	})
	if _, err := api.sendMessage(t.Context(), "token", 42, 0, 0, "Смотрю…"); err != nil {
		t.Fatal(err)
	}
	inlineID, err := api.answerGuest(t.Context(), "token", "query", "Смотрю…")
	if err != nil || inlineID != "guest-inline" {
		t.Fatal(err)
	}
	if err := api.editGuest(t.Context(), "token", inlineID, "Готово."); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 ||
		!strings.Contains(requests[0], "/sendMessage ") || strings.Contains(requests[0], "rich_message") ||
		!strings.Contains(requests[1], `"message_text":"Смотрю…"`) || strings.Contains(requests[1], "rich_message") ||
		!strings.Contains(requests[2], `"text":"Готово."`) || strings.Contains(requests[2], "rich_message") {
		t.Fatalf("unexpected plain requests: %#v", requests)
	}
}

func TestBotAPIErrorClassification(t *testing.T) {
	permanent := fmt.Errorf("deliver: %w", &botAPIError{code: 400, method: "sendMessage", description: "Bad Request: message thread not found"})
	if !isPermanentBotAPIError(permanent) || !isMissingMessageThread(permanent) {
		t.Fatal("message thread error must be permanent")
	}
	if isPermanentBotAPIError(&botAPIError{code: 429, method: "sendMessage", description: "Too Many Requests"}) {
		t.Fatal("rate limit must remain retryable")
	}
}

func TestPermanentDeliveryErrorDoesNotBlockBot(t *testing.T) {
	ctx := t.Context()
	st, err := store.Open(filepath.Join(t.TempDir(), "brigade.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.DB().Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES ('u1', 'user', '', 0)`); err != nil {
		t.Fatal(err)
	}
	bot := store.TelegramBot{ID: "bot", UserID: "u1", Token: "token", TelegramID: 1, Username: "brigade", Name: "Brigade", CreatedAt: time.Now()}
	if err := st.SaveTelegramBot(ctx, bot); err != nil {
		t.Fatal(err)
	}
	payload := `{"update_id":7,"message":{"message_id":9,"message_thread_id":11,"from":{"id":123},"chat":{"id":42,"type":"private"},"text":"hello"}}`
	if _, err := st.InsertTelegramUpdate(ctx, bot.ID, 7, payload); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTelegramUpdateState(ctx, bot.ID, 7, "ready", "answer", ""); err != nil {
		t.Fatal(err)
	}
	conversation := store.TelegramConversation{BotID: bot.ID, Scope: "chat", ChatID: 42, ThreadID: 11, SessionID: "session"}
	if err := st.SetTelegramConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}

	service := New(st, nil, nil, "poll", "", nil)
	defer service.Close()
	service.api.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		response := `{"ok":true,"result":true}`
		if strings.HasSuffix(request.URL.Path, "/sendMessage") {
			response = `{"ok":false,"error_code":400,"description":"Bad Request: message thread not found"}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})

	if !service.deliverReady(bot) {
		t.Fatal("permanent delivery error blocked the bot")
	}
	if updates, err := st.ListTelegramUpdates(ctx, bot.ID, "ready"); err != nil || len(updates) != 0 {
		t.Fatalf("ready updates = %v, err = %v", updates, err)
	}
	if _, err := st.TelegramConversation(ctx, bot.ID, "chat", 42, 11); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stale conversation was not removed: %v", err)
	}
}

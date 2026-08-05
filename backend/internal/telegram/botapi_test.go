package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/bottoken/getFile":
			_, _ = io.WriteString(writer, `{"ok":true,"result":{"file_id":"photo","file_size":3,"file_path":"photos/photo.jpg"}}`)
		case "/file/bottoken/photos/photo.jpg":
			_, _ = writer.Write([]byte("jpg"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	api := newBotAPI()
	api.baseURL = server.URL
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
		if strings.HasSuffix(request.URL.Path, "/sendPhoto") {
			response = `{"ok":true,"result":{"message_id":12,"photo":[{"file_id":"small"},{"file_id":"large"}]}}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(response)),
		}, nil
	})
	if err := api.sendMessage(context.Background(), "token", 42, 7, "**жирный**"); err != nil {
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
	photo, err := api.sendPhoto(context.Background(), "token", 42, 7, "generated.png", []byte("png"), false)
	if err != nil || len(photo.Photo) != 2 {
		t.Fatal(err)
	}
	if err := api.editGuestImages(context.Background(), "token", inlineMessageID, "**Готово**", []string{"large"}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 7 ||
		!strings.Contains(requests[0], "/sendRichMessage ") ||
		!strings.Contains(requests[0], `"message_thread_id":7`) ||
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
		!strings.Contains(requests[6], `"media":[{"id":"image0","media":{"media":"large","type":"photo"}}]`) {
		t.Fatalf("unexpected rich requests: %#v", requests)
	}
}

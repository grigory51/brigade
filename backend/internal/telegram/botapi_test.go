package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
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

func TestRepliesUseRichMarkdown(t *testing.T) {
	api := newBotAPI()
	var requests []string
	api.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		requests = append(requests, request.URL.Path+" "+string(body))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"inline_message_id":"guest-inline"}}`)),
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
	if len(requests) != 5 ||
		!strings.Contains(requests[0], "/sendRichMessage ") ||
		!strings.Contains(requests[0], `"message_thread_id":7`) ||
		!strings.Contains(requests[0], `"markdown":"**жирный**"`) ||
		!strings.Contains(requests[1], "/answerGuestQuery ") ||
		!strings.Contains(requests[1], `"rich_message":{"markdown":"# Заголовок"}`) ||
		!strings.Contains(requests[2], "/editMessageText ") ||
		!strings.Contains(requests[2], `"inline_message_id":"guest-inline"`) ||
		!strings.Contains(requests[2], `"rich_message":{"markdown":"**Готово**"}`) ||
		!strings.Contains(requests[3], `"message_id":9,"reaction":[{"emoji":"👀","type":"emoji"}]`) ||
		!strings.Contains(requests[4], `"message_id":9,"reaction":[]`) {
		t.Fatalf("unexpected rich requests: %#v", requests)
	}
}

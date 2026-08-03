package telegram

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
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

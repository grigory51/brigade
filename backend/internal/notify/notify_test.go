package notify

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/grigory51/brigade/backend/internal/store"
)

// fakeSettings — источник настроек для теста: отдаёт заранее заданный UserSettings.
type fakeSettings struct{ s store.UserSettings }

func (f fakeSettings) GetUserSettings(context.Context, string) (store.UserSettings, error) {
	return f.s, nil
}

// capture поднимает httptest-сервер, ловящий один POST ntfy, и возвращает адрес + доступ к
// пойманному запросу.
func capture(t *testing.T) (addr string, path *string, body *string, auth *string, done chan struct{}) {
	t.Helper()
	var mu sync.Mutex
	p, b, a := "", "", ""
	ch := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		p, b, a = r.URL.Path, string(data), r.Header.Get("Authorization")
		mu.Unlock()
		ch <- struct{}{}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &p, &b, &a, ch
}

// TestTurnEndedPostsWhenEnabled: событие включено и топик задан → POST на server/topic с
// токеном и телом.
func TestTurnEndedPostsWhenEnabled(t *testing.T) {
	addr, path, body, auth, done := capture(t)
	svc := New(fakeSettings{store.UserSettings{
		NtfyServer: addr, NtfyTopic: "mytopic", NtfyToken: "tok", NtfyEvents: "turn_end,error",
	}})

	svc.TurnEnded(context.Background(), "u1", "Моя сессия", "end_turn", nil)

	<-done
	if *path != "/mytopic" {
		t.Errorf("path = %q, want /mytopic", *path)
	}
	if !strings.Contains(*body, "завершил") {
		t.Errorf("body = %q, не содержит текст уведомления", *body)
	}
	if *auth != "Bearer tok" {
		t.Errorf("auth = %q, want Bearer tok", *auth)
	}
}

// TestTurnEndedSkipsDisabledEvent: событие error не включено → POST не уходит (сервер не
// дёрнут). Проверяем через отдельный флаг вызова.
func TestTurnEndedSkipsDisabledEvent(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	t.Cleanup(srv.Close)
	svc := New(fakeSettings{store.UserSettings{
		NtfyServer: srv.URL, NtfyTopic: "t", NtfyEvents: "turn_end", // error выключен
	}})

	svc.TurnEnded(context.Background(), "u1", "s", "", errors.New("boom"))

	if called {
		t.Error("POST ушёл на выключенное событие error")
	}
}

// TestTurnEndedSkipsCancelled: stopReason cancelled (пользователь сам остановил) → не шлём.
func TestTurnEndedSkipsCancelled(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	t.Cleanup(srv.Close)
	svc := New(fakeSettings{store.UserSettings{NtfyServer: srv.URL, NtfyTopic: "t", NtfyEvents: "turn_end"}})

	svc.TurnEnded(context.Background(), "u1", "s", "cancelled", nil)

	if called {
		t.Error("POST ушёл на отменённый пользователем turn")
	}
}

// TestTurnEndedNoTopic: топик не задан → уведомления не шлём (фича не сконфигурирована).
func TestTurnEndedNoTopic(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	t.Cleanup(srv.Close)
	svc := New(fakeSettings{store.UserSettings{NtfyServer: srv.URL, NtfyEvents: "turn_end"}})

	svc.TurnEnded(context.Background(), "u1", "s", "end_turn", nil)

	if called {
		t.Error("POST ушёл без заданного топика")
	}
}

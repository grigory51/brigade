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

type fakeSettings struct{ backends []store.NotificationBackend }

func (f fakeSettings) ListNotificationBackends(context.Context, string) ([]store.NotificationBackend, error) {
	return f.backends, nil
}

func ntfyBackend(server, topic, token, events string) store.NotificationBackend {
	return store.NotificationBackend{
		ID: "n1", Kind: "ntfy", Secret: token, Events: events,
		Config: `{"server":"` + server + `","topic":"` + topic + `"}`,
	}
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
	svc := New(fakeSettings{[]store.NotificationBackend{
		ntfyBackend(addr, "mytopic", "tok", "turn_end,error"),
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

func TestTurnEndedPostsToEveryBackend(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls++
	}))
	t.Cleanup(srv.Close)
	svc := New(fakeSettings{[]store.NotificationBackend{
		ntfyBackend(srv.URL, "one", "", "turn_end"),
		ntfyBackend(srv.URL, "two", "", "turn_end"),
	}})

	svc.TurnEnded(context.Background(), "u1", "s", "end_turn", nil)

	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

// TestTurnEndedSkipsDisabledEvent: событие error не включено → POST не уходит (сервер не
// дёрнут). Проверяем через отдельный флаг вызова.
func TestTurnEndedSkipsDisabledEvent(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	t.Cleanup(srv.Close)
	svc := New(fakeSettings{[]store.NotificationBackend{
		ntfyBackend(srv.URL, "t", "", "turn_end"), // error выключен
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
	svc := New(fakeSettings{[]store.NotificationBackend{
		ntfyBackend(srv.URL, "t", "", "turn_end"),
	}})

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
	svc := New(fakeSettings{[]store.NotificationBackend{
		ntfyBackend(srv.URL, "", "", "turn_end"),
	}})

	svc.TurnEnded(context.Background(), "u1", "s", "end_turn", nil)

	if called {
		t.Error("POST ушёл без заданного топика")
	}
}

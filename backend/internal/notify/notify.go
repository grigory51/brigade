// Package notify отправляет персональные push-уведомления пользователей через ntfy
// (https://ntfy.sh или self-hosted). Настройки (server/topic/token/events) — пер-юзерные,
// берутся из store; топик и токен задаёт сам пользователь (Настройки → Уведомления). Без
// заданного топика уведомления не шлются. Доставка best-effort: сбой POST только логируется,
// работу сессии не блокирует.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/grigory51/brigade/backend/internal/store"
)

// Ключи событий уведомлений (хранятся CSV в user_settings.ntfy_events; пользователь
// включает нужные в UI).
const (
	// EventTurnEnd — агент завершил turn штатно (stopReason без ошибки).
	EventTurnEnd = "turn_end"
	// EventError — turn завершился ошибкой.
	EventError = "error"
)

// defaultServer — публичный ntfy, если пользователь не указал свой сервер.
const defaultServer = "https://ntfy.sh"

// BackendSource отдаёт все подключения уведомлений пользователя.
type BackendSource interface {
	ListNotificationBackends(ctx context.Context, userID string) ([]store.NotificationBackend, error)
}

// Service публикует уведомления в персональный ntfy пользователя.
type Service struct {
	backends BackendSource
	http     *http.Client
	senders  map[string]func(context.Context, store.NotificationBackend, string, string) error
}

// New собирает сервис уведомлений поверх источника настроек.
func New(backends BackendSource) *Service {
	s := &Service{backends: backends, http: &http.Client{Timeout: 10 * time.Second}}
	s.senders = map[string]func(context.Context, store.NotificationBackend, string, string) error{
		"ntfy": s.sendNtfy,
	}
	return s
}

// TurnEnded уведомляет пользователя о завершении turn'а сессии, если у него настроен ntfy и
// включено соответствующее событие. sessionLabel — отображаемое имя сессии для заголовка.
// stopReason "cancelled" (пользователь сам остановил turn) игнорируется. Блокирующий вызов —
// вызывающий запускает его в отдельной горутине (доставка не должна тормозить turn).
func (s *Service) TurnEnded(ctx context.Context, userID, sessionLabel, stopReason string, turnErr error) {
	if s == nil {
		return
	}
	event := EventTurnEnd
	switch {
	case turnErr != nil:
		event = EventError
	case stopReason == "cancelled":
		// Отмену инициировал сам пользователь — он на месте, уведомлять незачем.
		return
	}

	backends, err := s.backends.ListNotificationBackends(ctx, userID)
	if err != nil {
		log.Printf("notify: list backends %s: %v", userID, err)
		return
	}

	title, message := render(sessionLabel, event, stopReason, turnErr)
	for _, backend := range backends {
		if !eventEnabled(backend.Events, event) {
			continue
		}
		if err := s.send(ctx, backend, title, message); err != nil {
			log.Printf("notify: %s %s: %v", backend.Kind, backend.ID, err)
		}
	}
}

// Test отправляет пробное уведомление по сохранённым настройкам пользователя — проверка
// топика/сервера/токена прямо из UI. В отличие от событийных уведомлений, список включённых
// событий не смотрит (отправку запросил сам пользователь) и ошибку НЕ проглатывает: смысл
// проверки в том, чтобы неверные настройки были видны сразу, а не по молчанию в сессии.
func (s *Service) Test(ctx context.Context, userID, id string) error {
	if s == nil {
		return errors.New("notify: сервис уведомлений недоступен")
	}
	backends, err := s.backends.ListNotificationBackends(ctx, userID)
	if err != nil {
		return fmt.Errorf("notify: list backends: %w", err)
	}
	for _, backend := range backends {
		if backend.ID == id {
			return s.send(ctx, backend, "brigade", "Тестовое уведомление — настройки работают.")
		}
	}
	return errors.New("notify: подключение не найдено")
}

// render строит заголовок и тело уведомления по событию.
func render(sessionLabel, event, stopReason string, turnErr error) (title, message string) {
	label := sessionLabel
	if label == "" {
		label = "Сессия"
	}
	title = "brigade · " + label
	switch event {
	case EventError:
		message = "Turn завершился ошибкой"
		if turnErr != nil {
			message += ": " + turnErr.Error()
		}
	default:
		message = "Агент завершил ответ"
		if stopReason != "" && stopReason != "end_turn" {
			message += " (" + stopReason + ")"
		}
	}
	return title, message
}

// post отправляет уведомление в ntfy: тело — текст сообщения, заголовок — в HTTP-header
// Title (ntfy-протокол). Токен (если задан) — Bearer-авторизация для защищённого топика.
type ntfyConfig struct {
	Server string `json:"server"`
	Topic  string `json:"topic"`
}

func (s *Service) send(ctx context.Context, backend store.NotificationBackend, title, message string) error {
	send := s.senders[backend.Kind]
	if send == nil {
		return fmt.Errorf("неизвестный backend %q", backend.Kind)
	}
	return send(ctx, backend, title, message)
}

func (s *Service) sendNtfy(ctx context.Context, backend store.NotificationBackend, title, message string) error {
	var cfg ntfyConfig
	if err := json.Unmarshal([]byte(backend.Config), &cfg); err != nil {
		return fmt.Errorf("ntfy config: %w", err)
	}
	if strings.TrimSpace(cfg.Topic) == "" {
		return errors.New("топик не задан — уведомления отправлять некуда")
	}
	server := strings.TrimRight(strings.TrimSpace(cfg.Server), "/")
	if server == "" {
		server = defaultServer
	}
	url := server + "/" + cfg.Topic

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(message)))
	if err != nil {
		return err
	}
	// Title содержит кириллицу; ntfy принимает её в RFC 2047 не всегда, но UTF-8 в заголовке
	// проходит через большинство серверов. При проблемах пользователь увидит тело без title.
	req.Header.Set("Title", title)
	if backend.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+backend.Secret)
	}

	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy status %d", resp.StatusCode)
	}
	return nil
}

// eventEnabled сообщает, включено ли событие в CSV-списке ntfy_events пользователя.
func eventEnabled(csv, event string) bool {
	for _, e := range strings.Split(csv, ",") {
		if strings.TrimSpace(e) == event {
			return true
		}
	}
	return false
}

// Package notify отправляет персональные push-уведомления пользователей через ntfy
// (https://ntfy.sh или self-hosted). Настройки (server/topic/token/events) — пер-юзерные,
// берутся из store; топик и токен задаёт сам пользователь (Настройки → Уведомления). Без
// заданного топика уведомления не шлются. Доставка best-effort: сбой POST только логируется,
// работу сессии не блокирует.
package notify

import (
	"bytes"
	"context"
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

// SettingsSource отдаёт пер-юзерные настройки (ntfy-поля уже расшифрованы). Совпадает с
// store.Store.GetUserSettings — интерфейс введён для изоляции и тестируемости.
type SettingsSource interface {
	GetUserSettings(ctx context.Context, userID string) (store.UserSettings, error)
}

// Service публикует уведомления в персональный ntfy пользователя.
type Service struct {
	settings SettingsSource
	http     *http.Client
}

// New собирает сервис уведомлений поверх источника настроек.
func New(settings SettingsSource) *Service {
	return &Service{settings: settings, http: &http.Client{Timeout: 10 * time.Second}}
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

	set, err := s.settings.GetUserSettings(ctx, userID)
	if err != nil {
		log.Printf("notify: get settings %s: %v", userID, err)
		return
	}
	if set.NtfyTopic == "" || !eventEnabled(set.NtfyEvents, event) {
		return
	}

	title, message := render(sessionLabel, event, stopReason, turnErr)
	if err := s.post(ctx, set, title, message); err != nil {
		log.Printf("notify: post %s: %v", userID, err)
	}
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
func (s *Service) post(ctx context.Context, set store.UserSettings, title, message string) error {
	server := strings.TrimRight(strings.TrimSpace(set.NtfyServer), "/")
	if server == "" {
		server = defaultServer
	}
	url := server + "/" + set.NtfyTopic

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(message)))
	if err != nil {
		return err
	}
	// Title содержит кириллицу; ntfy принимает её в RFC 2047 не всегда, но UTF-8 в заголовке
	// проходит через большинство серверов. При проблемах пользователь увидит тело без title.
	req.Header.Set("Title", title)
	if set.NtfyToken != "" {
		req.Header.Set("Authorization", "Bearer "+set.NtfyToken)
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

package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/grigory51/brigade/backend/internal/store"
)

func businessSource(botID, connectionID string, chatID int64) string {
	return "telegram-business/" + botID + "/" + url.PathEscape(connectionID) + "/" + strconv.FormatInt(chatID, 10)
}

func isBusinessUpdate(update telegramUpdate) bool {
	return update.BusinessConnection != nil || update.BusinessMessage != nil ||
		update.EditedBusinessMessage != nil || update.DeletedBusinessMessages != nil
}

func (s *Service) processBusinessQueued(bot store.TelegramBot, stored store.TelegramUpdate, update telegramUpdate) error {
	if connection := update.BusinessConnection; connection != nil {
		if bot.OwnerTelegramID != 0 && connection.User.ID == bot.OwnerTelegramID {
			if err := s.store.SetTelegramBusinessConnection(s.ctx, bot.ID, connection.ID, connection.User.ID, connection.Enabled, connection.Rights.CanReply); err != nil {
				return err
			}
		}
		return s.store.DeleteTelegramUpdate(s.ctx, bot.ID, stored.UpdateID)
	}
	message := update.BusinessMessage
	if message == nil {
		message = update.EditedBusinessMessage
	}
	connectionID := ""
	chatID := int64(0)
	if message != nil {
		connectionID, chatID = message.BusinessConnectionID, message.Chat.ID
	} else if deleted := update.DeletedBusinessMessages; deleted != nil {
		connectionID, chatID = deleted.BusinessConnectionID, deleted.Chat.ID
	}
	if connectionID == "" || chatID == 0 || bot.OwnerTelegramID == 0 {
		return s.store.DeleteTelegramUpdate(s.ctx, bot.ID, stored.UpdateID)
	}
	if !bot.BusinessEnabled || bot.BusinessConnectionID != connectionID || bot.BusinessOwnerID != bot.OwnerTelegramID {
		connection, err := s.api.getBusinessConnection(s.ctx, bot.Token, connectionID)
		if err != nil {
			return err
		}
		if !connection.Enabled || connection.User.ID != bot.OwnerTelegramID {
			return s.store.DeleteTelegramUpdate(s.ctx, bot.ID, stored.UpdateID)
		}
		if err := s.store.SetTelegramBusinessConnection(s.ctx, bot.ID, connection.ID, connection.User.ID, connection.Enabled, connection.Rights.CanReply); err != nil {
			return err
		}
	}
	scope := "business:" + connectionID
	conversation, err := s.store.TelegramConversation(s.ctx, bot.ID, scope, chatID, 0)
	if errors.Is(err, store.ErrNotFound) {
		if message == nil || update.EditedBusinessMessage != nil {
			return s.store.DeleteTelegramUpdate(s.ctx, bot.ID, stored.UpdateID)
		}
		name := "Telegram · " + message.Chat.Title
		if message.Chat.Title == "" {
			name = fmt.Sprintf("Telegram · %d", chatID)
			if message.Chat.Username != "" {
				name = "Telegram · @" + message.Chat.Username
			} else if message.From != nil && message.From.ID != bot.OwnerTelegramID && message.From.FirstName != "" {
				name = "Telegram · " + message.From.FirstName
			}
		}
		sess, createErr := s.registry.CreateShadow(s.ctx, bot.UserID, bot.AgentType, bot.AuthProfile, bot.Image, name, "Telegram Business · @"+bot.Username)
		if createErr != nil {
			return createErr
		}
		conversation = store.TelegramConversation{BotID: bot.ID, Scope: scope, ChatID: chatID, SessionID: sess.ID}
		if err := s.store.SetTelegramConversation(s.ctx, conversation); err != nil {
			_ = s.registry.Delete(s.ctx, sess.ID, bot.UserID)
			return err
		}
	} else if err != nil {
		return err
	}
	sess, err := s.registry.Get(s.ctx, conversation.SessionID, bot.UserID)
	if errors.Is(err, store.ErrNotFound) {
		if err := s.store.DeleteTelegramConversation(s.ctx, bot.ID, scope, chatID, 0); err != nil {
			return err
		}
		if message == nil {
			return s.store.DeleteTelegramUpdate(s.ctx, bot.ID, stored.UpdateID)
		}
		return s.processBusinessQueued(bot, stored, update)
	}
	if err != nil {
		return err
	}
	if sess.ExecutionPolicy != store.SessionExecutionOnDemand {
		return errors.New("telegram: business conversation points to non-shadow session")
	}
	source := businessSource(bot.ID, connectionID, chatID)
	if update.DeletedBusinessMessages != nil {
		for _, id := range update.DeletedBusinessMessages.MessageIDs {
			if err := s.store.UpdateExternalSessionMessage(s.ctx, sess.ID, source, strconv.FormatInt(id, 10), "[Сообщение удалено в Telegram]"); err != nil {
				return err
			}
		}
	} else if message != nil {
		text := telegramMessageText(message)
		var details []string
		for _, file := range telegramMessageFiles(message) {
			name := file.filename
			if name == "" {
				name = "файл"
			}
			details = append(details, "[Вложение Telegram: "+name+"]")
		}
		if text == "" {
			details = append(details, telegramMessageDetails(message)...)
		}
		if len(details) != 0 {
			if text != "" {
				text += "\n\n"
			}
			text += strings.Join(details, "\n")
		}
		if text != "" {
			id := strconv.FormatInt(message.MessageID, 10)
			if update.EditedBusinessMessage != nil {
				if err := s.store.UpdateExternalSessionMessage(s.ctx, sess.ID, source, id, text); err != nil {
					return err
				}
			} else {
				author := store.MessageAuthorContact
				if message.SenderBusinessBot != nil || (message.From != nil && (message.From.ID == bot.OwnerTelegramID || message.From.ID == bot.TelegramID)) {
					author = store.MessageAuthorOwner
				}
				created := time.Unix(message.Date, 0)
				if message.Date == 0 {
					created = time.Now()
				}
				inserted, err := s.store.AddSessionMessage(s.ctx, store.SessionMessage{
					ID: uuid.NewString(), SessionID: sess.ID, Author: author, Content: text,
					Source: source, ExternalID: id, IncludedInContext: true,
					Delivery: store.MessageDeliveryReceived, CreatedAt: created,
				})
				if err != nil {
					return err
				}
				if inserted && author == store.MessageAuthorContact {
					if err := s.store.MarkSessionUnread(s.ctx, sess.ID); err != nil {
						return err
					}
				}
			}
		}
	}
	return s.store.DeleteTelegramUpdate(s.ctx, bot.ID, stored.UpdateID)
}

// SendDraft — единственная точка исходящей Business-доставки: явное действие
// владельца, проверка прав и 24-часового окна до обращения к Bot API.
func (s *Service) SendDraft(ctx context.Context, userID, sessionID, messageID, replyToMessageID string) error {
	sess, err := s.registry.Get(ctx, sessionID, userID)
	if err != nil {
		return err
	}
	if sess.ExecutionPolicy != store.SessionExecutionOnDemand {
		return errors.New("telegram: требуется shadow-сессия")
	}
	draft, err := s.store.SessionMessage(ctx, sessionID, messageID)
	if err != nil {
		return err
	}
	if draft.Delivery != store.MessageDeliveryDraft {
		return errors.New("telegram: черновик уже отправлен или недоступен")
	}
	if replyToMessageID == "" {
		replyToMessageID = draft.ReplyToID
	}
	if replyToMessageID == "" {
		return errors.New("telegram: выберите входящее сообщение для ответа")
	}
	target, err := s.store.SessionMessage(ctx, sessionID, replyToMessageID)
	if err != nil {
		return err
	}
	if target.Author != store.MessageAuthorContact || target.ExternalID == "" {
		return errors.New("telegram: отвечать можно только на входящее сообщение")
	}
	parts := strings.Split(target.Source, "/")
	if len(parts) != 4 || parts[0] != "telegram-business" {
		return errors.New("telegram: ответ не привязан к Telegram Business")
	}
	connectionID, err := url.PathUnescape(parts[2])
	if err != nil {
		return err
	}
	chatID, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return err
	}
	bot, err := s.store.GetTelegramBot(ctx, parts[1])
	if err != nil {
		return err
	}
	if bot.UserID != userID || !bot.BusinessEnabled || bot.BusinessOwnerID != bot.OwnerTelegramID || bot.BusinessConnectionID != connectionID {
		return errors.New("telegram: этот бот больше не подключён к вашему Telegram Business")
	}
	if !bot.BusinessCanReply {
		return errors.New("telegram: разрешите боту отвечать в настройках Telegram Business")
	}
	conversation, err := s.store.TelegramConversation(ctx, bot.ID, "business:"+connectionID, chatID, 0)
	if err != nil || conversation.SessionID != sessionID {
		return errors.New("telegram: привязка чата больше не актуальна")
	}
	lastIncoming, err := s.store.LastExternalContactTime(ctx, sessionID, target.Source)
	if err != nil {
		return err
	}
	if time.Since(lastIncoming) >= 24*time.Hour {
		return errors.New("telegram: истекло 24-часовое окно ответа")
	}
	if len([]rune(draft.Content)) > 4096 {
		return errors.New("telegram: ответ длиннее 4096 символов; сократите черновик")
	}
	replyID, err := strconv.ParseInt(target.ExternalID, 10, 64)
	if err != nil || replyID <= 0 {
		return errors.New("telegram: у входящего сообщения нет корректного Telegram ID")
	}
	if err := s.store.SetSessionDraftDelivery(ctx, sessionID, messageID, store.MessageDeliveryDraft, store.MessageDeliverySending); err != nil {
		return err
	}
	sent, err := s.api.sendBusinessMessage(ctx, bot.Token, connectionID, chatID, replyID, draft.Content)
	if err != nil {
		state := store.MessageDeliveryUncertain
		if isPermanentBotAPIError(err) {
			state = store.MessageDeliveryDraft
		}
		_ = s.store.SetSessionDraftDelivery(context.Background(), sessionID, messageID, store.MessageDeliverySending, state)
		return err
	}
	if sent.MessageID == 0 {
		_ = s.store.SetSessionDraftDelivery(context.Background(), sessionID, messageID, store.MessageDeliverySending, store.MessageDeliveryUncertain)
		return errors.New("telegram: ответ Bot API не содержит message_id; проверьте доставку вручную")
	}
	if err := s.store.MarkSessionDraftSent(ctx, sessionID, messageID, target.Source, strconv.FormatInt(sent.MessageID, 10)); err != nil {
		_ = s.store.SetSessionDraftDelivery(context.Background(), sessionID, messageID, store.MessageDeliverySending, store.MessageDeliveryUncertain)
		return err
	}
	return nil
}

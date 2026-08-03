// Package telegram подключает пользовательских Telegram-ботов как персональный транспорт
// к ACP-сессиям Brigade. Notification backends здесь не используются: входящие сообщения
// создают turn и требуют собственной маршрутизации и durable inbox.
package telegram

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/grigory51/brigade/backend/internal/agent"
	"github.com/grigory51/brigade/backend/internal/agentimage"
	"github.com/grigory51/brigade/backend/internal/session"
	"github.com/grigory51/brigade/backend/internal/store"
)

const bindingTTL = 15 * time.Minute

type Service struct {
	store      *store.Store
	registry   *session.Registry
	images     *agentimage.Service
	api        *botAPI
	mode       string
	webhookURL string
	secretKey  []byte

	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	polls  map[string]context.CancelFunc
	busy   map[string]bool
	again  map[string]bool
}

func New(st *store.Store, registry *session.Registry, images *agentimage.Service, mode, webhookURL string, secretKey []byte) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		store: st, registry: registry, images: images, api: newBotAPI(), mode: mode,
		webhookURL: strings.TrimRight(webhookURL, "/"), secretKey: secretKey,
		ctx: ctx, cancel: cancel, polls: make(map[string]context.CancelFunc), busy: make(map[string]bool), again: make(map[string]bool),
	}
}

func (s *Service) Mode() string { return s.mode }

func (s *Service) Start(ctx context.Context) error {
	bots, err := s.store.ListAllTelegramBots(ctx)
	if err != nil {
		return err
	}
	for _, bot := range bots {
		stale, _ := s.store.ListTelegramUpdates(ctx, bot.ID, "running")
		for _, update := range stale {
			_ = s.store.SetTelegramUpdateState(ctx, bot.ID, update.UpdateID, "ready",
				"Brigade перезапустился во время выполнения запроса. Повторите сообщение: turn не запускается повторно автоматически, чтобы не выполнить действия дважды.",
				"interrupted by restart")
		}
		if err := s.activate(bot); err != nil {
			log.Printf("telegram: activate @%s: %v", bot.Username, err)
		}
	}
	return nil
}

func (s *Service) Close() {
	s.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cancel := range s.polls {
		cancel()
	}
}

func (s *Service) List(ctx context.Context, userID string) ([]store.TelegramBot, error) {
	return s.store.ListTelegramBots(ctx, userID)
}

func (s *Service) Save(ctx context.Context, userID string, bot store.TelegramBot) (store.TelegramBot, error) {
	previousToken := ""
	var previousTelegramID int64
	if bot.ID != "" {
		current, err := s.store.GetTelegramBot(ctx, bot.ID)
		if err != nil || current.UserID != userID {
			return store.TelegramBot{}, store.ErrNotFound
		}
		if strings.TrimSpace(bot.Token) == "" {
			bot.Token = current.Token
		}
		previousToken = current.Token
		previousTelegramID = current.TelegramID
		bot.OwnerTelegramID = current.OwnerTelegramID
		bot.OwnerTelegramUsername = current.OwnerTelegramUsername
		bot.BindTokenHash = current.BindTokenHash
		bot.BindTokenExpiresAt = current.BindTokenExpiresAt
		bot.UpdateOffset = current.UpdateOffset
		bot.CreatedAt = current.CreatedAt
	} else {
		bot.ID = uuid.NewString()
		bot.CreatedAt = time.Now()
	}
	bot.UserID = userID
	bot.Token = strings.TrimSpace(bot.Token)
	if bot.Token == "" {
		return store.TelegramBot{}, errors.New("telegram: bot token required")
	}
	info, err := s.api.getMe(ctx, bot.Token)
	if err != nil {
		return store.TelegramBot{}, err
	}
	if !info.IsBot || info.Username == "" {
		return store.TelegramBot{}, errors.New("telegram: token does not belong to a named bot")
	}
	if previousTelegramID != 0 && info.ID != previousTelegramID {
		return store.TelegramBot{}, errors.New("telegram: token belongs to another bot; add it as a separate integration")
	}
	bot.TelegramID, bot.Username, bot.Name = info.ID, info.Username, info.FirstName
	bot.SupportsGuestQueries, bot.HasTopicsEnabled = info.SupportsGuestQueries, info.HasTopicsEnabled
	if err := s.validateTemplate(ctx, &bot); err != nil {
		return store.TelegramBot{}, err
	}
	if err := s.store.SaveTelegramBot(ctx, bot); err != nil {
		return store.TelegramBot{}, err
	}
	if err := s.activate(bot); err != nil {
		return store.TelegramBot{}, err
	}
	if s.mode == "webhook" && previousToken != "" && previousToken != bot.Token {
		if err := s.api.deleteWebhook(ctx, previousToken); err != nil {
			log.Printf("telegram: remove previous webhook @%s: %v", bot.Username, err)
		}
	}
	return s.store.GetTelegramBot(ctx, bot.ID)
}

func (s *Service) validateTemplate(ctx context.Context, bot *store.TelegramBot) error {
	found := false
	for _, candidate := range agent.List() {
		if candidate.ID == bot.AgentType && candidate.CommandFor(store.SessionKindACP) != "" {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("telegram: agent %q does not support ACP", bot.AgentType)
	}
	if bot.AgentType == agent.Codex.ID {
		if bot.AuthProfile != "chatgpt" && bot.AuthProfile != "api-key" {
			return errors.New("telegram: Codex auth profile must be chatgpt or api-key")
		}
	} else {
		bot.AuthProfile = "claude-token"
	}
	image, err := s.images.Resolve(ctx, bot.UserID, bot.Image)
	if err != nil {
		return err
	}
	bot.Image = image
	servers, err := s.store.ListMcpServers(ctx, bot.UserID)
	if err != nil {
		return err
	}
	owned := make(map[string]bool, len(servers))
	for _, server := range servers {
		owned[server.ID] = true
	}
	for _, id := range bot.McpServers {
		if !owned[id] {
			return fmt.Errorf("telegram: MCP server %s not found", id)
		}
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, userID, id string) error {
	bot, err := s.store.GetTelegramBot(ctx, id)
	if err != nil || bot.UserID != userID {
		return store.ErrNotFound
	}
	s.deactivate(id)
	if s.mode == "webhook" {
		if err := s.api.deleteWebhook(ctx, bot.Token); err != nil {
			return err
		}
	}
	return s.store.DeleteTelegramBot(ctx, userID, id)
}

func (s *Service) BindingLink(ctx context.Context, userID, id string) (string, time.Time, error) {
	bot, err := s.store.GetTelegramBot(ctx, id)
	if err != nil || bot.UserID != userID {
		return "", time.Time{}, store.ErrNotFound
	}
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	code := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(code))
	expires := time.Now().Add(bindingTTL)
	if err := s.store.SetTelegramBinding(ctx, id, hex.EncodeToString(hash[:]), expires); err != nil {
		return "", time.Time{}, err
	}
	return "https://t.me/" + bot.Username + "?start=" + code, expires, nil
}

func (s *Service) activate(bot store.TelegramBot) error {
	s.deactivate(bot.ID)
	if s.mode == "webhook" {
		if err := s.api.setWebhook(s.ctx, bot.Token, s.webhookURL+"/"+bot.ID, s.webhookSecret(bot.ID, bot.Token)); err != nil {
			return err
		}
	} else {
		if err := s.api.deleteWebhook(s.ctx, bot.Token); err != nil {
			return err
		}
		ctx, cancel := context.WithCancel(s.ctx)
		s.mu.Lock()
		s.polls[bot.ID] = cancel
		s.mu.Unlock()
		go s.poll(ctx, bot.ID)
	}
	s.kick(bot.ID)
	return nil
}

func (s *Service) deactivate(id string) {
	s.mu.Lock()
	if cancel := s.polls[id]; cancel != nil {
		cancel()
		delete(s.polls, id)
	}
	s.mu.Unlock()
}

func (s *Service) poll(ctx context.Context, botID string) {
	for ctx.Err() == nil {
		bot, err := s.store.GetTelegramBot(ctx, botID)
		if err != nil {
			return
		}
		updates, err := s.api.getUpdates(ctx, bot.Token, bot.UpdateOffset)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("telegram: poll @%s: %v", bot.Username, err)
				select {
				case <-time.After(3 * time.Second):
				case <-ctx.Done():
				}
			}
			continue
		}
		for _, update := range updates {
			if err := s.accept(ctx, bot, update); err != nil {
				log.Printf("telegram: accept @%s update=%d: %v", bot.Username, update.UpdateID, err)
				break
			}
			bot.UpdateOffset = update.UpdateID + 1
		}
	}
}

func (s *Service) accept(ctx context.Context, bot store.TelegramBot, update telegramUpdate) error {
	if update.UpdateID < bot.UpdateOffset {
		return nil
	}
	raw, err := json.Marshal(update)
	if err != nil {
		return err
	}
	_, err = s.store.InsertTelegramUpdate(ctx, bot.ID, update.UpdateID, string(raw))
	if err != nil {
		return err
	}
	if update.UpdateID >= bot.UpdateOffset {
		if err := s.store.SetTelegramUpdateOffset(ctx, bot.ID, update.UpdateID+1); err != nil {
			return err
		}
	}
	s.kick(bot.ID)
	return nil
}

func (s *Service) webhookSecret(id, token string) string {
	h := hmac.New(sha256.New, s.secretKey)
	_, _ = h.Write([]byte("telegram-webhook\x00" + id + "\x00" + token))
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.mode != "webhook" {
			http.NotFound(w, r)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/telegram/")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		bot, err := s.store.GetTelegramBot(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		want := s.webhookSecret(id, bot.Token)
		got := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var update telegramUpdate
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		if err := dec.Decode(&update); err != nil || update.UpdateID == 0 {
			http.Error(w, "invalid update", http.StatusBadRequest)
			return
		}
		if err := s.accept(r.Context(), bot, update); err != nil {
			http.Error(w, "store update", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func (s *Service) kick(botID string) {
	s.mu.Lock()
	if s.busy[botID] {
		s.again[botID] = true
		s.mu.Unlock()
		return
	}
	s.busy[botID] = true
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			again := s.again[botID]
			delete(s.again, botID)
			delete(s.busy, botID)
			s.mu.Unlock()
			if again {
				s.kick(botID)
			}
		}()
		s.process(botID)
	}()
}

func (s *Service) process(botID string) {
	// ponytail: один последовательный worker на бота сохраняет порядок топиков; отдельные
	// per-topic workers нужны только если параллельные чаты станут измеримой проблемой.
	for s.ctx.Err() == nil {
		bot, err := s.store.GetTelegramBot(s.ctx, botID)
		if err != nil {
			return
		}
		if !s.deliverReady(bot) {
			time.AfterFunc(3*time.Second, func() { s.kick(bot.ID) })
			return
		}
		queued, err := s.store.ListTelegramUpdates(s.ctx, botID, "queued")
		if err != nil {
			time.AfterFunc(3*time.Second, func() { s.kick(bot.ID) })
			return
		}
		if len(queued) == 0 {
			return
		}
		s.processQueued(bot, queued)
	}
}

func (s *Service) deliverReady(bot store.TelegramBot) bool {
	ready, err := s.store.ListTelegramUpdates(s.ctx, bot.ID, "ready")
	if err != nil {
		return false
	}
	for _, stored := range ready {
		var update telegramUpdate
		if json.Unmarshal([]byte(stored.Payload), &update) != nil {
			_ = s.store.DeleteTelegramUpdate(s.ctx, bot.ID, stored.UpdateID)
			continue
		}
		in := inboundFrom(update)
		if err := s.reply(s.ctx, bot, in, stored.Response); err != nil {
			log.Printf("telegram: deliver @%s update=%d: %v", bot.Username, stored.UpdateID, err)
			return false
		}
		if !in.guest {
			if err := s.api.setReaction(s.ctx, bot.Token, in.chatID, in.message.MessageID, ""); err != nil {
				log.Printf("telegram: clear reaction @%s update=%d: %v", bot.Username, stored.UpdateID, err)
			}
		}
		_ = s.store.DeleteTelegramUpdate(s.ctx, bot.ID, stored.UpdateID)
	}
	return true
}

type inbound struct {
	updateID int64
	message  *telegramMessage
	from     *telegramUser
	guest    bool
	scope    string
	chatID   int64
	threadID int64
	text     string
}

func inboundFrom(update telegramUpdate) inbound {
	message, guest := update.Message, false
	if update.GuestMessage != nil {
		message, guest = update.GuestMessage, true
	}
	if message == nil {
		return inbound{updateID: update.UpdateID}
	}
	scope := "chat"
	if guest {
		scope = "guest"
	}
	from := message.From
	if guest && message.GuestBotCallerUser != nil {
		from = message.GuestBotCallerUser
	}
	return inbound{updateID: update.UpdateID, message: message, from: from, guest: guest, scope: scope, chatID: message.Chat.ID, threadID: message.MessageThreadID, text: strings.TrimSpace(message.Text)}
}

func (s *Service) processQueued(bot store.TelegramBot, queued []store.TelegramUpdate) {
	var update telegramUpdate
	if err := json.Unmarshal([]byte(queued[0].Payload), &update); err != nil {
		_ = s.store.DeleteTelegramUpdate(s.ctx, bot.ID, queued[0].UpdateID)
		return
	}
	in := inboundFrom(update)
	if in.message == nil || in.text == "" {
		_ = s.store.DeleteTelegramUpdate(s.ctx, bot.ID, in.updateID)
		return
	}
	if strings.HasPrefix(in.text, "/start ") {
		s.bindOwner(bot, in)
		return
	}
	if in.from == nil || in.from.ID != bot.OwnerTelegramID || bot.OwnerTelegramID == 0 {
		_ = s.store.DeleteTelegramUpdate(s.ctx, bot.ID, in.updateID)
		return
	}
	if in.message.Chat.Type != "private" && !in.guest && !addressedTo(bot, in.message) {
		_ = s.store.DeleteTelegramUpdate(s.ctx, bot.ID, in.updateID)
		return
	}
	in.text = stripAddress(bot, in.text)
	if !in.guest {
		_ = s.api.setReaction(s.ctx, bot.Token, in.chatID, in.message.MessageID, "👀")
	}
	if in.text == "/new" {
		s.newSession(bot, in)
		return
	}
	batch := []inbound{in}
	if !in.guest {
		for _, candidate := range queued[1:] {
			var next telegramUpdate
			if json.Unmarshal([]byte(candidate.Payload), &next) != nil {
				continue
			}
			other := inboundFrom(next)
			addressed := other.message != nil && (other.message.Chat.Type == "private" || addressedTo(bot, other.message))
			if addressed && other.from != nil && other.from.ID == bot.OwnerTelegramID &&
				!other.guest && other.scope == in.scope && other.chatID == in.chatID && other.threadID == in.threadID &&
				other.text != "" && !strings.HasPrefix(other.text, "/") {
				other.text = stripAddress(bot, other.text)
				batch = append(batch, other)
			}
		}
	}
	for _, item := range batch {
		_ = s.store.SetTelegramUpdateState(s.ctx, bot.ID, item.updateID, "running", "", "")
	}
	var prompts []string
	for _, item := range batch {
		prompts = append(prompts, item.text)
	}
	sessionID, err := s.session(bot, in)
	if err == nil {
		stopTyping := s.typing(bot, in)
		var answer string
		answer, err = s.registry.PromptAutoApprove(s.ctx, sessionID, bot.UserID, strings.Join(prompts, "\n\n"))
		stopTyping()
		if err == nil {
			s.finishWithReply(bot, batch, answer, nil)
			return
		}
	}
	s.finishWithReply(bot, batch, "Не удалось выполнить запрос в Brigade.", err)
}

func (s *Service) bindOwner(bot store.TelegramBot, in inbound) {
	code := strings.TrimSpace(strings.TrimPrefix(in.text, "/start"))
	hash := sha256.Sum256([]byte(code))
	valid := bot.BindTokenHash != "" && time.Now().Before(bot.BindTokenExpiresAt) &&
		subtle.ConstantTimeCompare([]byte(hex.EncodeToString(hash[:])), []byte(bot.BindTokenHash)) == 1
	if !valid || in.message.Chat.Type != "private" || in.from == nil {
		_ = s.store.DeleteTelegramUpdate(s.ctx, bot.ID, in.updateID)
		return
	}
	if err := s.store.BindTelegramOwner(s.ctx, bot.ID, in.from.ID, in.from.Username); err != nil {
		s.finishWithReply(bot, []inbound{in}, "Не удалось привязать Telegram к Brigade.", err)
		return
	}
	s.finishWithReply(bot, []inbound{in}, "Telegram подключён к Brigade. Напишите задачу в личном чате; топики можно использовать для отдельных сессий.", nil)
}

func addressedTo(bot store.TelegramBot, message *telegramMessage) bool {
	mention := "@" + strings.ToLower(bot.Username)
	text := strings.ToLower(message.Text)
	if strings.Contains(text, mention) || strings.HasPrefix(text, "/new@"+strings.ToLower(bot.Username)) {
		return true
	}
	return message.ReplyToMessage != nil && message.ReplyToMessage.From != nil && message.ReplyToMessage.From.ID == bot.TelegramID
}

func stripAddress(bot store.TelegramBot, text string) string {
	mention := "@" + bot.Username
	for {
		index := strings.Index(strings.ToLower(text), strings.ToLower(mention))
		if index < 0 {
			break
		}
		text = text[:index] + text[index+len(mention):]
	}
	return strings.TrimSpace(text)
}

func (s *Service) session(bot store.TelegramBot, in inbound) (string, error) {
	conversation, err := s.store.TelegramConversation(s.ctx, bot.ID, in.scope, in.chatID, in.threadID)
	if err == nil {
		if existing, getErr := s.registry.Get(s.ctx, conversation.SessionID, bot.UserID); getErr == nil && existing.Status == store.SessionStatusRunning {
			return conversation.SessionID, nil
		}
	}
	created, err := s.registry.Create(s.ctx, bot.UserID, store.SessionKindACP, bot.AgentType,
		bot.AuthProfile, "", "", bot.McpServers, bot.Image)
	if err != nil {
		return "", err
	}
	name := "Telegram · " + in.message.Chat.Title
	if in.message.Chat.Type == "private" {
		name = "Telegram · @" + bot.Username
		if in.threadID != 0 {
			name += fmt.Sprintf(" · %d", in.threadID)
		}
	} else if in.threadID != 0 {
		name += fmt.Sprintf(" · %d", in.threadID)
	}
	_, _ = s.registry.Rename(s.ctx, created.ID, bot.UserID, name)
	err = s.store.SetTelegramConversation(s.ctx, store.TelegramConversation{
		BotID: bot.ID, Scope: in.scope, ChatID: in.chatID, ThreadID: in.threadID, SessionID: created.ID,
	})
	return created.ID, err
}

func (s *Service) newSession(bot store.TelegramBot, in inbound) {
	conversation, err := s.store.TelegramConversation(s.ctx, bot.ID, in.scope, in.chatID, in.threadID)
	if err == nil {
		if _, err = s.registry.Archive(s.ctx, conversation.SessionID, bot.UserID); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.finishWithReply(bot, []inbound{in}, "Не удалось архивировать текущую сессию.", err)
			return
		}
	}
	_ = s.store.DeleteTelegramConversation(s.ctx, bot.ID, in.scope, in.chatID, in.threadID)
	s.finishWithReply(bot, []inbound{in}, "Текущая сессия отправлена в архив. Следующее сообщение создаст новую.", nil)
}

func (s *Service) typing(bot store.TelegramBot, in inbound) func() {
	if in.guest {
		return func() {}
	}
	ctx, cancel := context.WithCancel(s.ctx)
	_ = s.api.sendTyping(ctx, bot.Token, in.chatID, in.threadID)
	go func() {
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = s.api.sendTyping(ctx, bot.Token, in.chatID, in.threadID)
			case <-ctx.Done():
				return
			}
		}
	}()
	return cancel
}

func (s *Service) finishWithReply(bot store.TelegramBot, updates []inbound, answer string, turnErr error) {
	errorText := ""
	if turnErr != nil {
		errorText = turnErr.Error()
		log.Printf("telegram: turn @%s: %v", bot.Username, turnErr)
	}
	for index, update := range updates {
		if index == 0 {
			_ = s.store.SetTelegramUpdateState(s.ctx, bot.ID, update.updateID, "ready", answer, errorText)
		} else {
			_ = s.store.DeleteTelegramUpdate(s.ctx, bot.ID, update.updateID)
		}
	}
}

func (s *Service) reply(ctx context.Context, bot store.TelegramBot, in inbound, text string) error {
	if in.guest {
		return s.api.answerGuest(ctx, bot.Token, in.message.GuestQueryID, text)
	}
	return s.api.sendMessage(ctx, bot.Token, in.chatID, in.threadID, text)
}

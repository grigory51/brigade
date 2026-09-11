package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grigory51/brigade/backend/internal/memory"
	"github.com/grigory51/brigade/backend/internal/session"
	"github.com/grigory51/brigade/backend/internal/store"
)

type telegramTestRegistry struct {
	*session.Registry
	sessions                         map[string]store.Session
	created, archived, deleted       []string
	prompts                          []string
	archiveErr, deleteErr, createErr error
	beforeReset                      func()
}

func (r *telegramTestRegistry) Get(_ context.Context, id, userID string) (store.Session, error) {
	if sess, ok := r.sessions[id]; ok && sess.UserID == userID {
		return sess, nil
	}
	return store.Session{}, store.ErrNotFound
}

func (r *telegramTestRegistry) Create(_ context.Context, userID string, kind store.SessionKind, agentType, authProfile, cwd, prompt string, mcp []string, image, instructionProfile, responseProfile, groupLabel, experienceID string) (store.Session, error) {
	if r.createErr != nil {
		return store.Session{}, r.createErr
	}
	id := fmt.Sprintf("new-%d", len(r.created)+1)
	sess := store.Session{ID: id, UserID: userID, Kind: kind, AgentType: agentType, Status: store.SessionStatusRunning, InstructionProfile: instructionProfile}
	r.sessions[id] = sess
	r.created = append(r.created, id)
	return sess, nil
}

func (r *telegramTestRegistry) Rename(_ context.Context, id, userID, name string) (store.Session, error) {
	sess := r.sessions[id]
	sess.Name = name
	r.sessions[id] = sess
	return sess, nil
}

func (r *telegramTestRegistry) Archive(_ context.Context, id, userID string) (memory.ArchivedSession, error) {
	if r.beforeReset != nil {
		r.beforeReset()
	}
	if r.archiveErr != nil {
		return memory.ArchivedSession{}, r.archiveErr
	}
	r.archived = append(r.archived, id)
	delete(r.sessions, id)
	return memory.ArchivedSession{ID: id}, nil
}

func (r *telegramTestRegistry) Delete(_ context.Context, id, userID string) error {
	if r.beforeReset != nil {
		r.beforeReset()
	}
	if r.deleteErr != nil {
		return r.deleteErr
	}
	r.deleted = append(r.deleted, id)
	delete(r.sessions, id)
	return nil
}

func (r *telegramTestRegistry) PromptAutoApprove(_ context.Context, id, userID, prompt string) (session.PromptResult, error) {
	r.prompts = append(r.prompts, id+":"+prompt)
	return session.PromptResult{Messages: []string{"Ответ"}}, nil
}

func telegramModeFixture(t *testing.T, mode store.TelegramSessionMode, action store.TelegramNewSessionAction) (*Service, *telegramTestRegistry, store.TelegramBot) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "brigade.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.DB().Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES ('u1', 'user', '', 0)`); err != nil {
		t.Fatal(err)
	}
	bot := store.TelegramBot{ID: "bot", UserID: "u1", Token: "token", TelegramID: 1, Username: "brigade", OwnerTelegramID: 42, AgentType: "codex", SessionMode: mode, NewSessionAction: action}
	if err := st.SaveTelegramBot(t.Context(), bot); err != nil {
		t.Fatal(err)
	}
	service := New(st, nil, nil, "poll", "", nil)
	t.Cleanup(service.Close)
	registry := &telegramTestRegistry{sessions: map[string]store.Session{"old": {ID: "old", UserID: "u1", Status: store.SessionStatusRunning}}}
	service.registry = registry
	service.api.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		result := `{"ok":true,"result":true}`
		if strings.HasSuffix(request.URL.Path, "/sendMessage") {
			result = `{"ok":true,"result":{"message_id":100}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(result))}, nil
	})
	return service, registry, bot
}

func queueTelegramMessage(t *testing.T, service *Service, bot store.TelegramBot, id int64, text string) inbound {
	t.Helper()
	update := telegramUpdate{UpdateID: id, Message: &telegramMessage{MessageID: id, MessageThreadID: 7, From: &telegramUser{ID: 42}, Chat: telegramChat{ID: 100, Type: "private"}, Text: text}}
	payload, err := json.Marshal(update)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.store.InsertTelegramUpdate(t.Context(), bot.ID, id, string(payload)); err != nil {
		t.Fatal(err)
	}
	return inboundFrom(update)
}

func TestNewTelegramSession(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   store.TelegramSessionMode
		action store.TelegramNewSessionAction
	}{
		{"archive", store.TelegramSessionChat, store.TelegramNewSessionArchive},
		{"delete", store.TelegramSessionChat, store.TelegramNewSessionDelete},
		{"threads unchanged", store.TelegramSessionThreads, store.TelegramNewSessionDelete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, registry, bot := telegramModeFixture(t, tc.mode, tc.action)
			in := queueTelegramMessage(t, s, bot, 1, "/new")
			threadID := conversationThreadID(bot, in)
			if err := s.store.SetTelegramConversation(t.Context(), store.TelegramConversation{BotID: bot.ID, Scope: "chat", ChatID: 100, ThreadID: threadID, SessionID: "old"}); err != nil {
				t.Fatal(err)
			}
			registry.beforeReset = func() {
				running, err := s.store.ListTelegramUpdates(t.Context(), bot.ID, "running")
				if err != nil || len(running) != 1 {
					t.Fatalf("command must be durable before teardown: %v, %v", running, err)
				}
			}
			s.newSession(bot, in, "")
			if _, ok := registry.sessions["old"]; ok {
				t.Fatal("previous session still exists")
			}
			if (len(registry.deleted) != 0) != (tc.mode == store.TelegramSessionChat && tc.action == store.TelegramNewSessionDelete) {
				t.Fatalf("wrong reset action: %+v", registry)
			}
			conversation, err := s.store.TelegramConversation(t.Context(), bot.ID, "chat", 100, threadID)
			if tc.mode == store.TelegramSessionChat {
				if err != nil || conversation.SessionID != "new-1" || registry.sessions["new-1"].Kind != store.SessionKindACP {
					t.Fatalf("new ACP session not ready: %+v, %v", conversation, err)
				}
			} else if !errors.Is(err, store.ErrNotFound) || len(registry.created) != 0 {
				t.Fatal("threaded mode should create lazily")
			}
			ready, err := s.store.ListTelegramUpdates(t.Context(), bot.ID, "ready")
			if err != nil || len(ready) != 1 || ready[0].Error != "" {
				t.Fatalf("reply not ready: %+v %v", ready, err)
			}
		})
	}
}

func TestTelegramResetFailurePreservesConversation(t *testing.T) {
	for _, action := range []store.TelegramNewSessionAction{store.TelegramNewSessionArchive, store.TelegramNewSessionDelete} {
		t.Run(string(action), func(t *testing.T) {
			s, registry, bot := telegramModeFixture(t, store.TelegramSessionChat, action)
			registry.archiveErr, registry.deleteErr = errors.New("archive offline"), errors.New("busy")
			in := queueTelegramMessage(t, s, bot, 1, "/new")
			if err := s.store.SetTelegramConversation(t.Context(), store.TelegramConversation{BotID: bot.ID, Scope: "chat", ChatID: 100, SessionID: "old"}); err != nil {
				t.Fatal(err)
			}
			s.newSession(bot, in, "")
			conversation, err := s.store.TelegramConversation(t.Context(), bot.ID, "chat", 100, 0)
			if err != nil || conversation.SessionID != "old" || len(registry.created) != 0 {
				t.Fatalf("lost previous session: %+v %v", conversation, err)
			}
			ready, _ := s.store.ListTelegramUpdates(t.Context(), bot.ID, "ready")
			if len(ready) != 1 || !strings.Contains(ready[0].Response, "Не удалось") {
				t.Fatalf("missing failure reply: %+v", ready)
			}
		})
	}
}

func TestTelegramNewSessionCreateFailureCanRetry(t *testing.T) {
	s, registry, bot := telegramModeFixture(t, store.TelegramSessionChat, store.TelegramNewSessionDelete)
	in := queueTelegramMessage(t, s, bot, 1, "/new")
	if err := s.store.SetTelegramConversation(t.Context(), store.TelegramConversation{BotID: bot.ID, Scope: "chat", ChatID: 100, SessionID: "old"}); err != nil {
		t.Fatal(err)
	}
	registry.createErr = errors.New("image unavailable")
	s.newSession(bot, in, "")
	ready, _ := s.store.ListTelegramUpdates(t.Context(), bot.ID, "ready")
	if len(ready) != 1 || !strings.Contains(ready[0].Response, "Не удалось запустить") {
		t.Fatalf("missing create failure: %+v", ready)
	}
	registry.createErr = nil
	s.newSession(bot, queueTelegramMessage(t, s, bot, 2, "/new"), "")
	if len(registry.deleted) != 1 || len(registry.created) != 1 {
		t.Fatalf("retry reset twice: %+v", registry)
	}
}

func TestTelegramChatModeRouting(t *testing.T) {
	for _, mode := range []store.TelegramSessionMode{store.TelegramSessionThreads, store.TelegramSessionChat} {
		t.Run(string(mode), func(t *testing.T) {
			s, registry, bot := telegramModeFixture(t, mode, store.TelegramNewSessionArchive)
			in := queueTelegramMessage(t, s, bot, 1, "hello")
			first, err := s.session(bot, in)
			if err != nil {
				t.Fatal(err)
			}
			in.threadID = 8
			second, err := s.session(bot, in)
			if err != nil {
				t.Fatal(err)
			}
			if (first == second) != (mode == store.TelegramSessionChat) {
				t.Fatalf("wrong thread routing: %s %s", first, second)
			}
			in.chatID++
			third, err := s.session(bot, in)
			if err != nil || third == second {
				t.Fatalf("chats must remain isolated: %s %v", third, err)
			}
			in.scope, in.guest = "guest", true
			fourth, err := s.session(bot, in)
			if err != nil || fourth == third {
				t.Fatalf("guest must remain isolated: %s %v", fourth, err)
			}
			if registry.sessions[fourth].InstructionProfile != session.InstructionProfileTelegramGuest {
				t.Fatal("guest profile lost")
			}
		})
	}
}

func TestTelegramNewCommandIsQueueBoundary(t *testing.T) {
	s, registry, bot := telegramModeFixture(t, store.TelegramSessionChat, store.TelegramNewSessionDelete)
	queueTelegramMessage(t, s, bot, 1, "before")
	queueTelegramMessage(t, s, bot, 2, "/new@brigade")
	queueTelegramMessage(t, s, bot, 3, "after")
	s.process(bot.ID)
	if got := strings.Join(registry.prompts, ","); got != "new-1:before,new-2:after" {
		t.Fatalf("crossed /new boundary: %s", got)
	}
	if len(registry.deleted) != 1 || registry.deleted[0] != "new-1" {
		t.Fatalf("wrong session deleted: %v", registry.deleted)
	}
	// Telegram retries an already confirmed update; it must not reset the conversation.
	bot.UpdateOffset = 4
	if err := s.accept(t.Context(), bot, telegramUpdate{UpdateID: 2}); err != nil {
		t.Fatal(err)
	}
	if len(registry.created) != 2 {
		t.Fatal("duplicate /new created another session")
	}
}

func TestTelegramNewCommandOwnerOnly(t *testing.T) {
	s, registry, bot := telegramModeFixture(t, store.TelegramSessionChat, store.TelegramNewSessionDelete)
	queueTelegramMessage(t, s, bot, 1, "/new")
	bot.OwnerTelegramID = 99
	queued, _ := s.store.ListTelegramUpdates(t.Context(), bot.ID, "queued")
	s.processQueued(bot, queued)
	if len(registry.created)+len(registry.deleted)+len(registry.archived) != 0 {
		t.Fatal("another user executed /new")
	}
}

func TestTelegramChatQueuePreservesInterleavedTopics(t *testing.T) {
	s, registry, bot := telegramModeFixture(t, store.TelegramSessionChat, store.TelegramNewSessionDelete)
	for index, text := range []string{"A1", "B2", "A3", "/new", "A5", "B6", "A7"} {
		in := queueTelegramMessage(t, s, bot, int64(index+1), text)
		if text == "B2" || text == "B6" {
			in.message.MessageThreadID = 8
			payload, err := json.Marshal(telegramUpdate{UpdateID: in.updateID, Message: in.message})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.store.SetTelegramUpdatePayload(t.Context(), bot.ID, in.updateID, string(payload)); err != nil {
				t.Fatal(err)
			}
		}
	}
	s.process(bot.ID)
	want := "new-1:A1,new-1:B2,new-1:A3,new-2:A5,new-2:B6,new-2:A7"
	if got := strings.Join(registry.prompts, ","); got != want {
		t.Fatalf("reordered topics: %s", got)
	}
}

func TestTelegramResetResumesAcrossRestart(t *testing.T) {
	for _, stage := range []string{"before teardown", "after teardown", "after create"} {
		t.Run(stage, func(t *testing.T) {
			s, registry, bot := telegramModeFixture(t, store.TelegramSessionChat, store.TelegramNewSessionDelete)
			queueTelegramMessage(t, s, bot, 1, "/new")
			queueTelegramMessage(t, s, bot, 2, "after")
			plan, err := json.Marshal(resetPlan{SessionID: "old", SessionMode: store.TelegramSessionChat, Action: store.TelegramNewSessionDelete})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.store.StartTelegramReset(t.Context(), bot.ID, 1, string(plan)); err != nil {
				t.Fatal(err)
			}
			currentID := "old"
			if stage != "before teardown" {
				delete(registry.sessions, "old")
			}
			if stage == "after create" {
				currentID = "already-created"
				registry.sessions[currentID] = store.Session{ID: currentID, UserID: bot.UserID, Status: store.SessionStatusRunning}
			}
			if err := s.store.SetTelegramConversation(t.Context(), store.TelegramConversation{BotID: bot.ID, Scope: "chat", ChatID: 100, SessionID: currentID}); err != nil {
				t.Fatal(err)
			}
			// Не запускаем фоновый worker до проверки восстановления inbox.
			s.mode = "webhook"
			transport := s.api.http.Transport
			s.api.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline during startup") })
			if err := s.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			s.api.http.Transport = transport
			queued, err := s.store.ListTelegramUpdates(t.Context(), bot.ID, "queued")
			if err != nil || len(queued) != 2 || queued[0].ResetPlan != string(plan) {
				t.Fatalf("reset barrier lost: %+v, %v", queued, err)
			}
			s.process(bot.ID)
			wantID := "new-1"
			if stage == "after create" {
				wantID = currentID
			}
			if got := strings.Join(registry.prompts, ","); got != wantID+":after" {
				t.Fatalf("sent into old session after restart: %s", got)
			}
			if stage == "after create" && len(registry.created)+len(registry.deleted) != 0 {
				t.Fatal("restarted command reset the new session")
			}
			if stage == "before teardown" && (len(registry.deleted) != 1 || registry.deleted[0] != "old") {
				t.Fatalf("did not retire old target: %v", registry.deleted)
			}
		})
	}
}

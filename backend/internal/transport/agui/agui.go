// Package agui обслуживает канонический AG-UI protocol поверх HTTP/SSE.
//
// Эндпоинт POST /api/ag-ui/run принимает RunAgentInput (канонический JSON @ag-ui/core)
// и отвечает потоком Server-Sent Events: каждое AG-UI-событие сериализуется как
// `data: {json}\n\n` с немедленным Flush. Поток открывается RUN_STARTED, закрывается
// RUN_FINISHED либо RUN_ERROR. Клиент (@ag-ui/client HttpAgent) подключается напрямую.
//
// Адресация: threadId запроса трактуется как идентификатор brigade-сессии. ACP-клиент
// сессии берётся из реестра живых сессий (provider.Bindable) и привязывается к SSE-потоку
// на время прогона. Аутентификация — Bearer access-JWT на каждый запрос (в отличие от
// одноразового тикета CLI-режима).
//
// Permission (human-in-the-loop): SSE однонаправлен, поэтому запрос разрешения уходит
// клиенту событием CUSTOM (Name=CustomPermissionName), а ответ приходит отдельным
// POST /api/ag-ui/permission с тем же id. Резолвер блокируется на общем сторе ожиданий
// до ответа или до разрыва потока.
package agui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/grigory51/brigade/backend/internal/auth"

	"github.com/google/uuid"

	"github.com/grigory51/brigade/backend/internal/acp"
	aguimodel "github.com/grigory51/brigade/backend/internal/agui"
)

// CustomPermissionName — значение поля name события CUSTOM, несущего запрос разрешения
// клиенту. Полезная нагрузка (value) — agui.PermissionRequest.
const CustomPermissionName = "permission_request"

// TokenVerifier проверяет access-JWT из заголовка Authorization. Удовлетворяется
// *auth.JWT (метод Verify возвращает claims с Subject — идентификатором пользователя).
type TokenVerifier interface {
	Verify(token string) (userID string, ok bool)
}

// ClientProvider отдаёт биндер ACP-клиента сессии по идентификатору. Удовлетворяется
// реестром живых сессий (session.Registry). threadId AG-UI-запроса соответствует
// brigade sessionID.
type ClientProvider interface {
	// Bindable возвращает биндер ACP-клиента сессии для пользователя. ok=false, если
	// сессия неизвестна, не в ACP-режиме или принадлежит другому пользователю.
	Bindable(sessionID, userID string) (b Bindable, ok bool)
}

// Bindable — ACP-клиент сессии, к которому транспорт подключает текущий SSE-поток.
// Реализуется *acp.Client напрямую.
type Bindable interface {
	// Bind связывает клиента с потоком: задаёт sink доставки AG-UI-событий и resolver
	// permission-flow на время прогона. Возвращает unbind для снятия привязки.
	Bind(sink acp.EventSink, resolver acp.PermissionResolver) (unbind func())
	// Prompt передаёт пользовательский ввод агенту (блокируется до конца turn'а) и
	// возвращает stopReason завершённого turn'а.
	Prompt(ctx context.Context, text string) (stopReason string, err error)
	// SetFrontendTools обновляет реестр кастомных инструментов сессии.
	SetFrontendTools(tools []acp.FrontendTool)
	// FinishStreams закрывает открытые потоковые сообщения перед завершением
	// replay-прогона, чтобы RUN_FINISHED не уходил поверх незакрытого сообщения.
	FinishStreams()
	// Messages возвращает историю чата сессии массивом сообщений (для
	// ThreadHistoryAdapter фронта).
	Messages() []acp.Message
	// Commands возвращает последний список slash-команд агента (для автокомплита
	// composer'а; отдаётся вместе с историей).
	Commands() []aguimodel.AvailableCommand
}

// *acp.Client удовлетворяет Bindable напрямую — проверяется на этапе компиляции.
var _ Bindable = (*acp.Client)(nil)

// runAgentInput — подмножество канонического RunAgentInput (@ag-ui/core), которое
// использует brigade. Остальные поля (state, context, forwardedProps) принимаются, но
// в текущем режиме не интерпретируются.
type runAgentInput struct {
	ThreadID string         `json:"threadId"`
	RunID    string         `json:"runId"`
	Messages []inputMessage `json:"messages"`
	Tools    []inputTool    `json:"tools"`
}

// inputMessage — сообщение из RunAgentInput. Для prompt берётся текст последнего
// пользовательского сообщения (см. lastUserText).
type inputMessage struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	Content string `json:"content"`
}

// inputTool — описание frontend-tool из RunAgentInput.tools[]. parameters — JSON Schema
// входных параметров инструмента.
type inputTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// permissionStore связывает (threadId,id) запроса разрешения с каналом ответа. Общий для
// /run (резолвер регистрирует ожидание) и /permission (доставляет решение клиента).
type permissionStore struct {
	mu      sync.Mutex
	pending map[string]chan string
}

func newPermissionStore() *permissionStore {
	return &permissionStore{pending: make(map[string]chan string)}
}

// register заводит ожидание ответа по ключу и возвращает канал и функцию снятия.
func (p *permissionStore) register(key string) (<-chan string, func()) {
	ch := make(chan string, 1)
	p.mu.Lock()
	p.pending[key] = ch
	p.mu.Unlock()
	return ch, func() {
		p.mu.Lock()
		delete(p.pending, key)
		p.mu.Unlock()
	}
}

// deliver доставляет решение ожидающему резолверу. ok=false, если ожидания с таким
// ключом нет (повтор, опоздавший ответ).
func (p *permissionStore) deliver(key, decision string) bool {
	p.mu.Lock()
	ch, ok := p.pending[key]
	p.mu.Unlock()
	if !ok {
		return false
	}
	// Канал буферизован на 1 и читается ровно один раз; неблокирующая отправка защищает
	// от повторного ответа на уже разрешённый запрос.
	select {
	case ch <- decision:
		return true
	default:
		return false
	}
}

// Mux собирает HTTP-обработчики AG-UI и регистрирует их в переданном ServeMux под
// /api/ag-ui/run и /api/ag-ui/permission. permissionStore разделяется обоими.
func Mux(mux *http.ServeMux, verifier TokenVerifier, provider ClientProvider) {
	perms := newPermissionStore()
	mux.Handle("POST /api/ag-ui/run", runHandler(verifier, provider, perms))
	mux.Handle("POST /api/ag-ui/permission", permissionHandler(verifier, perms))
	mux.Handle("GET /api/ag-ui/history", historyHandler(verifier, provider))
}

// historyHandler обслуживает GET /api/ag-ui/history?threadId=<id>: отдаёт историю чата
// сессии массивом сообщений {id, role, content}. Используется ThreadHistoryAdapter
// фронта для восстановления прошлых turn'ов при открытии треда (assistant-ui
// складывает их с корректными ролями, в отличие от SSE-replay, который склеивает поток
// одного run'а в единственное assistant-сообщение).
func historyHandler(verifier TokenVerifier, provider ClientProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := verifier.Verify(accessToken(r))
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		threadID := r.URL.Query().Get("threadId")
		if threadID == "" {
			http.Error(w, "threadId required", http.StatusBadRequest)
			return
		}

		bindable, ok := provider.Bindable(threadID, userID)
		if !ok {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		// Пустые история/команды сериализуются как [], а не null: фронт ожидает массивы.
		msgs := bindable.Messages()
		if msgs == nil {
			msgs = []acp.Message{}
		}
		cmds := bindable.Commands()
		if cmds == nil {
			cmds = []aguimodel.AvailableCommand{}
		}
		_ = json.NewEncoder(w).Encode(struct {
			Messages []acp.Message              `json:"messages"`
			Commands []aguimodel.AvailableCommand `json:"commands"`
		}{Messages: msgs, Commands: cmds})
	})
}

// runHandler обслуживает POST /api/ag-ui/run: парсит RunAgentInput, аутентифицирует
// пользователя по Bearer, привязывает ACP-клиента сессии к SSE-потоку и прогоняет
// turn агента, эмитя канонический поток событий.
func runHandler(verifier TokenVerifier, provider ClientProvider, perms *permissionStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := verifier.Verify(accessToken(r))
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var in runAgentInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid RunAgentInput", http.StatusBadRequest)
			return
		}
		if in.ThreadID == "" {
			http.Error(w, "threadId required", http.StatusBadRequest)
			return
		}

		bindable, ok := provider.Bindable(in.ThreadID, userID)
		if !ok {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		runID := in.RunID
		if runID == "" {
			runID = uuid.NewString()
		}

		newRun(r.Context(), w, flusher, bindable, perms, in.ThreadID, runID).serve(in)
	})
}

// permissionHandler обслуживает POST /api/ag-ui/permission: доставляет решение клиента
// ожидающему резолверу /run. Тело — {threadId, id, decision}.
func permissionHandler(verifier TokenVerifier, perms *permissionStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := verifier.Verify(accessToken(r)); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var body struct {
			ThreadID string `json:"threadId"`
			ID       string `json:"id"`
			Decision string `json:"decision"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		// Доставка best-effort: отсутствие ожидания (опоздавший/повторный ответ) — не
		// ошибка клиента, отвечаем 204 в обоих случаях.
		perms.deliver(permissionKey(body.ThreadID, body.ID), body.Decision)
		w.WriteHeader(http.StatusNoContent)
	})
}

// accessToken извлекает access-JWT из запроса: сперва из заголовка
// Authorization: Bearer <token> (мобильный клиент), затем из httpOnly-cookie
// brigade_access (web-клиент). Браузер не может выставить кастомный заголовок и
// хранит токен в httpOnly-cookie, недоступной JS, — поэтому cookie-fallback
// обязателен, иначе web-сессия теряет доступ к AG-UI после перезагрузки страницы.
func accessToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(header, "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	if c, err := r.Cookie(auth.AccessCookieName); err == nil {
		return c.Value
	}
	return ""
}

// permissionKey строит ключ ожидания разрешения из threadId и id запроса.
func permissionKey(threadID, id string) string { return threadID + "\x00" + id }

// toFrontendTools преобразует tools[] из RunAgentInput в реестр acp.FrontendTool.
// parameters канонического Tool соответствует InputSchema (JSON Schema параметров).
func toFrontendTools(tools []inputTool) []acp.FrontendTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]acp.FrontendTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, acp.FrontendTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Parameters,
		})
	}
	return out
}

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

	acpsdk "github.com/coder/acp-go-sdk"
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

// WorkflowInfo — состояние workflow-запуска харнесса агента для панели фоновых задач
// (wire-форма session.WorkflowInfo; поля см. там).
type WorkflowInfo struct {
	RunID           string `json:"runId"`
	Name            string `json:"name"`
	AgentsStarted   int    `json:"agentsStarted"`
	AgentsDone      int    `json:"agentsDone"`
	Done            bool   `json:"done"`
	Active          bool   `json:"active"`
	LastActivitySec int64  `json:"lastActivitySec"`
}

// WorkflowLister отдаёт workflow-запуски харнесса агента сессии. Удовлетворяется
// обвязкой session.Registry (см. cmd/brigade aguiProvider). ok=false — сессия
// неизвестна, не ACP или чужая.
type WorkflowLister interface {
	SessionWorkflows(ctx context.Context, sessionID, userID string) ([]WorkflowInfo, bool)
}

// Bindable — ACP-клиент сессии, к которому транспорт подключает текущий SSE-поток.
// Реализуется *acp.Client напрямую.
type Bindable interface {
	// Bind связывает клиента с потоком: задаёт sink доставки AG-UI-событий и resolver
	// permission-flow на время прогона. Возвращает unbind для снятия привязки.
	Bind(sink acp.EventSink, resolver acp.PermissionResolver) (unbind func())
	// Prompt передаёт пользовательский ввод агенту (блокируется до конца turn'а) и
	// возвращает stopReason завершённого turn'а. onTurnStart (может быть nil) вызывается
	// под turn-барьером до отправки запроса агенту — точка привязки sink нового прогона
	// (см. acp.Client.Prompt).
	Prompt(ctx context.Context, text string, onTurnStart func()) (stopReason string, err error)
	// Cancel просит агента отменить текущий turn (session/cancel). Идемпотентно,
	// безопасно без активного turn'а.
	Cancel(ctx context.Context) error
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
	// ConfigOptions возвращает текущие конфигурационные опции сессии (модель, режим
	// прав, усилие) в wire-формате ACP; отдаётся вместе с историей.
	ConfigOptions() []acpsdk.SessionConfigOption
	// SetConfigOption устанавливает значение опции сессии и возвращает актуальный
	// полный набор опций.
	SetConfigOption(ctx context.Context, configID, value string) ([]acpsdk.SessionConfigOption, error)
	// Status сообщает, генерирует ли агент сейчас (живой Prompt или недавняя фоновая
	// активность), и монотонный seq ленты для детекта новых сообщений на клиенте.
	Status() (generating bool, seq int)
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

// PermissionStore связывает (threadId,id) запроса разрешения с каналом ответа. Общий для
// SSE-прогона /run (резолвер регистрирует ожидание через Register) и Connect-метода
// AcpService.ResolvePermission (доставляет решение клиента через Deliver). Создаётся в
// main и передаётся обоим потребителям.
type PermissionStore struct {
	mu      sync.Mutex
	pending map[string]chan string
}

// NewPermissionStore создаёт пустой стор ожиданий permission-flow.
func NewPermissionStore() *PermissionStore {
	return &PermissionStore{pending: make(map[string]chan string)}
}

// Register заводит ожидание ответа по ключу и возвращает канал и функцию снятия.
func (p *PermissionStore) Register(key string) (<-chan string, func()) {
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

// Deliver доставляет решение ожидающему резолверу. ok=false, если ожидания с таким
// ключом нет (повтор, опоздавший ответ).
func (p *PermissionStore) Deliver(key, decision string) bool {
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

// PermissionKey строит ключ ожидания разрешения из threadId и id запроса. Общий формат
// для регистрации (резолвер /run) и доставки (AcpService.ResolvePermission).
func PermissionKey(threadID, id string) string { return threadID + "\x00" + id }

// Mux регистрирует единственный сырой HTTP-эндпоинт AG-UI — потоковый прогон turn'а
// POST /api/ag-ui/run. Он остаётся вне ConnectRPC: это SSE в формате стороннего
// @ag-ui/client, который Connect выразить не может. Управляющие ручки ACP (история,
// статус, workflow, отмена, опции, ответ на разрешение) — в brigade.v1.AcpService.
// perms разделяется с AcpService.ResolvePermission (создаётся в main).
func Mux(mux *http.ServeMux, verifier TokenVerifier, provider ClientProvider, perms *PermissionStore) {
	mux.Handle("POST /api/ag-ui/run", runHandler(verifier, provider, perms))
}

// runHandler обслуживает POST /api/ag-ui/run: парсит RunAgentInput, аутентифицирует
// пользователя по Bearer, привязывает ACP-клиента сессии к SSE-потоку и прогоняет
// turn агента, эмитя канонический поток событий.
func runHandler(verifier TokenVerifier, provider ClientProvider, perms *PermissionStore) http.Handler {
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

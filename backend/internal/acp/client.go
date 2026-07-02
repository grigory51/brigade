package acp

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	"github.com/grigory51/brigade/backend/internal/agui"
)

// gracefulCloseTimeout — суммарный бюджет на штатное завершение adapter'а после запроса
// session/close и закрытия stdin, прежде чем перейти к SIGTERM, а затем SIGKILL.
const gracefulCloseTimeout = 4 * time.Second

// adapterBinary — имя бинаря adapter'а (npm-пакет @agentclientprotocol/claude-agent-acp).
// Это agent-side ACP поверх Claude Agent SDK: brigade говорит с ним на стандартном ACP.
const adapterBinary = "claude-agent-acp"

// historyCap — максимальное число AG-UI событий, удерживаемых в ленте сессии для
// воспроизведения новому подключению. При превышении самые старые события
// отбрасываются; этого хвоста достаточно, чтобы рефреш восстановил недавний чат.
const historyCap = 2000

// EventSink принимает транслированные AG-UI-события для отправки клиенту. Реализуется
// транспортом (agui): пишет событие в SSE-поток. Возврат ошибки означает, что канал
// к клиенту мёртв; ACP-клиент в этом случае прекращает попытки доставки.
type EventSink func(agui.Event) error

// PermissionResolver запрашивает у клиента решение по разрешению и блокируется до ответа.
// Реализуется транспортом (agui): регистрирует ожидание по req.ID, отдаёт клиенту CUSTOM
// {name:"permission_request"} и ждёт ответа отдельным POST /api/ag-ui/permission.
// Возвращает OptionID выбранного варианта. Если ctx отменён (клиент отключился), ошибку —
// ACP-вызов тогда трактует исход как cancelled.
type PermissionResolver func(ctx context.Context, req agui.PermissionRequest) (optionID string, err error)

// Options — параметры запуска ACP-клиента для одной сессии.
type Options struct {
	// Cwd — рабочая директория агента (абсолютный путь). В docker-режиме это путь
	// внутри контейнера; спавн самого процесса задаётся вызывающей стороной.
	Cwd string
	// OAuthToken — подписочный токен Claude Code, пробрасывается adapter'у через
	// переменную окружения CLAUDE_CODE_OAUTH_TOKEN (модель доступа — подписка).
	OAuthToken string
	// ResumeSessionID — идентификатор существующей ACP-сессии для восстановления.
	// При непустом значении и поддержке агентом session/load клиент загружает эту
	// сессию (LoadSession) вместо создания новой, благодаря чему агент реплеит прошлый
	// thread нотификациями session/update и история чата восстанавливается. Используется
	// при восстановлении сессий после рестарта бэкенда.
	ResumeSessionID string
	// ForkFromSessionID — идентификатор ACP-сессии, от которой создаётся ветка
	// (session/fork): агент клонирует сессию с её историей в новую, независимую.
	// Взаимоисключим с ResumeSessionID; при отсутствии у агента capability fork —
	// ошибка (тихий откат в пустую сессию обманул бы пользователя).
	ForkFromSessionID string
	// SpawnProc порождает процесс adapter'а. nil — локальный subprocess
	// claude-agent-acp; docker-режим передаёт сюда фабрику контейнерного процесса
	// (см. spawn.DockerACPSpawner).
	SpawnProc ProcSpawner
}

// Client управляет одной ACP-сессией: владеет subprocess'ом adapter'а, реализует
// acp.Client (callbacks агента) и хранит состояние сессии (id, реестр frontend-tools).
//
// Жизненный цикл клиента равен жизни сессии brigade и может пережить несколько
// WebSocket-подключений (reconnect, resume). Поэтому доставка событий клиенту (sink) и
// permission-flow (resolver) задаются не при создании, а привязываются на время каждого
// WS-сеанса через Bind; вне сеанса используются безопасные заглушки (события
// отбрасываются, разрешения трактуются как cancelled).
type Client struct {
	opts Options

	// proc — процесс adapter'а (локальный subprocess либо контейнер), абстрагированный
	// AgentProc: Client общается с ним только через stdio и сигналы.
	proc AgentProc
	conn *acpsdk.ClientSideConnection
	// agentCaps — возможности агента, заявленные при Initialize. Используются для
	// решения, поддерживает ли агент session/close при graceful-остановке.
	agentCaps acpsdk.AgentCapabilities
	// closeOnce гарантирует однократный teardown: повторный Close безопасен и не шлёт
	// сигналы уже завершённому процессу.
	closeOnce sync.Once
	// promptMu сериализует turn'ы: ACP-сессия обрабатывает ходы строго последовательно,
	// а brigade допускает параллельные POST /api/ag-ui/run в один тред. Без сериализации
	// потоковые сообщения двух turn'ов пересеклись бы в общем sink (START одного между
	// CONTENT другого). Держится на всё время Prompt.
	promptMu sync.Mutex

	sessionID acpsdk.SessionId

	mu sync.Mutex
	// sink/resolver — текущая привязка к WS-сеансу. nil вне сеанса (см. emit/resolve).
	sink     EventSink
	resolver PermissionResolver
	// history — лента ранее отправленных AG-UI событий сессии. В SSE-поток при Bind
	// НЕ реплеится (это ломало агрегатор клиента) — служит источником для Messages(),
	// по которому GET /api/ag-ui/history восстанавливает ленту массивом сообщений.
	// Turn агента доходит до конца и копится здесь независимо от подключения клиента
	// (см. emit/SessionUpdate). Permission-запросы в историю не пишутся: их повторный
	// показ после ответа некорректен (см. emit).
	history []agui.Event
	// lastUsage — последнее событие расхода контекста. Хранится отдельно от history,
	// потому что usage_update приходит высокочастотно (на каждый прирост токенов): в
	// ленте важно лишь актуальное значение, а не вся последовательность. Реплеится в
	// Bind после истории. nil, пока агент не сообщил расход.
	lastUsage *agui.Event
	// lastCommands — последний список slash-команд агента (ACP available_commands_update).
	// Хранится отдельно от history по той же причине, что и lastUsage: это снимок,
	// идемпотентно заменяющий предыдущий, важно лишь актуальное значение. Реплеится в
	// Bind. nil, пока агент не прислал список команд.
	lastCommands *agui.Event
	// promptActive — true, пока выполняется живой Prompt. В этом окне трансляцию
	// user_message_chunk подавляем, чтобы не дублировать оптимистичное сообщение фронта
	// (см. translate.emitUserMessage). Доступ — под mu.
	promptActive bool
	// bindGen — номер текущей привязки. Увеличивается при каждом Bind; unbind снимает
	// привязку только если её номер ещё актуален, чтобы закрытие старого сеанса не
	// затёрло привязку нового.
	bindGen uint64
	// frontendTools — реестр кастомных сниппетов, присланный фронтом. Пробрасывается
	// агенту при Prompt.
	frontendTools []FrontendTool

	// stream отслеживает открытые потоковые сообщения (текст/размышление) для
	// расстановки START/END вокруг чанков по смене messageId. Доступ — под mu.
	stream streamState
	// toolCalls — состояние tool call'ов по toolCallId: агент шлёт несколько
	// tool_call_update на один вызов, а клиент требует ровно один TOOL_CALL_END и хранит
	// один результат. Здесь копится содержательный результат (diff «липнет» — статусная
	// строка его не затирает) и отслеживается, закрыт ли вызов. Доступ — под mu.
	toolCalls map[string]*toolCallState
}

// убеждаемся, что Client удовлетворяет интерфейсу acp.Client на этапе компиляции.
var _ acpsdk.Client = (*Client)(nil)

// New спавнит процесс adapter'а (локальный subprocess либо контейнер — по
// opts.SpawnProc), устанавливает ACP-соединение, выполняет Initialize и заводит сессию.
// Возвращает готовый к Prompt клиент. При ошибке процесс завершается.
func New(ctx context.Context, opts Options) (*Client, error) {
	if opts.Cwd == "" {
		return nil, fmt.Errorf("acp: cwd не задан")
	}

	var proc AgentProc
	var err error
	if opts.SpawnProc != nil {
		proc, err = opts.SpawnProc(ctx)
	} else {
		proc, err = spawnLocalProc(opts)
	}
	if err != nil {
		return nil, err
	}

	c := &Client{opts: opts, proc: proc}
	// peerInput — куда пишем запросы агенту (его stdin); peerOutput — откуда читаем его
	// ответы и нотификации (его stdout).
	c.conn = acpsdk.NewClientSideConnection(c, proc.Stdin(), proc.Stdout())

	if err := c.handshake(ctx); err != nil {
		// Handshake не удался — корректно сворачиваем процесс: убиваем и реапим, чтобы
		// не оставить зомби и не утечь pipes.
		_ = proc.Kill()
		_ = proc.Wait()
		return nil, err
	}
	return c, nil
}

// handshake выполняет Initialize и заводит сессию: при заданном ResumeSessionID и
// поддержке агентом session/load — загружает существующую (LoadSession), иначе создаёт
// новую (NewSession). Заявляем поддержку файловых операций, чтобы агент мог запрашивать
// чтение/запись через клиента (Read/WriteTextFile).
func (c *Client) handshake(ctx context.Context) error {
	initResp, err := c.conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{
			Fs: acpsdk.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true},
		},
	})
	if err != nil {
		return fmt.Errorf("acp: initialize: %w", err)
	}
	// Сохраняем возможности агента: по ним при остановке решаем, слать ли session/close.
	c.agentCaps = initResp.AgentCapabilities

	// Ветка сессии: session/fork клонирует исходную сессию агента (с историей) в новую
	// независимую. Ошибка не маскируется — вызывающий (registry.Fork) откатывает
	// создание ветки целиком.
	if c.opts.ForkFromSessionID != "" {
		if initResp.AgentCapabilities.SessionCapabilities.Fork == nil {
			return fmt.Errorf("acp: agent does not support session/fork")
		}
		forkResp, err := c.conn.UnstableForkSession(ctx, acpsdk.UnstableForkSessionRequest{
			SessionId:  acpsdk.SessionId(c.opts.ForkFromSessionID),
			Cwd:        c.opts.Cwd,
			McpServers: []acpsdk.UnstableMcpServer{},
		})
		if err != nil {
			return fmt.Errorf("acp: fork session %s: %w", c.opts.ForkFromSessionID, err)
		}
		c.sessionID = forkResp.SessionId
		return nil
	}

	// Resume через session/load: агент реплеит прошлый thread нотификациями
	// session/update, которые наполняют историю для воспроизведения в Bind. Id при
	// load известен заранее, новый не выдаётся. Ранний return — только при успехе;
	// любая ошибка load (в т.ч. resourceNotFound на устаревший/неизвестный id) не
	// валит восстановление: логируем и проваливаемся в NewSession ниже.
	if c.opts.ResumeSessionID != "" && initResp.AgentCapabilities.LoadSession {
		if _, err := c.conn.LoadSession(ctx, acpsdk.LoadSessionRequest{
			SessionId:  acpsdk.SessionId(c.opts.ResumeSessionID),
			Cwd:        c.opts.Cwd,
			McpServers: []acpsdk.McpServer{},
		}); err != nil {
			log.Printf("acp: load session %s failed (%v), starting fresh session", c.opts.ResumeSessionID, err)
		} else {
			c.sessionID = acpsdk.SessionId(c.opts.ResumeSessionID)
			// Реплей session/load не закрывает потоковые сообщения — последний текст
			// остаётся «открытым» в stream-состоянии. Без закрытия первый Bind нового
			// run'а переоткрыл бы старый messageId, и клиентский агрегатор принял бы его
			// за id текущего ответа (стрим уезжает в старое сообщение — сбой порядка).
			c.FinishStreams()
			return nil
		}
	} else if c.opts.ResumeSessionID != "" {
		// Запрошен resume, но агент не поддерживает session/load. Не падаем — заводим
		// новую сессию (история не восстановится, но сессия рабочая).
		log.Printf("acp: agent has no loadSession capability, starting fresh session for %s", c.opts.ResumeSessionID)
	}

	newSess, err := c.conn.NewSession(ctx, acpsdk.NewSessionRequest{
		Cwd:        c.opts.Cwd,
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		return fmt.Errorf("acp: new session: %w", err)
	}
	c.sessionID = newSess.SessionId
	return nil
}

// SessionID возвращает идентификатор ACP-сессии агента. Сохраняется в store как
// agent_session_id для последующего resume.
func (c *Client) SessionID() string { return string(c.sessionID) }

// Message — одно сообщение чата для восстановления истории на клиенте (ThreadHistory).
type Message struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Commands возвращает последний известный список slash-команд агента (ACP
// available_commands_update). Отдаётся вместе с историей: команды приходят
// CUSTOM-событием в SSE-прогоне, а при открытии треда прогон больше не стартует
// (история грузится отдельным запросом), поэтому набор команд нужно вернуть явно.
func (c *Client) Commands() []agui.AvailableCommand {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastCommands == nil {
		return nil
	}
	cmds, ok := c.lastCommands.Value.(agui.AvailableCommands)
	if !ok {
		return nil
	}
	return cmds.Commands
}

// Messages собирает историю чата как массив сообщений, агрегируя накопленные AG-UI
// события (TEXT_MESSAGE_START/CONTENT/END) по messageId. Нужен для ThreadHistoryAdapter
// фронта: клиент assistant-ui восстанавливает прошлые turn'ы массивом сообщений с
// корректными ролями, а не SSE-replay'ем (который склеивает весь поток в одно
// assistant-сообщение).
//
// Учитываются только текстовые сообщения (user и assistant); размышления, tool-call'ы и
// прочие события в историю чата не разворачиваются. Роль берётся из START-события.
func (c *Client) Messages() []Message {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out []Message
	idx := make(map[string]int) // messageId → позиция в out
	for _, evt := range c.history {
		switch evt.Type {
		case agui.EventTextMessageStart:
			if evt.MessageID == "" {
				continue
			}
			if _, ok := idx[evt.MessageID]; !ok {
				idx[evt.MessageID] = len(out)
				role := evt.Role
				if role == "" {
					role = "assistant"
				}
				out = append(out, Message{ID: evt.MessageID, Role: role})
			}
		case agui.EventTextMessageContent:
			if i, ok := idx[evt.MessageID]; ok {
				out[i].Content += evt.Delta
			}
		}
	}
	return out
}

// Bind привязывает к клиенту доставку событий (sink) и обработку разрешений (resolver)
// на время одного WS-сеанса. Возвращает unbind, который снимает привязку (события снова
// отбрасываются, разрешения трактуются как cancelled). Повторный Bind поверх активного
// заменяет привязку — последнее подключение «выигрывает».
func (c *Client) Bind(sink EventSink, resolver PermissionResolver) (unbind func()) {
	c.mu.Lock()
	c.bindGen++
	gen := c.bindGen
	c.sink = sink
	c.resolver = resolver
	lastUsage := c.lastUsage
	lastCommands := c.lastCommands
	// Снимок открытых потоковых сообщений и tool call'ов: если turn ещё идёт, их START
	// ушёл в прежний sink. Новому потоку нужно переоткрыть контекст, иначе последующий
	// CONTENT/END прилетит без START и клиент @ag-ui отвергнет событие.
	openText := c.stream.textID
	openThought := c.stream.thoughtID
	type openTool struct{ id, name string }
	var openTools []openTool
	for id, st := range c.toolCalls {
		if st.open {
			openTools = append(openTools, openTool{id: id, name: st.name})
		}
	}
	c.mu.Unlock()

	// Историю чата в SSE-поток НЕ проигрываем: лента восстанавливается отдельным запросом
	// GET /api/ag-ui/history (ThreadHistoryAdapter.load массивом с корректными ролями).
	// Иначе каждый живой turn повторно проигрывал бы всю накопленную ленту, а агрегатор
	// @ag-ui/react-ag-ui склеил бы её с новым ответом в кашу. В sink уходят только
	// актуальные снимки usage/commands (это состояние, а не история), переоткрытие
	// незакрытых потоков и живые события текущего turn'а.
	if lastCommands != nil {
		if sink(*lastCommands) != nil {
			lastUsage = nil // мёртвый sink — дальше слать нет смысла
		}
	}
	if lastUsage != nil {
		_ = sink(*lastUsage)
	}
	// Переоткрываем незакрытые потоки и tool call'ы в новом sink (их START уже был в
	// прежнем потоке). Лог оставлен намеренно: переоткрытие текстового потока в run с
	// НОВЫМ промптом — кандидат в причины сбоя порядка сообщений на клиенте
	// (агрегатор принимает первый messageId за id ответа текущего turn'а).
	if openText != "" {
		log.Printf("acp: bind reopens open text stream %s", openText)
		_ = sink(agui.Event{Type: agui.EventTextMessageStart, MessageID: openText, Role: "assistant"})
	}
	if openThought != "" {
		_ = sink(agui.Event{Type: agui.EventReasoningStart, MessageID: openThought})
		_ = sink(agui.Event{Type: agui.EventReasoningMessageStart, MessageID: openThought, Role: "reasoning"})
	}
	for _, tc := range openTools {
		_ = sink(agui.Event{Type: agui.EventToolCallStart, ToolCallID: tc.id, ToolCallName: tc.name})
	}

	return func() {
		c.mu.Lock()
		// Снимаем привязку только если она всё ещё наша: иначе unbind старого сеанса
		// затёр бы привязку нового, успевшего перепривязаться.
		if c.bindGen == gen {
			c.sink = nil
			c.resolver = nil
		}
		c.mu.Unlock()
	}
}

// emit доставляет событие текущему привязанному sink и сохраняет его в ленте сессии
// для воспроизведения при reconnect (см. Bind). Вне WS-сеанса доставка пропускается,
// но событие всё равно копится в истории: turn агента доходит до конца и сохраняется
// независимо от того, подключён ли сейчас клиент.
//
// Permission-запросы в историю не пишутся (повторный показ после ответа некорректен);
// usage-обновления хранятся отдельным последним значением (см. lastUsage).
func (c *Client) emit(evt agui.Event) {
	c.mu.Lock()
	switch {
	case evt.Type == agui.EventPermissionRequest:
		// Не сохраняется: повторный показ после ответа некорректен.
	case evt.Type == agui.EventCustom && evt.Name == agui.CustomUsageName:
		// Расход контекста хранится отдельно: важно только последнее значение, а не вся
		// высокочастотная последовательность usage-обновлений (см. lastUsage).
		u := evt
		c.lastUsage = &u
	case evt.Type == agui.EventCustom && evt.Name == agui.CustomCommandsName:
		// Список команд — снимок: храним только последний, как и usage (см. lastCommands).
		cm := evt
		c.lastCommands = &cm
	default:
		c.appendHistoryLocked(evt)
	}
	sink := c.sink
	c.mu.Unlock()
	if sink == nil {
		return
	}
	_ = sink(evt)
}

// appendHistoryLocked добавляет событие в ленту с соблюдением historyCap. Вызывается под
// c.mu.
func (c *Client) appendHistoryLocked(evt agui.Event) {
	c.history = append(c.history, evt)
	if len(c.history) > historyCap {
		c.history = c.history[len(c.history)-historyCap:]
	}
}

// recordUserMessage кладёт реплику пользователя в ленту истории как самодостаточную
// тройку TEXT_MESSAGE_START/CONTENT/END (role=user), НЕ доставляя её в живой sink: на
// живом turn'е фронт рисует сообщение оптимистично, дубль был бы лишним. Нужна, чтобы
// GET /api/ag-ui/history (acp.Client.Messages) возвращал реплики пользователя текущего
// процесса. Вызывается под c.mu.
func (c *Client) recordUserMessage(text string) {
	id := uuid.NewString()
	c.appendHistoryLocked(agui.Event{Type: agui.EventTextMessageStart, MessageID: id, Role: "user"})
	c.appendHistoryLocked(agui.Event{Type: agui.EventTextMessageContent, MessageID: id, Delta: text})
	c.appendHistoryLocked(agui.Event{Type: agui.EventTextMessageEnd, MessageID: id})
}

// SetFrontendTools обновляет реестр кастомных сниппетов. Вызывается транспортом при
// получении {type:"frontend_tools"}. Тулы применяются к следующему Prompt.
func (c *Client) SetFrontendTools(tools []FrontendTool) {
	c.mu.Lock()
	c.frontendTools = tools
	c.mu.Unlock()
}

// Prompt отправляет агенту пользовательский ввод и блокируется до конца turn'а.
// Реестр frontend-tools пробрасывается агенту через _meta запроса: adapter добавляет
// их в доступные агенту tools, после чего tool_use по ним приходит обратно как
// tool_call → транслируется в AG-UI TOOL_CALL_* → фронт рендерит свой компонент.
//
// События turn'а (текст, tool calls, план) доставляются асинхронно через SessionUpdate
// в привязанный sink. Lifecycle прогона (RUN_STARTED/RUN_FINISHED/RUN_ERROR) эмитит не
// клиент, а транспорт: эти события несут threadId/runId конкретного запроса, известные
// только ему (см. internal/transport/agui). Возвращаемый stopReason транспорт кладёт в
// RUN_FINISHED.result.
func (c *Client) Prompt(ctx context.Context, text string) (stopReason string, err error) {
	// Сериализуем turn'ы: пока идёт один Prompt, следующий ждёт. Иначе потоковые события
	// двух turn'ов смешались бы в общем sink (см. promptMu).
	c.promptMu.Lock()
	defer c.promptMu.Unlock()

	c.mu.Lock()
	tools := c.frontendTools
	c.promptActive = true
	// Записываем пользовательскую реплику в историю (без доставки в живой sink: фронт
	// уже отрисовал её оптимистично при отправке). Без этого user-сообщения текущего
	// процесса не попадали бы в GET /api/ag-ui/history — их эмитит лишь session/load при
	// рестарте, и при reload в рамках живого процесса лента теряла бы реплики пользователя.
	c.recordUserMessage(text)
	c.mu.Unlock()
	// Страховка от паники в conn.Prompt: залипший promptActive подавлял бы трансляцию
	// user-сообщений всех последующих turn'ов. Штатный сброс ниже остаётся (повторный
	// сброс безвреден), defer покрывает только аварийный выход.
	defer func() {
		c.mu.Lock()
		c.promptActive = false
		c.mu.Unlock()
	}()

	req := acpsdk.PromptRequest{
		SessionId: c.sessionID,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock(text)},
	}
	// Контракт frontend-tools пробрасывается в зарезервированном _meta: имена и
	// JSON Schema компонентов фронта. Adapter трактует их как дополнительно доступные
	// агенту инструменты. Поле _meta — штатная точка расширения ACP.
	if len(tools) > 0 {
		req.Meta = map[string]any{"brigade.frontendTools": tools}
	}

	resp, err := c.conn.Prompt(ctx, req)

	// Закрываем потоковые сообщения и tool call'ы, оставшиеся открытыми к концу turn'а
	// (без терминального статуса от агента), чтобы клиент снял индикаторы выполнения.
	c.mu.Lock()
	c.promptActive = false
	closing := c.finishStreams()
	closing = append(closing, c.closeOpenToolCalls()...)
	c.mu.Unlock()
	for _, evt := range closing {
		c.emit(evt)
	}

	if err != nil {
		return "", fmt.Errorf("acp: prompt: %w", err)
	}
	return string(resp.StopReason), nil
}

// FinishStreams закрывает открытые потоковые сообщения (TEXT_MESSAGE_END /
// REASONING_*_END) и доставляет их через emit (в sink и в history). Нужен перед
// завершением replay-прогона: история, восстановленная через session/load, может
// оканчиваться незакрытым потоковым сообщением (load не идёт через Prompt и не вызывает
// finishStreams), а RUN_FINISHED поверх открытого сообщения клиент отвергает.
// Идемпотентен: при отсутствии открытых потоков finishStreams вернёт пусто и метод
// ничего не сделает.
func (c *Client) FinishStreams() {
	c.mu.Lock()
	closing := c.finishStreams()
	closing = append(closing, c.closeOpenToolCalls()...)
	c.mu.Unlock()
	// emit берёт c.mu внутри — вызываем строго после Unlock, иначе deadlock.
	for _, evt := range closing {
		c.emit(evt)
	}
}

// Close штатно завершает ACP-сессию и процесс adapter'а. Последовательность
// (по нарастанию жёсткости): session/close агенту (если он это поддерживает) → закрытие
// stdin (adapter получает EOF и выходит сам) → ожидание выхода в пределах бюджета →
// Signal (SIGTERM/graceful stop) → Kill. В конце обязателен Wait для reap процесса
// (иначе остаётся зомби) и освобождения ресурсов. Идемпотентна (closeOnce).
func (c *Client) Close() error {
	if c.proc == nil {
		return nil
	}

	c.closeOnce.Do(func() {
		// 1. Просим агента штатно закрыть сессию, если он умеет session/close. Короткий
		//    таймаут: это вежливая попытка, неуспех не критичен (дальше закрываем stdin).
		if c.agentCaps.SessionCapabilities.Close != nil && c.sessionID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), gracefulCloseTimeout/2)
			if _, err := c.conn.CloseSession(ctx, acpsdk.CloseSessionRequest{SessionId: c.sessionID}); err != nil {
				log.Printf("acp: close session %s: %v", c.sessionID, err)
			}
			cancel()
		}

		// 2. Закрываем stdin: adapter получает EOF на управляющем канале и завершается сам.
		_ = c.proc.Stdin().Close()

		// 3. Реапим процесс в фоне и ждём завершения в пределах бюджета. exited
		//    закрывается, как только Wait вернулся — независимо от причины выхода.
		exited := make(chan struct{})
		go func() {
			_ = c.proc.Wait()
			close(exited)
		}()

		select {
		case <-exited:
			return
		case <-time.After(gracefulCloseTimeout):
		}

		// 4. Не вышел сам — просим завершиться и ждём ещё короткий интервал.
		_ = c.proc.Signal()
		select {
		case <-exited:
			return
		case <-time.After(2 * time.Second):
		}

		// 5. Последняя мера — жёсткое убийство. Wait из горутины выше реапит процесс.
		_ = c.proc.Kill()
		<-exited
	})
	return nil
}

// Done закрывается, когда adapter отключился (subprocess завершился). Транспорт может
// слушать этот канал, чтобы закрыть WS при падении агента.
func (c *Client) Done() <-chan struct{} { return c.conn.Done() }

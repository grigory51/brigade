package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
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
// {name:"permission_request"} и ждёт ответа Connect-методом AcpService.ResolvePermission.
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
	// AdapterCommand — имя/путь бинаря ACP-адаптера для локального спавна (agent-agnostic).
	// Пусто — дефолт claude-agent-acp. Используется демоном для запуска произвольного
	// ACP-агента внутри контейнера.
	AdapterCommand string
	// ExtraEnv — дополнительные переменные окружения агента ("KEY=VALUE").
	// Используется локальным subprocess'ом (spawnLocalProc); контейнерный процесс
	// получает окружение через spawn.Spec.Env.
	ExtraEnv []string
	// McpServers — статические MCP-серверы сессии (session/new mcpServers). brigade
	// кладёт сюда свой MCP-сервер кастомных UI-инструментов (см. BrigadeMCPServer):
	// это единственный канал, которым модель получает эти тулы (сток-адаптер игнорирует
	// _meta). Пусто — модель их не видит. Задаётся только в docker-режиме (путь сервера —
	// внутри образа агента), см. registry.applyACPSpawnMode.
	McpServers []acpsdk.McpServer
	// PluginDirs — локальные плагины сессии (абсолютные пути внутри контейнера/хоста).
	// Пробрасываются агенту через _meta.claudeCode.options.plugins ([{type:"local",path}]) —
	// единственный канал загрузки плагина в Agent SDK (settings.json enabledPlugins SDK не
	// читает, это CLI-концепт). brigade кладёт сюда per-session плагин brigade (skill preview,
	// namespace /brigade:preview). Задаётся в docker-режиме при включённом preview.
	PluginDirs []string
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
	// по которому AcpService.GetHistory восстанавливает ленту массивом сообщений.
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
	// configOptions — текущие конфигурационные опции сессии (модель, режим прав,
	// усилие): снимок из session/new|load|fork, обновляется ConfigOptionUpdate и
	// ответами session/set_config_option. Отдаётся фронту вместе с историей
	// (history-endpoint); изменение — SetConfigOption. Доступ — под mu.
	configOptions []acpsdk.SessionConfigOption
	// promptActive — true, пока выполняется живой Prompt. В этом окне трансляцию
	// user_message_chunk подавляем, чтобы не дублировать оптимистичное сообщение фронта
	// (см. translate.emitUserMessage). Доступ — под mu.
	promptActive bool
	// lastActivityAt — время последнего содержательного события агента (текст/размышление/
	// tool call). Служит для детекта «агент генерирует» вне живого Prompt: фоновый turn
	// (agent wakeup после завершения Workflow/задачи) стримит session/update без активного
	// /run, и его нельзя отследить через promptActive. Идёт в Status() c дебаунс-окном.
	// Доступ — под mu.
	lastActivityAt time.Time
	// bindGen — номер текущей привязки. Увеличивается при каждом Bind; unbind снимает
	// привязку только если её номер ещё актуален, чтобы закрытие старого сеанса не
	// затёрло привязку нового.
	bindGen uint64
	// stream отслеживает открытые потоковые сообщения (текст/размышление) для
	// расстановки START/END вокруг чанков по смене messageId. Доступ — под mu.
	stream streamState
	// turnMsgID — messageId первого ассистентского сообщения текущего turn'а. Все
	// tool call'ы turn'а получают его как parentMessageId (TOOL_CALL_START), чтобы
	// клиентский агрегатор собрал их в единый блок «N tool calls» (см. translate.go и
	// agui.Event.ParentMessageID). Сбрасывается в начале каждого turn'а (Prompt); пусто,
	// пока в turn'е не появилось ни одного ассистентского сообщения (тогда вызовы
	// группируются по смежности). Доступ — под mu.
	turnMsgID string
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
			McpServers: toUnstableMcpServers(c.opts.McpServers),
			Meta:       pluginsMeta(c.opts.PluginDirs),
		})
		if err != nil {
			return fmt.Errorf("acp: fork session %s: %w", c.opts.ForkFromSessionID, err)
		}
		c.sessionID = forkResp.SessionId
		// Unstable-вариант типа опций структурно идентичен стабильному (одинаковый
		// wire-формат) — конвертируем через JSON, чтобы не дублировать union-логику.
		c.configOptions = convertConfigOptions(forkResp.ConfigOptions)
		return nil
	}

	// Resume через session/load: агент реплеит прошлый thread нотификациями
	// session/update, которые наполняют историю для воспроизведения в Bind. Id при
	// load известен заранее, новый не выдаётся. Ранний return — только при успехе;
	// любая ошибка load (в т.ч. resourceNotFound на устаревший/неизвестный id) не
	// валит восстановление: логируем и проваливаемся в NewSession ниже.
	if c.opts.ResumeSessionID != "" && initResp.AgentCapabilities.LoadSession {
		if loadResp, err := c.conn.LoadSession(ctx, acpsdk.LoadSessionRequest{
			SessionId:  acpsdk.SessionId(c.opts.ResumeSessionID),
			Cwd:        c.opts.Cwd,
			McpServers: mcpServersOrEmpty(c.opts.McpServers),
			Meta:       pluginsMeta(c.opts.PluginDirs),
		}); err != nil {
			log.Printf("acp: load session %s failed (%v), starting fresh session", c.opts.ResumeSessionID, err)
		} else {
			c.sessionID = acpsdk.SessionId(c.opts.ResumeSessionID)
			c.configOptions = loadResp.ConfigOptions
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
		McpServers: mcpServersOrEmpty(c.opts.McpServers),
		Meta:       pluginsMeta(c.opts.PluginDirs),
	})
	if err != nil {
		return fmt.Errorf("acp: new session: %w", err)
	}
	c.sessionID = newSess.SessionId
	c.configOptions = newSess.ConfigOptions
	return nil
}

// convertConfigOptions приводит unstable-вариант опций (session/fork) к стабильному
// типу через JSON: wire-формат обоих идентичен.
func convertConfigOptions(in []acpsdk.UnstableSessionConfigOption) []acpsdk.SessionConfigOption {
	if len(in) == 0 {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var out []acpsdk.SessionConfigOption
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// ConfigOptions возвращает текущие конфигурационные опции сессии (модель, режим
// прав, усилие). Отдаётся фронту history-endpoint'ом вместе с историей и командами.
func (c *Client) ConfigOptions() []acpsdk.SessionConfigOption {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]acpsdk.SessionConfigOption, len(c.configOptions))
	copy(out, c.configOptions)
	return out
}

// SetConfigOption устанавливает значение конфигурационной опции сессии
// (session/set_config_option) и возвращает актуальный полный набор опций из ответа
// агента.
func (c *Client) SetConfigOption(ctx context.Context, configID, value string) ([]acpsdk.SessionConfigOption, error) {
	resp, err := c.conn.SetSessionConfigOption(ctx, acpsdk.SetSessionConfigOptionRequest{
		ValueId: &acpsdk.SetSessionConfigOptionValueId{
			SessionId: c.sessionID,
			ConfigId:  acpsdk.SessionConfigId(configID),
			Value:     acpsdk.SessionConfigValueId(value),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("acp: set config option %s=%s: %w", configID, value, err)
	}

	c.mu.Lock()
	c.configOptions = resp.ConfigOptions
	c.mu.Unlock()
	return c.ConfigOptions(), nil
}

// SessionID возвращает идентификатор ACP-сессии агента. Сохраняется в store как
// agent_session_id для последующего resume.
func (c *Client) SessionID() string { return string(c.sessionID) }

// Message — одно сообщение чата для восстановления истории на клиенте (ThreadHistory).
type Message struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	Content string `json:"content"`
	// Поля карточки инструмента (Role == "tool_call"): без них tool-карточки исчезали
	// из ленты при любом восстановлении истории (reload после фонового turn'а, рестарт
	// бэкенда) — /history отдавал только текст. ArgsText — сырой JSON аргументов,
	// Result — итоговый вывод вызова.
	ToolName string `json:"toolName,omitempty"`
	ArgsText string `json:"argsText,omitempty"`
	Result   string `json:"result,omitempty"`
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
	idx := make(map[string]int)     // messageId → позиция в out (текстовые сообщения)
	toolIdx := make(map[string]int) // toolCallId → позиция в out (карточки инструментов)
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
		case agui.EventToolCallStart:
			if evt.ToolCallID == "" {
				continue
			}
			if _, ok := toolIdx[evt.ToolCallID]; !ok {
				toolIdx[evt.ToolCallID] = len(out)
				out = append(out, Message{
					ID:       evt.ToolCallID,
					Role:     "tool_call",
					ToolName: evt.ToolCallName,
				})
			}
		case agui.EventToolCallArgs:
			if i, ok := toolIdx[evt.ToolCallID]; ok {
				out[i].ArgsText += evt.Delta
			}
		case agui.EventToolCallResult:
			if i, ok := toolIdx[evt.ToolCallID]; ok {
				out[i].Result = evt.Content
			}
		}
	}
	return out
}

// backgroundIdleWindow — окно тишины, после которого фоновый turn считается
// завершённым. Turn от wakeup (после завершения Workflow/фоновой задачи) не имеет
// сигнала окончания, в отличие от Prompt со stopReason, поэтому «idle» выводится по
// отсутствию новых содержательных событий в этом окне.
const backgroundIdleWindow = 4 * time.Second

// Status сообщает, генерирует ли агент сейчас, и монотонный seq ленты. generating =
// идёт живой Prompt ИЛИ недавно (в пределах backgroundIdleWindow) приходили
// содержательные события фонового turn'а. seq — число событий в history: растёт при
// каждом новом содержательном событии, что позволяет фронту заметить появление
// фонового turn'а, не разбирая сами события (по изменению seq — перечитать историю).
func (c *Client) Status() (generating bool, seq int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	generating = c.promptActive ||
		(!c.lastActivityAt.IsZero() && time.Since(c.lastActivityAt) < backgroundIdleWindow)
	return generating, len(c.history)
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
	// AcpService.GetHistory (ThreadHistoryAdapter.load массивом с корректными ролями).
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

// ReopenEvents возвращает START-события для открытых сейчас потоковых сообщений и tool
// call'ов. Нужны новому подписчику, подключившемуся ПОСРЕДИ turn'а (reconnect через демон):
// без переоткрытия последующие CONTENT/END пришли бы без START и клиент @ag-ui отверг бы их.
// Зеркалит логику переоткрытия в Bind (для локального acp.Client это делает сам Bind).
func (c *Client) ReopenEvents() []agui.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []agui.Event
	if c.stream.textID != "" {
		out = append(out, agui.Event{Type: agui.EventTextMessageStart, MessageID: c.stream.textID, Role: "assistant"})
	}
	if c.stream.thoughtID != "" {
		out = append(out,
			agui.Event{Type: agui.EventReasoningStart, MessageID: c.stream.thoughtID},
			agui.Event{Type: agui.EventReasoningMessageStart, MessageID: c.stream.thoughtID, Role: "reasoning"})
	}
	for id, st := range c.toolCalls {
		if st.open {
			out = append(out, agui.Event{Type: agui.EventToolCallStart, ToolCallID: id, ToolCallName: st.name})
		}
	}
	return out
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
		// Отмечаем содержательную активность агента для Status(): по ней детектируется
		// фоновый turn без активного Prompt. Служебные снимки (usage/commands) и
		// permission-запросы сюда не попадают (обработаны выше), поэтому индикатор
		// «работает» не залипает на высокочастотных usage-обновлениях.
		c.lastActivityAt = time.Now()
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
// AcpService.GetHistory (acp.Client.Messages) возвращал реплики пользователя текущего
// процесса. Вызывается под c.mu.
func (c *Client) recordUserMessage(text string) {
	id := uuid.NewString()
	c.appendHistoryLocked(agui.Event{Type: agui.EventTextMessageStart, MessageID: id, Role: "user"})
	c.appendHistoryLocked(agui.Event{Type: agui.EventTextMessageContent, MessageID: id, Delta: text})
	c.appendHistoryLocked(agui.Event{Type: agui.EventTextMessageEnd, MessageID: id})
}

// Prompt отправляет агенту пользовательский ввод и блокируется до конца turn'а.
// Кастомные UI-инструменты (render_ui, show_choice) агент получает не отсюда, а через
// MCP-сервер сессии (см. acp.BrigadeMCPServer); их tool_use приходит обратно как
// tool_call → транслируется в AG-UI TOOL_CALL_* → фронт рендерит компонент.
//
// События turn'а (текст, tool calls, план) доставляются асинхронно через SessionUpdate
// в привязанный sink. Lifecycle прогона (RUN_STARTED/RUN_FINISHED/RUN_ERROR) эмитит не
// клиент, а транспорт: эти события несут threadId/runId конкретного запроса, известные
// только ему (см. internal/transport/agui). Возвращаемый stopReason транспорт кладёт в
// RUN_FINISHED.result.
//
// onTurnStart (может быть nil) вызывается ровно один раз ПОСЛЕ захвата turn-барьера
// (promptMu) и записи реплики пользователя, но ДО отправки запроса агенту. К этому
// моменту предыдущий turn полностью завершён (его отложенный cleanup закрыл потоки),
// поэтому хук — безопасная точка привязать sink нового прогона: события предыдущего
// turn'а физически не могут прийти после освобождения им promptMu, а значит не попадут
// в sink этого прогона (структурная защита от слипания ответов двух turn'ов).
// WriteFile кладёт content в рабочую директорию агента по относительному пути rel (напр.
// uploads/<имя>). Часть фасада сессии: brigade заливает вложения через него, не завязываясь
// на среду (docker и т.п.) — здесь запись идёт в локальную ФС относительно cwd адаптера
// (в docker-режиме этот же метод исполняется внутри контейнера демоном). ctx не используется.
func (c *Client) WriteFile(_ context.Context, rel string, content []byte) error {
	abs := filepath.Join(c.opts.Cwd, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("acp: mkdir upload dir: %w", err)
	}
	if err := os.WriteFile(abs, content, 0o644); err != nil {
		return fmt.Errorf("acp: write file %s: %w", rel, err)
	}
	return nil
}

func (c *Client) Prompt(ctx context.Context, text string, onTurnStart func()) (stopReason string, err error) {
	// Сериализуем turn'ы: пока идёт один Prompt, следующий ждёт. Иначе потоковые события
	// двух turn'ов смешались бы в общем sink (см. promptMu).
	c.promptMu.Lock()
	defer c.promptMu.Unlock()

	c.mu.Lock()
	c.promptActive = true
	// Новый turn — сбрасываем якорь группировки tool call'ов: его задаст первое
	// ассистентское сообщение этого turn'а (см. translate.go, turnMsgID).
	c.turnMsgID = ""
	// Записываем пользовательскую реплику в историю (без доставки в живой sink: фронт
	// уже отрисовал её оптимистично при отправке). Без этого user-сообщения текущего
	// процесса не попадали бы в AcpService.GetHistory — их эмитит лишь session/load при
	// рестарте, и при reload в рамках живого процесса лента теряла бы реплики пользователя.
	c.recordUserMessage(text)
	c.mu.Unlock()

	// Барьер привязки sink: под удержанным promptMu предыдущий turn гарантированно
	// завершён, поэтому здесь транспорт привязывает поток текущего прогона (см. коммент
	// выше и transport/agui/run.go).
	if onTurnStart != nil {
		onTurnStart()
	}
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

	// Сбрасываем метку активности после закрывающих emit'ов: события foreground-turn'а
	// (в т.ч. закрывающие END выше) обновляют lastActivityAt, и без сброса generating
	// держался бы ещё backgroundIdleWindow после возврата Prompt — фронт ложно принял бы
	// хвост завершённого прогона за фоновую работу. Активность живого turn'а атрибутируется
	// promptActive, поэтому по его завершении обнуляем окно; фоновый turn (без Prompt)
	// выставит метку заново своим первым событием. Turn'ы не пересекаются (promptMu).
	c.mu.Lock()
	c.lastActivityAt = time.Time{}
	c.mu.Unlock()

	if err != nil {
		return "", fmt.Errorf("acp: prompt: %w", err)
	}
	return string(resp.StopReason), nil
}

// Summarize запускает служебный turn с промптом prompt и возвращает собранный текст ответа
// ассистента, не выпуская потоковые события наружу (привязывает собственный sink-сборщик).
// Используется для recap сессии при архивации: агент суммирует диалог из своего контекста.
// Инструменты в этом turn'е недоступны (resolver nil → permission cancelled) — ожидается
// только текст. Держит turn-барьер через Prompt, поэтому сериализуется с обычными turn'ами;
// временно перехватывает sink (архивация — намеренное действие, не во время живого turn'а).
func (c *Client) Summarize(ctx context.Context, prompt string) (string, error) {
	var mu sync.Mutex
	var buf strings.Builder
	sink := func(evt agui.Event) error {
		if evt.Type == agui.EventTextMessageContent {
			mu.Lock()
			buf.WriteString(evt.Delta)
			mu.Unlock()
		}
		return nil
	}
	unbind := c.Bind(sink, nil)
	defer unbind()
	if _, err := c.Prompt(ctx, prompt, nil); err != nil {
		return "", err
	}
	mu.Lock()
	defer mu.Unlock()
	return strings.TrimSpace(buf.String()), nil
}

// Cancel просит агента отменить текущий turn — отправляет протокольное уведомление
// session/cancel (fire-and-forget, без ответа). Идемпотентно и безопасно без активного
// turn'а: агент по ACP-контракту игнорирует отмену неактивной сессии, повторный вызов
// тоже безвреден. Намеренно НЕ трогает promptMu и НЕ отменяет ctx выполняющегося Prompt:
// turn сворачивается кооперативно (агент доводит его до stopReason=cancelled и присылает
// финальный ответ), поэтому весь хвост событий turn'а приходит, пока Prompt ещё держит
// барьер, и не может утечь в следующий прогон. Работает и для фонового wakeup-turn
// (registry.go), который запущен на неотменяемом контексте. Пустой sessionID — no-op.
func (c *Client) Cancel(ctx context.Context) error {
	if c.sessionID == "" {
		return nil
	}
	return c.conn.Cancel(ctx, acpsdk.CancelNotification{SessionId: c.sessionID})
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

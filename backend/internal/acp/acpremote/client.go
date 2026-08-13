// Package acpremote — тонкий клиент brigade к durable ACP-демону (internal/acpdaemon),
// реализующий тот же интерфейс, что и локальный acp.Client (transport/agui.Bindable).
//
// brigade больше не владеет ACP-адаптером напрямую: адаптер живёт в демоне (pid1 контейнера
// сессии), а brigade — реплеебл-клиент. Bind открывает server-streaming StreamEvents и льёт
// события в sink; Prompt/Cancel/Status/… — unary RPC. При рестарте brigade объект
// пересоздаётся и переподписывается — turn в демоне не прерывается.
//
// Транспорт — Connect по docker-сети; авторизация — подпись brigade (asymmetric: приватный
// ключ у brigade, публичный — в env демона), токен подписывается на каждый вызов.
package acpremote

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"sync"
	"time"

	"connectrpc.com/connect"
	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/acp"
	"github.com/grigory51/brigade/backend/internal/agui"
	"github.com/grigory51/brigade/backend/internal/daemonrpc"
)

// Client — brigade-сторона ACP-сессии поверх демона. Реализует transport/agui.Bindable.
type Client struct {
	// Conn — транспорт к демону с подписью вызовов (asymmetric-auth: brigade подписывает
	// приватным ключом, демон проверяет публичным из env).
	daemonrpc.Conn

	mu        sync.Mutex
	sessionID string
	sink      acp.EventSink
	bindGen   uint64
	cancel    context.CancelFunc // отменяет текущий StreamEvents-цикл
	// deliveredSeq — seq последнего события, доставленного текущему sink (streamLoop). По нему
	// FinishStreams дожидается, что закрывающие потоки события доехали до SSE до RUN_FINISHED.
	deliveredSeq int64
	// pendingPermissions — последний снимок ожиданий durable-демона из Status.
	// GetStatus фронта читает его без второго RPC.
	pendingPermissions [][]byte
	// promptMu сериализует turn'ы, как acp.Client.promptMu: brigade допускает параллельные
	// /run в один тред, а привязка sink нового прогона (onTurnStart) должна происходить
	// строго между turn'ами.
	promptMu sync.Mutex

	// OnTurnEnd (может быть nil) вызывается по завершении каждого Prompt со stopReason и
	// ошибкой; реестр вешает сюда push-уведомление (internal/notify). Summarize идёт отдельным
	// RPC мимо Prompt, поэтому recap-архивации уведомление не шлёт. Ставится до первого Prompt.
	OnTurnEnd      func(stopReason string, err error)
	OnSessionTitle func(title string)
	sessionTitle   string
}

// New создаёт клиент к демону по baseURL (http://<host>:<port>). signToken подписывает токен
// на каждый вызов (asymmetric-auth); передаётся реестром (замыкает preview.DaemonToken по id).
func New(baseURL, sessionID string, signToken func() (string, error)) *Client {
	return &Client{
		Conn:      daemonrpc.Dial(baseURL, "acpremote", signToken),
		sessionID: sessionID,
	}
}

// ConfigureOptions — параметры Configure (спавн адаптера в демоне).
type ConfigureOptions struct {
	OAuthToken      string
	ExtraEnv        []string
	AdapterCommand  string
	Cwd             string
	ResumeSessionID string
	PluginDirs      []string
	McpServers      []acpsdk.McpServer
	SystemPrompt    string
	CredentialFile  string
}

// Configure просит демон (пере)поднять адаптер (секреты — здесь, не в env контейнера).
// Возвращает ACP session id (brigade персистит как resume-поле).
func (c *Client) Configure(ctx context.Context, opts ConfigureOptions) (string, error) {
	var mcpJSON []byte
	if len(opts.McpServers) > 0 {
		mcpJSON, _ = json.Marshal(opts.McpServers)
	}
	resp, err := c.RPC.Configure(ctx, daemonrpc.Req(c.Sign(), &v1.DaemonConfigureRequest{
		OauthToken:      opts.OAuthToken,
		ExtraEnv:        opts.ExtraEnv,
		AdapterCommand:  opts.AdapterCommand,
		Cwd:             opts.Cwd,
		ResumeSessionId: opts.ResumeSessionID,
		PluginDirs:      opts.PluginDirs,
		McpServersJson:  mcpJSON,
		SystemPrompt:    opts.SystemPrompt,
		CredentialFile:  opts.CredentialFile,
	}))
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.sessionID = resp.Msg.SessionId
	c.sessionTitle = resp.Msg.SessionTitle
	c.mu.Unlock()
	if resp.Msg.SessionTitle != "" && c.OnSessionTitle != nil {
		c.OnSessionTitle(resp.Msg.SessionTitle)
	}
	return resp.Msg.SessionId, nil
}

// SetSSHKey загружает приватный ключ пользователя в ssh-agent демона. Ключ уходит по
// защищённому каналу RPC и остаётся в памяти демона: на диск среды агента он не пишется.
func (c *Client) SetSSHKey(ctx context.Context, privatePEM string) error {
	_, err := c.RPC.SetSSHKey(ctx, daemonrpc.Req(c.Sign(), &v1.DaemonSetSSHKeyRequest{PrivateKey: privatePEM}))
	return err
}

// SessionID возвращает ACP session id (для persist как agent_session_id).
func (c *Client) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

func (c *Client) SetHooks(onTurnEnd func(string, error), onSessionTitle func(string)) {
	c.mu.Lock()
	c.OnTurnEnd = onTurnEnd
	c.OnSessionTitle = onSessionTitle
	title := c.sessionTitle
	c.mu.Unlock()
	if title != "" && onSessionTitle != nil {
		onSessionTitle(title)
	}
}

// Bind подписывается на поток событий демона и льёт их в sink. from_seq = текущий seq
// демона → живой поток (историю фронт берёт через GetHistory/Messages, как и с acp.Client).
// resolver сейчас не используется: permission-запрос прилетает CUSTOM-событием в потоке
// (демон его журналит), а ответ идёт AcpService.ResolvePermission → демон (Phase 3).
func (c *Client) Bind(sink acp.EventSink, _ acp.PermissionResolver) (unbind func()) {
	c.mu.Lock()
	c.bindGen++
	gen := c.bindGen
	c.sink = sink
	if c.cancel != nil {
		c.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.mu.Unlock()

	from := int64(0)
	if st, err := c.RPC.Status(ctx, daemonrpc.Req(c.Sign(), &v1.Empty{})); err == nil {
		from = st.Msg.Seq
	}
	go c.streamLoop(ctx, gen, from)

	return func() {
		c.mu.Lock()
		if c.bindGen == gen {
			c.sink = nil
			if c.cancel != nil {
				c.cancel()
				c.cancel = nil
			}
		}
		c.mu.Unlock()
	}
}

// streamLoop читает StreamEvents и доставляет каждое событие текущему sink, пока привязка
// актуальна (gen не сменился) и ctx не отменён.
func (c *Client) streamLoop(ctx context.Context, gen uint64, from int64) {
	stream, err := c.RPC.StreamEvents(ctx, daemonrpc.Req(c.Sign(), &v1.DaemonStreamEventsRequest{FromSeq: from}))
	if err != nil {
		return
	}
	for stream.Receive() {
		msg := stream.Msg()
		var evt agui.Event
		if err := json.Unmarshal(msg.AguiJson, &evt); err != nil {
			log.Printf("acpremote: unmarshal event seq=%d: %v", msg.Seq, err)
			continue
		}
		c.mu.Lock()
		cur := c.sink
		curGen := c.bindGen
		c.mu.Unlock()
		if cur == nil || curGen != gen {
			continue
		}
		_ = cur(evt)
		c.mu.Lock()
		c.deliveredSeq = msg.Seq
		c.mu.Unlock()
	}
}

// Prompt гонит turn через демон. onTurnStart (привязка sink нового прогона) вызывается под
// turn-барьером до RPC, как в acp.Client.Prompt.
func (c *Client) Prompt(ctx context.Context, text string, onTurnStart func()) (string, error) {
	return c.prompt(ctx, text, onTurnStart, false)
}

// PromptAutoApprove запускает turn с одноразовыми разрешениями внутри durable-демона.
func (c *Client) PromptAutoApprove(ctx context.Context, text string, onTurnStart func()) (string, error) {
	return c.prompt(ctx, text, onTurnStart, true)
}

func (c *Client) prompt(ctx context.Context, text string, onTurnStart func(), autoAllowOnce bool) (string, error) {
	c.promptMu.Lock()
	defer c.promptMu.Unlock()
	if onTurnStart != nil {
		onTurnStart()
	}
	resp, err := c.RPC.Prompt(ctx, daemonrpc.Req(c.Sign(), &v1.DaemonPromptRequest{Text: text, AutoAllowOnce: autoAllowOnce}))
	stopReason := ""
	if resp != nil {
		stopReason = resp.Msg.StopReason
		if resp.Msg.SessionTitle != "" {
			c.mu.Lock()
			changed := c.sessionTitle != resp.Msg.SessionTitle
			c.sessionTitle = resp.Msg.SessionTitle
			onTitle := c.OnSessionTitle
			c.mu.Unlock()
			if changed && onTitle != nil {
				onTitle(resp.Msg.SessionTitle)
			}
		}
	}
	if c.OnTurnEnd != nil {
		c.OnTurnEnd(stopReason, err)
	}
	if err != nil {
		return "", err
	}
	return stopReason, nil
}

// Cancel → session/cancel в демоне.
func (c *Client) Cancel(ctx context.Context) error {
	_, err := c.RPC.Cancel(ctx, daemonrpc.Req(c.Sign(), &v1.Empty{}))
	return err
}

// FinishStreams закрывает открытые потоки в демоне (журналит TEXT_MESSAGE_END и т.п.) И, если
// поток привязан, дожидается доставки этих закрытий на sink/SSE. Без ожидания — гонка: демон
// журналит END, а он едет на SSE асинхронно через StreamEvents, тогда как RUN_FINISHED run.go
// шлёт напрямую и обгоняет; клиент @ag-ui отвергает RUN_FINISHED «поверх открытого текста».
func (c *Client) FinishStreams() {
	if _, err := c.RPC.FinishStreams(context.Background(), daemonrpc.Req(c.Sign(), &v1.Empty{})); err != nil {
		log.Printf("acpremote: finish streams: %v", err)
		return
	}
	// Дренируем доставку только при активной привязке: без sink (напр. вызов на onTurnStart
	// до Bind) seq не двигается — ждать нечего.
	c.mu.Lock()
	bound := c.sink != nil
	c.mu.Unlock()
	if !bound {
		return
	}
	_, target := c.Status() // журнал демона после закрытия
	c.waitDelivered(int64(target), 2*time.Second)
}

// waitDelivered блокируется, пока streamLoop не доставит события до seq target (или таймаут —
// защита от повисшего/оборванного потока: best-effort, RUN_FINISHED всё равно уйдёт).
func (c *Client) waitDelivered(target int64, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for {
		c.mu.Lock()
		got := c.deliveredSeq
		c.mu.Unlock()
		if got >= target || time.Now().After(deadline) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Messages → проекция истории из демона.
func (c *Client) Messages() []acp.Message {
	resp, err := c.RPC.GetMessages(context.Background(), daemonrpc.Req(c.Sign(), &v1.Empty{}))
	if err != nil {
		return nil
	}
	var out []acp.Message
	_ = json.Unmarshal(resp.Msg.Json, &out)
	return out
}

// Commands → последний список slash-команд из демона.
func (c *Client) Commands() []agui.AvailableCommand {
	resp, err := c.RPC.GetCommands(context.Background(), daemonrpc.Req(c.Sign(), &v1.Empty{}))
	if err != nil {
		return nil
	}
	var out []agui.AvailableCommand
	_ = json.Unmarshal(resp.Msg.Json, &out)
	return out
}

// ConfigOptions → текущие опции сессии из демона.
func (c *Client) ConfigOptions() []acpsdk.SessionConfigOption {
	resp, err := c.RPC.GetConfigOptions(context.Background(), daemonrpc.Req(c.Sign(), &v1.Empty{}))
	if err != nil {
		return nil
	}
	var out []acpsdk.SessionConfigOption
	_ = json.Unmarshal(resp.Msg.Json, &out)
	return out
}

// SetConfigOption → изменение опции сессии в демоне.
func (c *Client) SetConfigOption(ctx context.Context, configID, value string) ([]acpsdk.SessionConfigOption, error) {
	resp, err := c.RPC.SetConfigOption(ctx, daemonrpc.Req(c.Sign(), &v1.DaemonSetConfigOptionRequest{ConfigId: configID, Value: value}))
	if err != nil {
		return nil, err
	}
	var out []acpsdk.SessionConfigOption
	_ = json.Unmarshal(resp.Msg.Json, &out)
	return out, nil
}

// Status → генерация + durable seq журнала демона.
func (c *Client) Status() (generating bool, seq int) {
	resp, err := c.RPC.Status(context.Background(), daemonrpc.Req(c.Sign(), &v1.Empty{}))
	if err != nil {
		return false, 0
	}
	c.mu.Lock()
	c.pendingPermissions = resp.Msg.PendingPermissionsJson
	c.mu.Unlock()
	return resp.Msg.Generating, int(resp.Msg.Seq)
}

// PendingPermissionsJSON возвращает снимок ожиданий из последнего Status RPC.
func (c *Client) PendingPermissionsJSON() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.pendingPermissions...)
}

// ResolvePermission доставляет решение пользователя ожидающему turn'у в демоне (диалог
// пришёл фронту CUSTOM-событием в StreamEvents; ответ идёт сюда через AcpService).
func (c *Client) ResolvePermission(ctx context.Context, id, decision string) error {
	_, err := c.RPC.ResolvePermission(ctx, daemonrpc.Req(c.Sign(), &v1.DaemonResolvePermissionRequest{Id: id, Decision: decision}))
	return err
}

// Summarize → служебный recap-turn в демоне (архивация).
func (c *Client) Summarize(ctx context.Context, prompt string) (string, error) {
	resp, err := c.RPC.Summarize(ctx, daemonrpc.Req(c.Sign(), &v1.DaemonSummarizeRequest{Prompt: prompt}))
	if err != nil {
		return "", err
	}
	return resp.Msg.Text, nil
}

// WriteFile просит демон записать файл в рабочую директорию агента (path — относительно
// cwd). Заливка вложений идёт через фасад, а не через docker-API brigade.
func (c *Client) WriteFile(ctx context.Context, path string, content []byte) error {
	_, err := c.RPC.WriteFile(ctx, daemonrpc.Req(c.Sign(), &v1.DaemonWriteFileRequest{Path: path, Content: content}))
	return err
}

// OpenShell поднимает вспомогательный шелл (bash в pty) внутри контейнера через демон и
// возвращает ShellSession (реализует termws.Shell). Эфемерный: закрытие сессии (Terminate)
// гасит pty в демоне. Через фасад, а не docker-exec — работает независимо от способа спавна.
func (c *Client) OpenShell(cwd string) (*ShellSession, error) {
	id := uuid.NewString()
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := c.RPC.OpenTerminal(ctx, daemonrpc.Req(c.Sign(), &v1.DaemonOpenTerminalRequest{
		Id:      id,
		Cmd:     []string{"/bin/bash"},
		Cwd:     cwd,
		Durable: false,
	}))
	if err != nil {
		cancel()
		return nil, err
	}
	pr, pw := io.Pipe()
	sh := &ShellSession{client: c, id: id, cancel: cancel, pr: pr, pw: pw}
	go sh.pump(stream)
	return sh, nil
}

// ShellSession — brigade-сторона вспомогательного шелла поверх Terminal RPC демона.
// Реализует termws.Shell: Read из потока вывода (через io.Pipe), Write/Resize — unary RPC,
// Terminate — отмена потока (демон гасит эфемерный pty). History пуст (жизнь = подключение).
type ShellSession struct {
	client *Client
	id     string
	cancel context.CancelFunc
	pr     *io.PipeReader
	pw     *io.PipeWriter
}

// pump перекладывает поток вывода терминала в pipe (его читает Read); конец потока → EOF.
func (sh *ShellSession) pump(stream *connect.ServerStreamForClient[v1.DaemonTerminalOutput]) {
	for stream.Receive() {
		if _, err := sh.pw.Write(stream.Msg().Data); err != nil {
			break
		}
	}
	_ = sh.pw.CloseWithError(io.EOF)
}

func (sh *ShellSession) Read(p []byte) (int, error) { return sh.pr.Read(p) }

func (sh *ShellSession) Write(p []byte) (int, error) {
	_, err := sh.client.RPC.TerminalInput(context.Background(), daemonrpc.Req(sh.client.Sign(),
		&v1.DaemonTerminalInputRequest{Id: sh.id, Data: p}))
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (sh *ShellSession) Resize(cols, rows uint16) error {
	_, err := sh.client.RPC.TerminalResize(context.Background(), daemonrpc.Req(sh.client.Sign(),
		&v1.DaemonTerminalResizeRequest{Id: sh.id, Cols: uint32(cols), Rows: uint32(rows)}))
	return err
}

// History пуст: жизнь вспомогательного шелла равна жизни WS-подключения (нечего восстанавливать).
func (sh *ShellSession) History() []byte { return nil }

// Terminate отменяет поток (демон гасит эфемерный pty) и закрывает pipe.
func (sh *ShellSession) Terminate(context.Context) error {
	sh.cancel()
	_ = sh.pw.CloseWithError(io.EOF)
	return nil
}

// Close отцепляет клиента (останавливает StreamEvents-цикл). Демон (и адаптер) при этом
// НЕ гасится — контейнер переживает рестарт brigade; остановка контейнера — отдельно,
// при явном teardown (registry.terminate → docker remove).
func (c *Client) Close() error {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.sink = nil
	c.mu.Unlock()
	return nil
}

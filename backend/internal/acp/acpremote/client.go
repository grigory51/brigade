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
	"net/http"
	"sync"

	"connectrpc.com/connect"
	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/gen/go/brigade/v1/brigadev1connect"
	"github.com/grigory51/brigade/backend/internal/acp"
	"github.com/grigory51/brigade/backend/internal/agui"
)

// Client — brigade-сторона ACP-сессии поверх демона. Реализует transport/agui.Bindable.
type Client struct {
	rpc brigadev1connect.AgentDaemonServiceClient
	// signToken выписывает свежий подписанный токен на каждый вызов (asymmetric-auth: brigade
	// подписывает приватным ключом, демон проверяет публичным из env).
	signToken func() (string, error)

	mu        sync.Mutex
	sessionID string
	sink      acp.EventSink
	bindGen   uint64
	cancel    context.CancelFunc // отменяет текущий StreamEvents-цикл

	// promptMu сериализует turn'ы, как acp.Client.promptMu: brigade допускает параллельные
	// /run в один тред, а привязка sink нового прогона (onTurnStart) должна происходить
	// строго между turn'ами.
	promptMu sync.Mutex
}

// New создаёт клиент к демону по baseURL (http://<host>:<port>). signToken подписывает токен
// на каждый вызов (asymmetric-auth); передаётся реестром (замыкает preview.DaemonToken по id).
func New(baseURL, sessionID string, signToken func() (string, error)) *Client {
	return &Client{
		rpc:       brigadev1connect.NewAgentDaemonServiceClient(http.DefaultClient, baseURL),
		signToken: signToken,
		sessionID: sessionID,
	}
}

// sign выписывает свежий подписанный токен; ошибку логирует и возвращает пустую строку
// (демон отвергнет вызов как Unauthenticated).
func (c *Client) sign() string {
	t, err := c.signToken()
	if err != nil {
		log.Printf("acpremote: sign daemon token: %v", err)
		return ""
	}
	return t
}

// authReq оборачивает сообщение в connect.Request с подписанным токеном в Authorization.
func authReq[T any](token string, msg *T) *connect.Request[T] {
	r := connect.NewRequest(msg)
	if token != "" {
		r.Header().Set("Authorization", "Bearer "+token)
	}
	return r
}

// ConfigureOptions — параметры Configure (спавн адаптера в демоне).
type ConfigureOptions struct {
	OAuthToken        string
	ExtraEnv          []string
	AdapterCommand    string
	Cwd               string
	ResumeSessionID   string
	ForkFromSessionID string
	PluginDirs        []string
	McpServers        []acpsdk.McpServer
}

// Configure просит демон (пере)поднять адаптер (секреты — здесь, не в env контейнера).
// Возвращает ACP session id (brigade персистит как resume-поле).
func (c *Client) Configure(ctx context.Context, opts ConfigureOptions) (string, error) {
	var mcpJSON []byte
	if len(opts.McpServers) > 0 {
		mcpJSON, _ = json.Marshal(opts.McpServers)
	}
	resp, err := c.rpc.Configure(ctx, authReq(c.sign(), &v1.DaemonConfigureRequest{
		OauthToken:        opts.OAuthToken,
		ExtraEnv:          opts.ExtraEnv,
		AdapterCommand:    opts.AdapterCommand,
		Cwd:               opts.Cwd,
		ResumeSessionId:   opts.ResumeSessionID,
		ForkFromSessionId: opts.ForkFromSessionID,
		PluginDirs:        opts.PluginDirs,
		McpServersJson:    mcpJSON,
	}))
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.sessionID = resp.Msg.SessionId
	c.mu.Unlock()
	return resp.Msg.SessionId, nil
}

// SessionID возвращает ACP session id (для persist как agent_session_id).
func (c *Client) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
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
	if st, err := c.rpc.Status(ctx, authReq(c.sign(), &v1.Empty{})); err == nil {
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
	stream, err := c.rpc.StreamEvents(ctx, authReq(c.sign(), &v1.DaemonStreamEventsRequest{FromSeq: from}))
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
	}
}

// Prompt гонит turn через демон. onTurnStart (привязка sink нового прогона) вызывается под
// turn-барьером до RPC, как в acp.Client.Prompt.
func (c *Client) Prompt(ctx context.Context, text string, onTurnStart func()) (string, error) {
	c.promptMu.Lock()
	defer c.promptMu.Unlock()
	if onTurnStart != nil {
		onTurnStart()
	}
	resp, err := c.rpc.Prompt(ctx, authReq(c.sign(), &v1.DaemonPromptRequest{Text: text}))
	if err != nil {
		return "", err
	}
	return resp.Msg.StopReason, nil
}

// Cancel → session/cancel в демоне.
func (c *Client) Cancel(ctx context.Context) error {
	_, err := c.rpc.Cancel(ctx, authReq(c.sign(), &v1.Empty{}))
	return err
}

// FinishStreams → закрытие открытых потоков в демоне.
func (c *Client) FinishStreams() {
	if _, err := c.rpc.FinishStreams(context.Background(), authReq(c.sign(), &v1.Empty{})); err != nil {
		log.Printf("acpremote: finish streams: %v", err)
	}
}

// Messages → проекция истории из демона.
func (c *Client) Messages() []acp.Message {
	resp, err := c.rpc.GetMessages(context.Background(), authReq(c.sign(), &v1.Empty{}))
	if err != nil {
		return nil
	}
	var out []acp.Message
	_ = json.Unmarshal(resp.Msg.Json, &out)
	return out
}

// Commands → последний список slash-команд из демона.
func (c *Client) Commands() []agui.AvailableCommand {
	resp, err := c.rpc.GetCommands(context.Background(), authReq(c.sign(), &v1.Empty{}))
	if err != nil {
		return nil
	}
	var out []agui.AvailableCommand
	_ = json.Unmarshal(resp.Msg.Json, &out)
	return out
}

// ConfigOptions → текущие опции сессии из демона.
func (c *Client) ConfigOptions() []acpsdk.SessionConfigOption {
	resp, err := c.rpc.GetConfigOptions(context.Background(), authReq(c.sign(), &v1.Empty{}))
	if err != nil {
		return nil
	}
	var out []acpsdk.SessionConfigOption
	_ = json.Unmarshal(resp.Msg.Json, &out)
	return out
}

// SetConfigOption → изменение опции сессии в демоне.
func (c *Client) SetConfigOption(ctx context.Context, configID, value string) ([]acpsdk.SessionConfigOption, error) {
	resp, err := c.rpc.SetConfigOption(ctx, authReq(c.sign(), &v1.DaemonSetConfigOptionRequest{ConfigId: configID, Value: value}))
	if err != nil {
		return nil, err
	}
	var out []acpsdk.SessionConfigOption
	_ = json.Unmarshal(resp.Msg.Json, &out)
	return out, nil
}

// Status → генерация + durable seq журнала демона.
func (c *Client) Status() (generating bool, seq int) {
	resp, err := c.rpc.Status(context.Background(), authReq(c.sign(), &v1.Empty{}))
	if err != nil {
		return false, 0
	}
	return resp.Msg.Generating, int(resp.Msg.Seq)
}

// ResolvePermission доставляет решение пользователя ожидающему turn'у в демоне (диалог
// пришёл фронту CUSTOM-событием в StreamEvents; ответ идёт сюда через AcpService).
func (c *Client) ResolvePermission(ctx context.Context, id, decision string) error {
	_, err := c.rpc.ResolvePermission(ctx, authReq(c.sign(), &v1.DaemonResolvePermissionRequest{Id: id, Decision: decision}))
	return err
}

// Summarize → служебный recap-turn в демоне (архивация).
func (c *Client) Summarize(ctx context.Context, prompt string) (string, error) {
	resp, err := c.rpc.Summarize(ctx, authReq(c.sign(), &v1.DaemonSummarizeRequest{Prompt: prompt}))
	if err != nil {
		return "", err
	}
	return resp.Msg.Text, nil
}

// WriteFile просит демон записать файл в рабочую директорию агента (path — относительно
// cwd). Заливка вложений идёт через фасад, а не через docker-API brigade.
func (c *Client) WriteFile(ctx context.Context, path string, content []byte) error {
	_, err := c.rpc.WriteFile(ctx, authReq(c.sign(), &v1.DaemonWriteFileRequest{Path: path, Content: content}))
	return err
}

// OpenShell поднимает вспомогательный шелл (bash в pty) внутри контейнера через демон и
// возвращает ShellSession (реализует termws.Shell). Эфемерный: закрытие сессии (Terminate)
// гасит pty в демоне. Через фасад, а не docker-exec — работает независимо от способа спавна.
func (c *Client) OpenShell(cwd string) (*ShellSession, error) {
	id := uuid.NewString()
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := c.rpc.OpenTerminal(ctx, authReq(c.sign(), &v1.DaemonOpenTerminalRequest{
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
	_, err := sh.client.rpc.TerminalInput(context.Background(), authReq(sh.client.sign(),
		&v1.DaemonTerminalInputRequest{Id: sh.id, Data: p}))
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (sh *ShellSession) Resize(cols, rows uint16) error {
	_, err := sh.client.rpc.TerminalResize(context.Background(), authReq(sh.client.sign(),
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

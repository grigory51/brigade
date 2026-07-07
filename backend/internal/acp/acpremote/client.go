// Package acpremote — тонкий клиент brigade к durable ACP-демону (internal/acpdaemon),
// реализующий тот же интерфейс, что и локальный acp.Client (transport/agui.Bindable).
//
// brigade больше не владеет ACP-адаптером напрямую: адаптер живёт в демоне (pid1 контейнера
// сессии), а brigade — реплеебл-клиент. Bind открывает server-streaming StreamEvents и льёт
// события в sink; Prompt/Cancel/Status/… — unary RPC. При рестарте brigade объект
// пересоздаётся и переподписывается — turn в демоне не прерывается.
//
// Транспорт — Connect по docker-сети; авторизация — per-session daemon-token в заголовке.
package acpremote

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"connectrpc.com/connect"
	acpsdk "github.com/coder/acp-go-sdk"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/gen/go/brigade/v1/brigadev1connect"
	"github.com/grigory51/brigade/backend/internal/acp"
	"github.com/grigory51/brigade/backend/internal/agui"
)

// Client — brigade-сторона ACP-сессии поверх демона. Реализует transport/agui.Bindable.
type Client struct {
	rpc   brigadev1connect.AgentDaemonServiceClient
	token string

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

// New создаёт клиент к демону по baseURL (http://<host>:<port>) с daemon-token'ом.
func New(baseURL, token, sessionID string) *Client {
	return &Client{
		rpc:       brigadev1connect.NewAgentDaemonServiceClient(http.DefaultClient, baseURL),
		token:     token,
		sessionID: sessionID,
	}
}

// authReq оборачивает сообщение в connect.Request с daemon-token'ом в Authorization.
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
	resp, err := c.rpc.Configure(ctx, authReq(c.token, &v1.DaemonConfigureRequest{
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
	if st, err := c.rpc.Status(ctx, authReq(c.token, &v1.Empty{})); err == nil {
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
	stream, err := c.rpc.StreamEvents(ctx, authReq(c.token, &v1.DaemonStreamEventsRequest{FromSeq: from}))
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
	resp, err := c.rpc.Prompt(ctx, authReq(c.token, &v1.DaemonPromptRequest{Text: text}))
	if err != nil {
		return "", err
	}
	return resp.Msg.StopReason, nil
}

// Cancel → session/cancel в демоне.
func (c *Client) Cancel(ctx context.Context) error {
	_, err := c.rpc.Cancel(ctx, authReq(c.token, &v1.Empty{}))
	return err
}

// FinishStreams → закрытие открытых потоков в демоне.
func (c *Client) FinishStreams() {
	if _, err := c.rpc.FinishStreams(context.Background(), authReq(c.token, &v1.Empty{})); err != nil {
		log.Printf("acpremote: finish streams: %v", err)
	}
}

// Messages → проекция истории из демона.
func (c *Client) Messages() []acp.Message {
	resp, err := c.rpc.GetMessages(context.Background(), authReq(c.token, &v1.Empty{}))
	if err != nil {
		return nil
	}
	var out []acp.Message
	_ = json.Unmarshal(resp.Msg.Json, &out)
	return out
}

// Commands → последний список slash-команд из демона.
func (c *Client) Commands() []agui.AvailableCommand {
	resp, err := c.rpc.GetCommands(context.Background(), authReq(c.token, &v1.Empty{}))
	if err != nil {
		return nil
	}
	var out []agui.AvailableCommand
	_ = json.Unmarshal(resp.Msg.Json, &out)
	return out
}

// ConfigOptions → текущие опции сессии из демона.
func (c *Client) ConfigOptions() []acpsdk.SessionConfigOption {
	resp, err := c.rpc.GetConfigOptions(context.Background(), authReq(c.token, &v1.Empty{}))
	if err != nil {
		return nil
	}
	var out []acpsdk.SessionConfigOption
	_ = json.Unmarshal(resp.Msg.Json, &out)
	return out
}

// SetConfigOption → изменение опции сессии в демоне.
func (c *Client) SetConfigOption(ctx context.Context, configID, value string) ([]acpsdk.SessionConfigOption, error) {
	resp, err := c.rpc.SetConfigOption(ctx, authReq(c.token, &v1.DaemonSetConfigOptionRequest{ConfigId: configID, Value: value}))
	if err != nil {
		return nil, err
	}
	var out []acpsdk.SessionConfigOption
	_ = json.Unmarshal(resp.Msg.Json, &out)
	return out, nil
}

// Status → генерация + durable seq журнала демона.
func (c *Client) Status() (generating bool, seq int) {
	resp, err := c.rpc.Status(context.Background(), authReq(c.token, &v1.Empty{}))
	if err != nil {
		return false, 0
	}
	return resp.Msg.Generating, int(resp.Msg.Seq)
}

// ResolvePermission доставляет решение пользователя ожидающему turn'у в демоне (диалог
// пришёл фронту CUSTOM-событием в StreamEvents; ответ идёт сюда через AcpService).
func (c *Client) ResolvePermission(ctx context.Context, id, decision string) error {
	_, err := c.rpc.ResolvePermission(ctx, authReq(c.token, &v1.DaemonResolvePermissionRequest{Id: id, Decision: decision}))
	return err
}

// Summarize → служебный recap-turn в демоне (архивация).
func (c *Client) Summarize(ctx context.Context, prompt string) (string, error) {
	resp, err := c.rpc.Summarize(ctx, authReq(c.token, &v1.DaemonSummarizeRequest{Prompt: prompt}))
	if err != nil {
		return "", err
	}
	return resp.Msg.Text, nil
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

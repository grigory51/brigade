package acpdaemon

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/agui"
	"github.com/grigory51/brigade/backend/internal/eventlog"
)

// service реализует brigadev1connect.AgentDaemonServiceHandler поверх Daemon.
type service struct {
	d *Daemon
}

func (s *service) Configure(ctx context.Context, req *connect.Request[v1.DaemonConfigureRequest]) (*connect.Response[v1.DaemonConfigureResponse], error) {
	sid, err := s.d.configure(ctx, &v1ConfigureRequest{
		OauthToken:        req.Msg.OauthToken,
		ExtraEnv:          req.Msg.ExtraEnv,
		AdapterCommand:    req.Msg.AdapterCommand,
		Cwd:               req.Msg.Cwd,
		ResumeSessionId:   req.Msg.ResumeSessionId,
		ForkFromSessionId: req.Msg.ForkFromSessionId,
		PluginDirs:        req.Msg.PluginDirs,
		McpServersJson:    req.Msg.McpServersJson,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.DaemonConfigureResponse{SessionId: sid}), nil
}

// StreamEvents отдаёт события журнала с from_seq и далее live-tail, пока brigade подключён.
// Обрыв brigade (ctx отменён) завершает поток, но turn в демоне продолжается (см. Prompt).
//
// На (пере)подписке сначала переоткрываем открытые потоки (reconnect ПОСРЕДИ turn'а: иначе
// CONTENT/END без START) и переотдаём висящие permission (reconnect посреди ожидания
// разрешения), затем — журнал с from_seq + live-tail.
func (s *service) StreamEvents(ctx context.Context, req *connect.Request[v1.DaemonStreamEventsRequest], stream *connect.ServerStream[v1.DaemonEvent]) error {
	seq := s.d.log.LastSeq()
	if c, err := s.d.getClient(); err == nil {
		for _, evt := range c.ReopenEvents() {
			if err := sendDaemonEvent(stream, seq, evt); err != nil {
				return err
			}
		}
	}
	for _, pr := range s.d.perms.pendingList() {
		if err := sendDaemonEvent(stream, seq, agui.Event{Type: agui.EventCustom, Name: customPermissionName, Value: pr}); err != nil {
			return err
		}
	}
	return s.d.log.Follow(ctx.Done(), req.Msg.FromSeq, func(e eventlog.Entry) error {
		return stream.Send(&v1.DaemonEvent{Seq: e.Seq, AguiJson: e.Data})
	})
}

// sendDaemonEvent сериализует AG-UI событие и шлёт его подписчику (для reopen/pending —
// вне журнала, с текущим seq). Ошибка marshal не роняет поток.
func sendDaemonEvent(stream *connect.ServerStream[v1.DaemonEvent], seq int64, evt agui.Event) error {
	data, err := json.Marshal(evt)
	if err != nil {
		return nil
	}
	return stream.Send(&v1.DaemonEvent{Seq: seq, AguiJson: data})
}

// Prompt гонит turn на context.WithoutCancel: обрыв brigade НЕ отменяет turn — он доходит
// до конца в демоне, brigade переподключается и дочитывает журнал.
func (s *service) Prompt(ctx context.Context, req *connect.Request[v1.DaemonPromptRequest]) (*connect.Response[v1.DaemonPromptResponse], error) {
	c, err := s.d.getClient()
	if err != nil {
		return nil, err
	}
	stop, err := c.Prompt(context.WithoutCancel(ctx), req.Msg.Text, nil)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.DaemonPromptResponse{StopReason: stop}), nil
}

func (s *service) Cancel(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.Empty], error) {
	c, err := s.d.getClient()
	if err != nil {
		return nil, err
	}
	// Сначала сворачиваем висящие permission (иначе turn на WithoutCancel завис бы), затем
	// просим агента отменить turn.
	s.d.perms.cancelAll()
	if err := c.Cancel(ctx); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

func (s *service) FinishStreams(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.Empty], error) {
	c, err := s.d.getClient()
	if err != nil {
		return nil, err
	}
	c.FinishStreams()
	return connect.NewResponse(&v1.Empty{}), nil
}

func (s *service) Status(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.DaemonStatusResponse], error) {
	c, err := s.d.getClient()
	if err != nil {
		return nil, err
	}
	generating, _ := c.Status()
	// seq — из durable-журнала (переживает рестарт brigade), а не len(history) в RAM.
	return connect.NewResponse(&v1.DaemonStatusResponse{Generating: generating, Seq: s.d.log.LastSeq()}), nil
}

func (s *service) GetMessages(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.DaemonPayloadResponse], error) {
	c, err := s.d.getClient()
	if err != nil {
		return nil, err
	}
	return payload(c.Messages())
}

func (s *service) GetCommands(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.DaemonPayloadResponse], error) {
	c, err := s.d.getClient()
	if err != nil {
		return nil, err
	}
	return payload(c.Commands())
}

func (s *service) GetConfigOptions(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.DaemonPayloadResponse], error) {
	c, err := s.d.getClient()
	if err != nil {
		return nil, err
	}
	return payload(c.ConfigOptions())
}

func (s *service) SetConfigOption(ctx context.Context, req *connect.Request[v1.DaemonSetConfigOptionRequest]) (*connect.Response[v1.DaemonPayloadResponse], error) {
	c, err := s.d.getClient()
	if err != nil {
		return nil, err
	}
	opts, err := c.SetConfigOption(ctx, req.Msg.ConfigId, req.Msg.Value)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return payload(opts)
}

func (s *service) ResolvePermission(ctx context.Context, req *connect.Request[v1.DaemonResolvePermissionRequest]) (*connect.Response[v1.Empty], error) {
	s.d.perms.deliver(req.Msg.Id, req.Msg.Decision)
	return connect.NewResponse(&v1.Empty{}), nil
}

func (s *service) Summarize(ctx context.Context, req *connect.Request[v1.DaemonSummarizeRequest]) (*connect.Response[v1.DaemonSummarizeResponse], error) {
	c, err := s.d.getClient()
	if err != nil {
		return nil, err
	}
	text, err := c.Summarize(ctx, req.Msg.Prompt)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.DaemonSummarizeResponse{Text: text}), nil
}

// payload сериализует произвольную структуру в DaemonPayloadResponse.
func payload(v any) (*connect.Response[v1.DaemonPayloadResponse], error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.DaemonPayloadResponse{Json: data}), nil
}

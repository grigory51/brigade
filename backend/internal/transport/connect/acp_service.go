package connectsvc

import (
	"context"

	acpsdk "github.com/coder/acp-go-sdk"
	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/transport/agui"
)

// AcpService реализует brigade.v1.AcpService — управляющие вызовы ACP-чата (история,
// состояние, workflow, отмена, опции, ответ на разрешение). Потоковый turn идёт мимо
// Connect по SSE (POST /api/ag-ui/run); permission-ответ доставляется через тот же
// PermissionStore, что регистрирует ожидание SSE-резолвер.
type AcpService struct {
	provider  agui.ClientProvider
	workflows agui.WorkflowLister
	perms     *agui.PermissionStore
}

// NewAcpService собирает реализацию AcpService.
func NewAcpService(provider agui.ClientProvider, workflows agui.WorkflowLister, perms *agui.PermissionStore) *AcpService {
	return &AcpService{provider: provider, workflows: workflows, perms: perms}
}

// bindable отдаёт ACP-клиента сессии её владельцу либо connect-ошибку (Unauthenticated
// без пользователя, NotFound если сессия неизвестна/чужая/не ACP).
func (s *AcpService) bindable(ctx context.Context, threadID string) (agui.Bindable, error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	b, ok := s.provider.Bindable(threadID, userID)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errSessionNotFound)
	}
	return b, nil
}

// GetHistory отдаёт ленту чата плюс снимки команд и опций сессии.
func (s *AcpService) GetHistory(ctx context.Context, req *connect.Request[v1.GetHistoryRequest]) (*connect.Response[v1.GetHistoryResponse], error) {
	b, err := s.bindable(ctx, req.Msg.ThreadId)
	if err != nil {
		return nil, err
	}
	resp := &v1.GetHistoryResponse{}
	for _, m := range b.Messages() {
		resp.Messages = append(resp.Messages, &v1.AcpMessage{
			Id: m.ID, Role: m.Role, Content: m.Content,
			ToolName: m.ToolName, ArgsText: m.ArgsText, Result: m.Result,
		})
	}
	for _, c := range b.Commands() {
		resp.Commands = append(resp.Commands, &v1.AcpCommand{
			Name: c.Name, Description: c.Description, Hint: c.Hint,
		})
	}
	resp.ConfigOptions = configOptionsToProto(b.ConfigOptions())
	return connect.NewResponse(resp), nil
}

// GetStatus — снимок состояния сессии.
func (s *AcpService) GetStatus(ctx context.Context, req *connect.Request[v1.GetStatusRequest]) (*connect.Response[v1.GetStatusResponse], error) {
	b, err := s.bindable(ctx, req.Msg.ThreadId)
	if err != nil {
		return nil, err
	}
	generating, seq := b.Status()
	return connect.NewResponse(&v1.GetStatusResponse{Generating: generating, Seq: int64(seq)}), nil
}

// ListWorkflows — workflow-запуски харнесса сессии.
func (s *AcpService) ListWorkflows(ctx context.Context, req *connect.Request[v1.ListWorkflowsRequest]) (*connect.Response[v1.ListWorkflowsResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	list, ok := s.workflows.SessionWorkflows(ctx, req.Msg.ThreadId, userID)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errSessionNotFound)
	}
	resp := &v1.ListWorkflowsResponse{}
	for _, wf := range list {
		resp.Workflows = append(resp.Workflows, &v1.AcpWorkflow{
			RunId: wf.RunID, Name: wf.Name,
			AgentsStarted: int32(wf.AgentsStarted), AgentsDone: int32(wf.AgentsDone),
			Done: wf.Done, Active: wf.Active, LastActivitySec: wf.LastActivitySec,
		})
	}
	return connect.NewResponse(resp), nil
}

// Cancel просит агента отменить текущий turn.
func (s *AcpService) Cancel(ctx context.Context, req *connect.Request[v1.CancelRequest]) (*connect.Response[v1.Empty], error) {
	b, err := s.bindable(ctx, req.Msg.ThreadId)
	if err != nil {
		return nil, err
	}
	if err := b.Cancel(ctx); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	// Сворачиваем висящие запросы разрешения сессии: turn идёт на неотменяемом контексте
	// (WithoutCancel), поэтому неотвеченный permission иначе держал бы его бесконечно.
	// Явный Stop — надёжный разрыв (см. run.resolvePermission, PermissionStore.CancelPending).
	s.perms.CancelPending(req.Msg.ThreadId)
	return connect.NewResponse(&v1.Empty{}), nil
}

// SetConfigOption меняет опцию сессии и возвращает актуальный набор.
func (s *AcpService) SetConfigOption(ctx context.Context, req *connect.Request[v1.SetConfigOptionRequest]) (*connect.Response[v1.SetConfigOptionResponse], error) {
	b, err := s.bindable(ctx, req.Msg.ThreadId)
	if err != nil {
		return nil, err
	}
	opts, err := b.SetConfigOption(ctx, req.Msg.ConfigId, req.Msg.Value)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&v1.SetConfigOptionResponse{ConfigOptions: configOptionsToProto(opts)}), nil
}

// ResolvePermission доставляет решение пользователя ожидающему резолверу /run.
// Best-effort: отсутствие ожидания (повтор/опоздание) — не ошибка.
func (s *AcpService) ResolvePermission(ctx context.Context, req *connect.Request[v1.ResolvePermissionRequest]) (*connect.Response[v1.Empty], error) {
	b, err := s.bindable(ctx, req.Msg.ThreadId)
	if err != nil {
		return nil, err
	}
	// docker-режим (durable-демон): ожидание permission живёт в демоне, а не в brigade —
	// диалог пришёл фронту CUSTOM-событием через StreamEvents, ответ доставляем демону.
	if rp, ok := b.(interface {
		ResolvePermission(context.Context, string, string) error
	}); ok {
		_ = rp.ResolvePermission(ctx, req.Msg.Id, req.Msg.Decision)
	}
	// local-режим: доставка ожидающему резолверу /run (brigade PermissionStore).
	s.perms.Deliver(agui.PermissionKey(req.Msg.ThreadId, req.Msg.Id), req.Msg.Decision)
	return connect.NewResponse(&v1.Empty{}), nil
}

// configOptionsToProto нормализует опции сессии из union-формата ACP-SDK в типизированный
// proto. Берутся только Select-опции (Boolean UI не показывает). UI-модель — плоский
// список значений, поэтому grouped-варианты сплющиваются (заголовки групп отбрасываются:
// селектор группировку не рисует). Политику скрытия небезопасных значений
// (bypassPermissions) применяет web-клиент.
func configOptionsToProto(opts []acpsdk.SessionConfigOption) []*v1.AcpConfigOption {
	var out []*v1.AcpConfigOption
	for _, o := range opts {
		sel := o.Select
		if sel == nil {
			continue
		}
		category := ""
		if sel.Category != nil {
			category = string(*sel.Category)
		}
		opt := &v1.AcpConfigOption{
			Id:           string(sel.Id),
			Name:         sel.Name,
			Category:     category,
			CurrentValue: string(sel.CurrentValue),
		}
		if sel.Options.Ungrouped != nil {
			for _, v := range *sel.Options.Ungrouped {
				opt.Options = append(opt.Options, selectValueToProto(v))
			}
		}
		if sel.Options.Grouped != nil {
			for _, g := range *sel.Options.Grouped {
				for _, v := range g.Options {
					opt.Options = append(opt.Options, selectValueToProto(v))
				}
			}
		}
		out = append(out, opt)
	}
	return out
}

// selectValueToProto переводит одно значение опции ACP-SDK в proto.
func selectValueToProto(v acpsdk.SessionConfigSelectOption) *v1.AcpConfigOptionValue {
	desc := ""
	if v.Description != nil {
		desc = *v.Description
	}
	return &v1.AcpConfigOptionValue{Value: string(v.Value), Name: v.Name, Description: desc}
}

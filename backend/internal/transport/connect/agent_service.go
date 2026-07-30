package connectsvc

import (
	"context"

	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/agent"
)

// AgentService реализует brigade.v1.AgentService поверх манифеста агентов
// (internal/agent) — того же, по которому собирается среда запуска. Режим взаимодействия
// (CLI — pty + xterm, ACP — AG-UI) выбирается отдельно через SessionKind.
type AgentService struct{}

// NewAgentService собирает реализацию AgentService.
func NewAgentService() *AgentService { return &AgentService{} }

// ListAgentTypes возвращает доступные типы агентов. Режим взаимодействия задаётся
// отдельно (SessionKind) и от агента не зависит, поэтому здесь не фигурирует.
func (s *AgentService) ListAgentTypes(_ context.Context, _ *connect.Request[v1.ListAgentTypesRequest]) (*connect.Response[v1.ListAgentTypesResponse], error) {
	types := agent.List()
	out := make([]*v1.AgentType, 0, len(types))
	for _, t := range types {
		out = append(out, &v1.AgentType{Id: t.ID, Name: t.Name})
	}
	return connect.NewResponse(&v1.ListAgentTypesResponse{AgentTypes: out}), nil
}

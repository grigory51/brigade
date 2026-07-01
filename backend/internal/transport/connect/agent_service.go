package connectsvc

import (
	"context"

	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
)

// AgentService реализует brigade.v1.AgentService. Список типов агентов статичен:
// brigade целится в Claude Code. Режим взаимодействия (CLI — pty + xterm, ACP —
// AG-UI) выбирается отдельно через SessionKind, агент поддерживает оба.
type AgentService struct{}

// NewAgentService собирает реализацию AgentService.
func NewAgentService() *AgentService { return &AgentService{} }

// ListAgentTypes возвращает доступные типы агентов. Режим взаимодействия задаётся
// отдельно (SessionKind) и от агента не зависит, поэтому здесь не фигурирует.
func (s *AgentService) ListAgentTypes(_ context.Context, _ *connect.Request[v1.ListAgentTypesRequest]) (*connect.Response[v1.ListAgentTypesResponse], error) {
	return connect.NewResponse(&v1.ListAgentTypesResponse{
		AgentTypes: []*v1.AgentType{
			{Id: "claude-code", Name: "Claude Code"},
		},
	}), nil
}

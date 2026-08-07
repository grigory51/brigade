package connectsvc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"connectrpc.com/connect"

	"github.com/google/uuid"
	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/agent"
	"github.com/grigory51/brigade/backend/internal/codexlogin"
	"github.com/grigory51/brigade/backend/internal/store"
)

// AgentService реализует brigade.v1.AgentService поверх манифеста агентов
// (internal/agent) — того же, по которому собирается среда запуска. Режим взаимодействия
// (CLI — pty + xterm, ACP — AG-UI) выбирается отдельно через SessionKind.
type AgentService struct {
	store *store.Store
	login *codexlogin.Service
}

// NewAgentService собирает реализацию AgentService.
func NewAgentService(store *store.Store, login *codexlogin.Service) *AgentService {
	return &AgentService{store: store, login: login}
}

// ListAgentTypes возвращает доступные типы агентов. Режим взаимодействия задаётся
// отдельно (SessionKind) и от агента не зависит, поэтому здесь не фигурирует.
func (s *AgentService) ListAgentTypes(_ context.Context, _ *connect.Request[v1.ListAgentTypesRequest]) (*connect.Response[v1.ListAgentTypesResponse], error) {
	types := agent.List()
	out := make([]*v1.AgentType, 0, len(types))
	for _, t := range types {
		kinds := make([]string, 0, len(t.Command))
		for _, kind := range []store.SessionKind{store.SessionKindACP, store.SessionKindCLI} {
			if t.CommandFor(kind) != "" {
				kinds = append(kinds, string(kind))
			}
		}
		profiles := []string{"claude-token"}
		section := "claude"
		if t.ID == agent.Codex.ID {
			profiles, section = []string{"chatgpt", "api-key"}, "codex"
		}
		out = append(out, &v1.AgentType{Id: t.ID, Name: t.Name, SupportedKinds: kinds, AuthProfiles: profiles, SettingsSection: section})
	}
	return connect.NewResponse(&v1.ListAgentTypesResponse{AgentTypes: out}), nil
}

func (s *AgentService) ListConnections(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.ListAgentConnectionsResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	items, err := s.store.ListAgentConnections(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*v1.AgentConnection, 0, len(items))
	for _, item := range items {
		out = append(out, agentConnectionToProto(item))
	}
	return connect.NewResponse(&v1.ListAgentConnectionsResponse{Connections: out}), nil
}

func (s *AgentService) SaveConnection(ctx context.Context, req *connect.Request[v1.SaveAgentConnectionRequest]) (*connect.Response[v1.AgentConnection], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	in := req.Msg.Connection
	if in == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("agent connection required"))
	}
	item := store.AgentConnection{ID: in.Id, UserID: userID, Name: strings.TrimSpace(in.Name), AgentType: in.AgentType, AuthProfile: in.AuthProfile, Secret: strings.TrimSpace(req.Msg.Secret)}
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	if item.Name == "" {
		item.Name = agent.Get(item.AgentType).Name
	}
	if err := validateAgentConnection(item); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if item.Secret == "" {
		existing, getErr := s.store.GetAgentConnection(ctx, userID, item.ID)
		if getErr != nil || existing.Secret == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("agent secret required"))
		}
	}
	if err := s.store.SaveAgentConnection(ctx, item); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	saved, err := s.store.GetAgentConnection(ctx, userID, item.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(agentConnectionToProto(saved)), nil
}

func (s *AgentService) DeleteConnection(ctx context.Context, req *connect.Request[v1.AgentConnectionRequest]) (*connect.Response[v1.Empty], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteAgentConnection(ctx, userID, req.Msg.Id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

func (s *AgentService) StartCodexLogin(ctx context.Context, req *connect.Request[v1.StartAgentCodexLoginRequest]) (*connect.Response[v1.CodexLogin], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	in := req.Msg.Connection
	if in == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("agent connection required"))
	}
	item := store.AgentConnection{ID: in.Id, UserID: userID, Name: strings.TrimSpace(in.Name), AgentType: agent.Codex.ID, AuthProfile: "chatgpt"}
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	if item.Name == "" {
		item.Name = "Codex"
	}
	login := s.login.StartWithSave(userID, func(ctx context.Context, secret string) error {
		item.Secret = secret
		return s.store.SaveAgentConnection(ctx, item)
	})
	out := codexLoginToProto(login)
	out.ConnectionId = item.ID
	return connect.NewResponse(out), nil
}

func validateAgentConnection(item store.AgentConnection) error {
	switch item.AgentType {
	case agent.Claude.ID:
		if item.AuthProfile != "claude-token" {
			return errors.New("unknown Claude auth profile")
		}
	case agent.Codex.ID:
		if item.AuthProfile != "chatgpt" && item.AuthProfile != "api-key" {
			return errors.New("unknown Codex auth profile")
		}
		if item.AuthProfile == "chatgpt" && item.Secret != "" && !json.Valid([]byte(item.Secret)) {
			return errors.New("Codex auth.json is not valid JSON")
		}
	default:
		return errors.New("unknown agent type")
	}
	return nil
}

func agentConnectionToProto(item store.AgentConnection) *v1.AgentConnection {
	return &v1.AgentConnection{Id: item.ID, Name: item.Name, AgentType: item.AgentType, AuthProfile: item.AuthProfile, SecretSet: item.Secret != ""}
}

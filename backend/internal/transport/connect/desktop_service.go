package connectsvc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/desktopenv"
)

type DesktopService struct{ environments *desktopenv.Manager }

func NewDesktopService(environments *desktopenv.Manager) *DesktopService {
	return &DesktopService{environments: environments}
}

func desktopEnvironmentToProto(environment desktopenv.Environment, activeID string) *v1.DesktopEnvironment {
	authMethods := environment.AuthMethods
	if environment.Kind == "remote" && len(authMethods) == 0 {
		authMethods = []*v1.AuthMethod{{Id: "password", Kind: "password", Name: "Логин и пароль"}}
	}
	result := &v1.DesktopEnvironment{
		Id: environment.ID, Name: environment.Name, Kind: environment.Kind, BaseUrl: environment.BaseURL,
		Active: environment.ID == activeID, Connected: environment.Kind == "local" || environment.Username != "",
		Username: environment.Username, Version: environment.Version, Capabilities: environment.Capabilities,
		AuthMethods: authMethods, Error: environment.Error,
	}
	for _, forward := range environment.PortForwards {
		result.PortForwards = append(result.PortForwards, &v1.DesktopPortForward{Id: forward.ID, SessionId: forward.SessionID, RemotePort: int32(forward.RemotePort), LocalPort: int32(forward.LocalPort)})
	}
	for _, mount := range environment.Mounts {
		result.Mounts = append(result.Mounts, &v1.DesktopMount{Id: mount.ID, SessionId: mount.SessionID, Path: mount.Path})
	}
	return result
}

func (s *DesktopService) AddPortForward(ctx context.Context, req *connect.Request[v1.AddDesktopPortForwardRequest]) (*connect.Response[v1.DesktopPortForward], error) {
	if err := s.require(ctx); err != nil {
		return nil, err
	}
	forward, err := s.environments.AddPortForward(ctx, req.Msg.SessionId, int(req.Msg.RemotePort), int(req.Msg.LocalPort))
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	status, statusErr := s.environments.ResourceStatus(forward.ID)
	return connect.NewResponse(&v1.DesktopPortForward{Id: forward.ID, SessionId: forward.SessionID, RemotePort: int32(forward.RemotePort), LocalPort: int32(forward.LocalPort), Status: status, Error: statusErr}), nil
}

func (s *DesktopService) RemovePortForward(ctx context.Context, req *connect.Request[v1.DesktopResourceRequest]) (*connect.Response[v1.Empty], error) {
	if err := s.require(ctx); err != nil {
		return nil, err
	}
	if err := s.environments.RemovePortForward(req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

func (s *DesktopService) MountWorkspace(ctx context.Context, req *connect.Request[v1.AddDesktopMountRequest]) (*connect.Response[v1.DesktopMount], error) {
	if err := s.require(ctx); err != nil {
		return nil, err
	}
	mount, err := s.environments.AddMount(ctx, req.Msg.SessionId, req.Msg.SessionName)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	status, statusErr := s.environments.ResourceStatus(mount.ID)
	return connect.NewResponse(&v1.DesktopMount{Id: mount.ID, SessionId: mount.SessionID, Path: mount.Path, Status: status, Error: statusErr}), nil
}

func (s *DesktopService) UnmountWorkspace(ctx context.Context, req *connect.Request[v1.DesktopResourceRequest]) (*connect.Response[v1.Empty], error) {
	if err := s.require(ctx); err != nil {
		return nil, err
	}
	if err := s.environments.RemoveMount(req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

func (s *DesktopService) require(ctx context.Context) error {
	if _, err := requireUser(ctx); err != nil {
		return err
	}
	if s.environments == nil {
		return connect.NewError(connect.CodeUnimplemented, errors.New("desktop only"))
	}
	return nil
}

func (s *DesktopService) ListEnvironments(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.ListDesktopEnvironmentsResponse], error) {
	if err := s.require(ctx); err != nil {
		return nil, err
	}
	s.environments.RefreshInfo(ctx)
	environments, activeID, needsSetup := s.environments.List()
	response := &v1.ListDesktopEnvironmentsResponse{ActiveId: activeID, NeedsSetup: needsSetup, Environments: make([]*v1.DesktopEnvironment, 0, len(environments))}
	for _, environment := range environments {
		item := desktopEnvironmentToProto(environment, activeID)
		for _, forward := range item.PortForwards {
			forward.Status, forward.Error = s.environments.ResourceStatus(forward.Id)
		}
		for _, mount := range item.Mounts {
			mount.Status, mount.Error = s.environments.ResourceStatus(mount.Id)
		}
		response.Environments = append(response.Environments, item)
	}
	return connect.NewResponse(response), nil
}

func (s *DesktopService) AddEnvironment(ctx context.Context, req *connect.Request[v1.AddDesktopEnvironmentRequest]) (*connect.Response[v1.DesktopEnvironment], error) {
	if err := s.require(ctx); err != nil {
		return nil, err
	}
	environment, err := s.environments.Add(ctx, req.Msg.Name, req.Msg.BaseUrl)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(desktopEnvironmentToProto(environment, "")), nil
}

func (s *DesktopService) UpdateEnvironment(ctx context.Context, req *connect.Request[v1.UpdateDesktopEnvironmentRequest]) (*connect.Response[v1.DesktopEnvironment], error) {
	if err := s.require(ctx); err != nil {
		return nil, err
	}
	environment, err := s.environments.Update(ctx, req.Msg.Id, req.Msg.Name, req.Msg.BaseUrl)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(desktopEnvironmentToProto(environment, "")), nil
}

func (s *DesktopService) DeleteEnvironment(ctx context.Context, req *connect.Request[v1.DesktopEnvironmentRequest]) (*connect.Response[v1.Empty], error) {
	if err := s.require(ctx); err != nil {
		return nil, err
	}
	if err := s.environments.Delete(req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

func (s *DesktopService) SelectEnvironment(ctx context.Context, req *connect.Request[v1.DesktopEnvironmentRequest]) (*connect.Response[v1.DesktopEnvironment], error) {
	if err := s.require(ctx); err != nil {
		return nil, err
	}
	environment, err := s.environments.Select(req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(desktopEnvironmentToProto(environment, environment.ID)), nil
}

func (s *DesktopService) LoginEnvironment(ctx context.Context, req *connect.Request[v1.LoginDesktopEnvironmentRequest]) (*connect.Response[v1.DesktopEnvironment], error) {
	if err := s.require(ctx); err != nil {
		return nil, err
	}
	environment, err := s.environments.Login(ctx, req.Msg.Id, req.Msg.Username, req.Msg.Password)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	return connect.NewResponse(desktopEnvironmentToProto(environment, "")), nil
}

func (s *DesktopService) LogoutEnvironment(ctx context.Context, req *connect.Request[v1.DesktopEnvironmentRequest]) (*connect.Response[v1.DesktopEnvironment], error) {
	if err := s.require(ctx); err != nil {
		return nil, err
	}
	environment, err := s.environments.Logout(req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(desktopEnvironmentToProto(environment, "")), nil
}

package connectsvc

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/agentimage"
	"github.com/grigory51/brigade/backend/internal/auth"
	"github.com/grigory51/brigade/backend/internal/store"
)

func (s *AuthService) GetAgentImageBuild(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.AgentImageBuild], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	if s.builds == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, agentimage.ErrUnavailable)
	}
	b, err := s.builds.Get(ctx, u.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(imageBuildToProto(b)), nil
}

func (s *AuthService) StartAgentImageBuild(ctx context.Context, req *connect.Request[v1.StartAgentImageBuildRequest]) (*connect.Response[v1.AgentImageBuild], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	if s.builds == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, agentimage.ErrUnavailable)
	}
	b, err := s.builds.Start(ctx, u.ID, req.Msg.Name, req.Msg.Script, req.Msg.NoCache)
	if err != nil {
		code := connect.CodeInvalidArgument
		switch {
		case errors.Is(err, agentimage.ErrBuildBusy), errors.Is(err, agentimage.ErrUnavailable):
			code = connect.CodeFailedPrecondition
		case errors.Is(err, agentimage.ErrBuildQueueFull):
			code = connect.CodeResourceExhausted
		}
		return nil, connect.NewError(code, err)
	}
	return connect.NewResponse(imageBuildToProto(b)), nil
}

func (s *AuthService) CancelAgentImageBuild(ctx context.Context, req *connect.Request[v1.CancelAgentImageBuildRequest]) (*connect.Response[v1.AgentImageBuild], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	if s.builds == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, agentimage.ErrUnavailable)
	}
	b, err := s.builds.Cancel(ctx, u.ID, req.Msg.Id)
	if err != nil {
		code := connect.CodeInternal
		if errors.Is(err, agentimage.ErrBuildNotFound) {
			code = connect.CodeNotFound
		}
		return nil, connect.NewError(code, err)
	}
	return connect.NewResponse(imageBuildToProto(b)), nil
}

func imageBuildToProto(b store.ImageBuild) *v1.AgentImageBuild {
	return &v1.AgentImageBuild{Id: b.ID, Name: b.Name, Script: b.Script, Status: b.Status, Log: b.Log, Error: b.Error, Image: b.Image}
}

package connectsvc

import (
	"context"
	"errors"
	"strconv"

	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/coding"
	"github.com/grigory51/brigade/backend/internal/store"
)

type CodingService struct{ coding *coding.Service }

func NewCodingService(service *coding.Service) *CodingService { return &CodingService{coding: service} }

func (s *CodingService) GetCI(ctx context.Context, req *connect.Request[v1.GetCIRequest]) (*connect.Response[v1.GetCIResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.coding.GetCI(ctx, req.Msg.SessionId, userID, req.Msg.ProjectPath)
	if err != nil {
		return nil, codingError(err)
	}
	response := &v1.GetCIResponse{Provider: string(result.Provider), Repository: result.Repository, CommitSha: result.CommitSHA, Partial: result.Partial, Warnings: result.Warnings, FetchedAt: result.FetchedAt.UnixMilli(), RefreshAfterSeconds: coding.RefreshAfterSeconds}
	for _, run := range result.Runs {
		mapped := &v1.CIRun{Id: run.ID, Name: run.Name, Status: run.Status, Conclusion: run.Conclusion, Url: run.URL, Ref: run.Ref, JobsPartial: run.JobsPartial}
		for _, job := range run.Jobs {
			mapped.Jobs = append(mapped.Jobs, &v1.CIJob{Id: job.ID, Name: job.Name, Status: job.Status, Conclusion: job.Conclusion, Url: job.URL, Stage: job.Stage, AllowFailure: job.AllowFailure})
		}
		response.Runs = append(response.Runs, mapped)
	}
	for _, deployment := range result.Deployments {
		response.Deployments = append(response.Deployments, &v1.CIDeployment{Id: deployment.ID, Environment: deployment.Environment, Status: deployment.Status, Url: deployment.URL, EnvironmentUrl: deployment.EnvironmentURL, Ref: deployment.Ref, CommitSha: deployment.CommitSHA})
	}
	return connect.NewResponse(response), nil
}

func codingError(err error) *connect.Error {
	code := connect.CodeInternal
	reason := "internal"
	switch {
	case errors.Is(err, store.ErrNotFound):
		code, reason = connect.CodeNotFound, "session-not-found"
	case errors.Is(err, coding.ErrInvalidRequest):
		code, reason = connect.CodeInvalidArgument, "invalid-request"
	case errors.Is(err, coding.ErrNoRemote):
		code, reason = connect.CodeFailedPrecondition, "no-remote"
	case errors.Is(err, coding.ErrNoHEAD):
		code, reason = connect.CodeFailedPrecondition, "no-head"
	case errors.Is(err, coding.ErrUnsupportedWorkspace):
		code, reason = connect.CodeFailedPrecondition, "unsupported-workspace"
	case errors.Is(err, coding.ErrWorkspace):
		code, reason = connect.CodeFailedPrecondition, "workspace-unavailable"
	case errors.Is(err, coding.ErrNotConfigured):
		code, reason = connect.CodeFailedPrecondition, "not-configured"
	case errors.Is(err, coding.ErrCredentials):
		code, reason = connect.CodePermissionDenied, "credentials"
	case errors.Is(err, coding.ErrRateLimited):
		code, reason = connect.CodeResourceExhausted, "rate-limited"
	case errors.Is(err, context.Canceled):
		code, reason = connect.CodeCanceled, "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		code, reason = connect.CodeDeadlineExceeded, "deadline-exceeded"
	case errors.Is(err, coding.ErrUpstream):
		code, reason = connect.CodeUnavailable, "upstream"
	}
	rpcErr := connect.NewError(code, err)
	rpcErr.Meta().Set("coding-reason", reason)
	if errors.Is(err, coding.ErrRateLimited) {
		rpcErr.Meta().Set("retry-after", strconv.Itoa(coding.RefreshAfterSeconds))
	}
	return rpcErr
}

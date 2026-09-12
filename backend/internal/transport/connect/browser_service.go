package connectsvc

import (
	"context"

	"connectrpc.com/connect"
	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/session"
)

type BrowserService struct{ registry *session.Registry }

func NewBrowserService(registry *session.Registry) *BrowserService {
	return &BrowserService{registry: registry}
}

func (s *BrowserService) Interact(ctx context.Context, req *connect.Request[v1.BrowserRequest]) (*connect.Response[v1.BrowserResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := s.registry.BrowserInteract(ctx, userID, req.Msg)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	r := connect.NewResponse(resp)
	r.Header().Set("Cache-Control", "no-store")
	return r, nil
}

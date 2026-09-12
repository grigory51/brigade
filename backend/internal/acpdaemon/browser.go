package acpdaemon

import (
	"context"

	"connectrpc.com/connect"
	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/browser"
)

func (s *service) BrowserInteract(ctx context.Context, req *connect.Request[v1.BrowserRequest]) (*connect.Response[v1.BrowserResponse], error) {
	// The signed daemon token, not a caller-supplied session ID, selects the browser.
	resp, err := browser.Interact(ctx, s.d.sessionID, req.Msg)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(resp), nil
}

func (s *service) GetBrowserDebug(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.BrowserDebugResponse], error) {
	resp, err := browser.Debug(ctx, s.d.sessionID)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(resp), nil
}

package acpremote

import (
	"context"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/daemonrpc"
)

func (c *Client) BrowserInteract(ctx context.Context, req *v1.BrowserRequest) (*v1.BrowserResponse, error) {
	resp, err := c.RPC.BrowserInteract(ctx, daemonrpc.Req(c.Sign(), req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

package cliremote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/gen/go/brigade/v1/brigadev1connect"
)

type hangingDaemon struct {
	brigadev1connect.UnimplementedAgentDaemonServiceHandler
}

func (hangingDaemon) OpenTerminal(ctx context.Context, _ *connect.Request[v1.DaemonOpenTerminalRequest], _ *connect.ServerStream[v1.DaemonTerminalOutput]) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestEphemeralStartHonorsContext(t *testing.T) {
	path, handler := brigadev1connect.NewAgentDaemonServiceHandler(hangingDaemon{})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := New(server.URL, "login", func() (string, error) { return "", nil }).StartEphemeral(ctx, []string{"codex"}, "/", nil, 0, 0)
	if err == nil {
		t.Fatal("StartEphemeral проигнорировал отмену context")
	}
}

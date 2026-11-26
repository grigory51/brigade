package cliremote

import (
	"context"
	"io"
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

type streamingDaemon struct {
	brigadev1connect.UnimplementedAgentDaemonServiceHandler
	canceled chan struct{}
}

func (d streamingDaemon) OpenTerminal(ctx context.Context, _ *connect.Request[v1.DaemonOpenTerminalRequest], stream *connect.ServerStream[v1.DaemonTerminalOutput]) error {
	defer close(d.canceled)
	if err := stream.Send(&v1.DaemonTerminalOutput{Data: []byte("ready")}); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestEphemeralStreamHonorsCancellationAfterStart(t *testing.T) {
	daemon := streamingDaemon{canceled: make(chan struct{})}
	path, handler := brigadev1connect.NewAgentDaemonServiceHandler(daemon)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := New(server.URL, "login", func() (string, error) { return "", nil })
	if err := client.StartEphemeral(ctx, []string{"codex"}, "/", nil, 0, 0); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	data := make([]byte, len("ready"))
	if _, err := io.ReadFull(client, data); err != nil || string(data) != "ready" {
		t.Fatalf("initial output = %q, err = %v", data, err)
	}
	cancel()
	select {
	case <-daemon.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon stream did not observe cancellation")
	}
	if _, err := io.ReadAll(client); err != nil {
		t.Fatal(err)
	}
	if client.Alive() {
		t.Fatal("canceled stream is still alive")
	}
}

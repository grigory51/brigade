package acpremote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/gen/go/brigade/v1/brigadev1connect"
	"github.com/grigory51/brigade/backend/internal/agui"
)

type recoveryDaemon struct {
	brigadev1connect.UnimplementedAgentDaemonServiceHandler
	lifecycle  string
	generating bool
	events     func(context.Context, *v1.DaemonStreamEventsRequest, *connect.ServerStream[v1.DaemonEvent]) error
	streams    atomic.Int32
}

func (d *recoveryDaemon) Status(context.Context, *connect.Request[v1.Empty]) (*connect.Response[v1.DaemonStatusResponse], error) {
	response := connect.NewResponse(&v1.DaemonStatusResponse{Seq: 7, Generating: d.generating})
	response.Header().Set("Brigade-Turn-Events", d.lifecycle)
	return response, nil
}

func (d *recoveryDaemon) StreamEvents(ctx context.Context, req *connect.Request[v1.DaemonStreamEventsRequest], stream *connect.ServerStream[v1.DaemonEvent]) error {
	d.streams.Add(1)
	return d.events(ctx, req.Msg, stream)
}

func (d *recoveryDaemon) client(t *testing.T) *Client {
	t.Helper()
	_, handler := brigadev1connect.NewAgentDaemonServiceHandler(d)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := New(server.URL, "session", func() (string, error) { return "", nil })
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestRecoveredTurnCursorReplayAndIndependentUI(t *testing.T) {
	at := time.Now().UTC()
	completed, err := json.Marshal(agui.Event{Type: agui.EventCustom, Name: agui.CustomTurnCompletedName, Value: map[string]string{"completedAt": at.Format(time.RFC3339Nano)}})
	if err != nil {
		t.Fatal(err)
	}
	uiFrames := make(chan agui.Event, 4)
	d := &recoveryDaemon{lifecycle: "active", generating: true}
	d.events = func(ctx context.Context, req *v1.DaemonStreamEventsRequest, stream *connect.ServerStream[v1.DaemonEvent]) error {
		if req.ReplayState {
			for _, frame := range []*v1.DaemonEvent{
				{Seq: 7, AguiJson: []byte(`{"type":"MESSAGES_SNAPSHOT","messages":[]}`)},
				{Seq: 8, AguiJson: completed},
				{Seq: 9, AguiJson: []byte(`{"type":"TEXT_MESSAGE_CONTENT","messageId":"ui","delta":"still connected"}`)},
			} {
				if err := stream.Send(frame); err != nil {
					return err
				}
			}
			<-ctx.Done()
			return ctx.Err()
		}
		if req.FromSeq != 7 {
			return errors.New("lost status cursor")
		}
		// Completion уже в журнале до Subscribe. Replay на старом cursor
		// не должен вызвать callback; повтор нового completion также не нужен.
		for _, seq := range []int64{7, 8, 8} {
			data := completed
			if seq == 7 {
				data = []byte(`{"type":"CUSTOM","name":"brigade.turn_completed","value":{"completedAt":"1970-01-01T00:00:00Z"}}`)
			}
			if err := stream.Send(&v1.DaemonEvent{Seq: seq, AguiJson: data}); err != nil {
				return err
			}
		}
		<-ctx.Done()
		return ctx.Err()
	}
	c := d.client(t)
	var notifications atomic.Int32
	c.SetHooks(func(string, error) { notifications.Add(1) }, nil)
	unbind := c.BindReplay(func(event agui.Event) error { uiFrames <- event; return nil }, nil)
	defer unbind()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var calls int
	if err := c.ObserveRecoveredTurn(ctx, func(_ context.Context, got time.Time) {
		calls++
		if !got.Equal(at) {
			t.Errorf("completion time = %v, want %v", got, at)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.ObserveRecoveredTurn(ctx, func(context.Context, time.Time) { calls++ }); err == nil {
		t.Fatal("second recovery observer accepted")
	}
	if calls != 1 || notifications.Load() != 0 {
		t.Fatalf("callbacks=%d notifications=%d", calls, notifications.Load())
	}
	for _, expected := range []agui.EventType{agui.EventMessagesSnapshot, agui.EventTextMessageContent} {
		select {
		case event := <-uiFrames:
			if event.Type != expected {
				t.Fatalf("private lifecycle leaked to UI: %+v", event)
			}
		case <-ctx.Done():
			t.Fatal("recovery disrupted UI subscription")
		}
	}
}

func TestRecoveredTurnCloseCancelsStream(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	d := &recoveryDaemon{lifecycle: "active", generating: true}
	d.events = func(ctx context.Context, _ *v1.DaemonStreamEventsRequest, stream *connect.ServerStream[v1.DaemonEvent]) error {
		defer close(stopped)
		if err := stream.Send(&v1.DaemonEvent{Seq: 8, AguiJson: []byte(`{"type":"TEXT_MESSAGE_END","messageId":"partial"}`)}); err != nil {
			return err
		}
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	c := d.client(t)
	var callbacks atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- c.ObserveRecoveredTurn(t.Context(), func(context.Context, time.Time) { callbacks.Add(1) })
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("observer did not subscribe")
	}
	_ = c.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("interrupted stream reported completion")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("orphan observer after Close")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("orphan server subscription after Close")
	}
	if callbacks.Load() != 0 {
		t.Fatal("message END mistaken for turn completion")
	}
}

func TestRecoveredTurnIdleAndLegacy(t *testing.T) {
	for _, tc := range []struct {
		name, lifecycle string
		generating      bool
		wantError       bool
	}{
		{"idle", "idle", false, false},
		{"background activity", "idle", true, false},
		{"legacy idle", "", false, false},
		{"legacy active", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &recoveryDaemon{lifecycle: tc.lifecycle, generating: tc.generating}
			c := d.client(t)
			called := false
			err := c.ObserveRecoveredTurn(t.Context(), func(_ context.Context, at time.Time) {
				called = true
				if !at.IsZero() {
					t.Fatal("idle recovery fabricated a completion time")
				}
			})
			if (err != nil) != tc.wantError || called == tc.wantError || d.streams.Load() != 0 {
				t.Fatalf("error=%v called=%v streams=%d", err, called, d.streams.Load())
			}
		})
	}
}

func TestRecoveredTurnCloseCancelsPreviewCallback(t *testing.T) {
	d := &recoveryDaemon{lifecycle: "idle"}
	c := d.client(t)
	started, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_ = c.ObserveRecoveredTurn(t.Context(), func(ctx context.Context, _ time.Time) {
			close(started)
			<-ctx.Done()
		})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("preview callback did not start")
	}
	_ = c.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("preview callback outlived Close")
	}
}

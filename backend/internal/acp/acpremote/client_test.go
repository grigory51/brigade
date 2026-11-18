package acpremote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/gen/go/brigade/v1/brigadev1connect"
	"github.com/grigory51/brigade/backend/internal/agui"
)

type replayDaemon struct {
	brigadev1connect.UnimplementedAgentDaemonServiceHandler
	seq    int64
	events func(context.Context, *connect.ServerStream[v1.DaemonEvent]) error
}

func (d *replayDaemon) Status(context.Context, *connect.Request[v1.Empty]) (*connect.Response[v1.DaemonStatusResponse], error) {
	return connect.NewResponse(&v1.DaemonStatusResponse{Seq: d.seq}), nil
}

func (*replayDaemon) FinishStreams(context.Context, *connect.Request[v1.Empty]) (*connect.Response[v1.Empty], error) {
	return connect.NewResponse(&v1.Empty{}), nil
}

func (d *replayDaemon) StreamEvents(ctx context.Context, req *connect.Request[v1.DaemonStreamEventsRequest], stream *connect.ServerStream[v1.DaemonEvent]) error {
	if !req.Msg.ReplayState {
		return errors.New("expected replay subscription")
	}
	return d.events(ctx, stream)
}

func (d *replayDaemon) client(t *testing.T) *Client {
	t.Helper()
	_, handler := brigadev1connect.NewAgentDaemonServiceHandler(d)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := New(server.URL, "session", func() (string, error) { return "", nil })
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestReplayWaitsForCurrentSnapshot(t *testing.T) {
	for _, seq := range []int64{0, 42} {
		t.Run(fmt.Sprint(seq), func(t *testing.T) {
			d := &replayDaemon{seq: seq}
			d.events = func(ctx context.Context, stream *connect.ServerStream[v1.DaemonEvent]) error {
				if err := stream.Send(&v1.DaemonEvent{Seq: seq, AguiJson: []byte(`{"type":"MESSAGES_SNAPSHOT","messages":[]}`)}); err != nil {
					return err
				}
				<-ctx.Done()
				return ctx.Err()
			}
			c := d.client(t)
			unbindOld, finishOld := c.BindReplayWithFinish(func(agui.Event) error { return nil }, nil)
			if err := finishOld(t.Context()); err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			unbind, finish := c.BindReplayWithFinish(func(evt agui.Event) error {
				if evt.Type != agui.EventMessagesSnapshot {
					return fmt.Errorf("unexpected event %s", evt.Type)
				}
				close(entered)
				select {
				case <-release:
				case <-t.Context().Done():
				}
				return nil
			}, nil)
			defer unbind()
			unbindOld()
			if err := finishOld(t.Context()); !errors.Is(err, context.Canceled) {
				t.Fatalf("superseded subscription: %v", err)
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("snapshot did not reach the new sink")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			if err := finish(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("snapshot still blocked in the new sink: got %v", err)
			}
			// На медленном SSE доставка snapshot может занять больше двух секунд:
			// лимит обычного FinishStreams не должен ограничивать replay.
			if seq != 0 {
				timer := time.AfterFunc(2100*time.Millisecond, func() { close(release) })
				defer timer.Stop()
			} else {
				close(release)
			}
			waitCtx, stop := context.WithTimeout(t.Context(), 5*time.Second)
			defer stop()
			if err := finish(waitCtx); err != nil {
				t.Fatalf("snapshot delivered, no further journal events: %v", err)
			}
		})
	}
}

func TestReplayFailureWakesWaiter(t *testing.T) {
	sinkErr := errors.New("SSE disconnected")
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"EOF before snapshot", io.EOF},
		{"sink rejected snapshot", sinkErr},
		{"unbind", context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			d := &replayDaemon{events: func(ctx context.Context, stream *connect.ServerStream[v1.DaemonEvent]) error {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-release:
				}
				if tc.err == sinkErr {
					return stream.Send(&v1.DaemonEvent{AguiJson: []byte(`{"type":"MESSAGES_SNAPSHOT","messages":[]}`)})
				}
				return nil
			}}
			c := d.client(t)
			unbind, finish := c.BindReplayWithFinish(func(agui.Event) error { return sinkErr }, nil)
			defer unbind()
			done := make(chan error, 1)
			go func() { done <- finish(t.Context()) }()
			// Ждём регистрации барьера, чтобы проверить пробуждение уже ожидающего вызова.
			deadline := time.Now().Add(time.Second)
			for {
				c.mu.Lock()
				waiting := c.stream.updated != nil
				c.mu.Unlock()
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("finish did not wait for snapshot")
				}
				time.Sleep(time.Millisecond)
			}
			if tc.err == context.Canceled {
				unbind()
			} else {
				close(release)
			}
			select {
			case err := <-done:
				if !errors.Is(err, tc.err) {
					t.Fatalf("finish = %v, want %v", err, tc.err)
				}
			case <-time.After(time.Second):
				t.Fatal("finish did not wake on stream termination")
			}
		})
	}
}

type messagesRPC struct {
	brigadev1connect.AgentDaemonServiceClient
	data []byte
	err  error
}

func (r *messagesRPC) GetMessages(context.Context, *connect.Request[v1.Empty]) (*connect.Response[v1.DaemonPayloadResponse], error) {
	return connect.NewResponse(&v1.DaemonPayloadResponse{Json: r.data}), r.err
}

func TestMessagesPreservesReadErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		rpc  *messagesRPC
	}{
		{name: "RPC", rpc: &messagesRPC{err: errors.New("daemon unavailable")}},
		{name: "JSON", rpc: &messagesRPC{data: []byte(`{"invalid":true}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := New("http://unused", "session", func() (string, error) { return "", nil })
			c.RPC = tc.rpc
			if _, err := c.Messages(context.Background()); err == nil {
				t.Fatal("history failure reported as an empty conversation")
			}
		})
	}
}

func TestNormalizeLegacyDaemonText(t *testing.T) {
	events := normalizeDaemonEvent(agui.Event{Type: agui.EventTextMessageContent, Delta: "error"})
	if len(events) != 3 || events[0].Type != agui.EventTextMessageStart || events[1].Type != agui.EventTextMessageContent || events[2].Type != agui.EventTextMessageEnd {
		t.Fatalf("events = %+v", events)
	}
	if events[0].MessageID == "" || events[1].MessageID != events[0].MessageID || events[2].MessageID != events[0].MessageID {
		t.Fatalf("message ids = %q, %q, %q", events[0].MessageID, events[1].MessageID, events[2].MessageID)
	}
}

func TestNormalizeLegacyDaemonSnapshot(t *testing.T) {
	events := normalizeDaemonEvent(agui.Event{
		Type: agui.EventMessagesSnapshot,
		Messages: []any{
			map[string]any{"id": "user-1", "role": "user", "content": "Вопрос"},
			map[string]any{"id": "call-1", "role": "assistant", "content": []any{
				map[string]any{"type": "tool-call", "toolCallId": "call-1", "toolName": "read_file", "args": map[string]any{"path": "a.txt"}, "result": "text"},
			}},
		},
	})
	data, err := json.Marshal(events[0].Messages)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, `"content":[`) || !strings.Contains(got, `"toolCalls":[`) || !strings.Contains(got, `"toolCallId":"call-1"`) {
		t.Fatalf("snapshot = %s", got)
	}
}

func TestSessionTitleEventUpdatesClientWithoutSink(t *testing.T) {
	var got string
	c := &Client{OnSessionTitle: func(title string) { got = title }}
	c.updateSessionTitle(agui.Event{
		Type:  agui.EventCustom,
		Name:  agui.CustomSessionTitleName,
		Value: map[string]any{"title": "Новая тема"},
	})
	if got != "Новая тема" || c.sessionTitle != got {
		t.Fatalf("callback=%q sessionTitle=%q", got, c.sessionTitle)
	}
}

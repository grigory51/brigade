package acpremote

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/gen/go/brigade/v1/brigadev1connect"
	"github.com/grigory51/brigade/backend/internal/agui"
)

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

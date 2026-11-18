package acpdaemon

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	acpsdk "github.com/coder/acp-go-sdk"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/gen/go/brigade/v1/brigadev1connect"
	"github.com/grigory51/brigade/backend/internal/acp"
	"github.com/grigory51/brigade/backend/internal/agui"
)

func TestStreamEventsNewTurnStartsOnceAfterStatusCursor(t *testing.T) {
	d, err := New("session", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.client = &acp.Client{}
	d.unbind = d.client.Bind(d.sink, nil)
	_, handler := brigadev1connect.NewAgentDaemonServiceHandler(&service{d: d})
	server := httptest.NewServer(handler)
	defer server.Close()
	rpc := brigadev1connect.NewAgentDaemonServiceClient(server.Client(), server.URL)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	chunk := func(id, text string) {
		t.Helper()
		err := d.client.SessionUpdate(ctx, acpsdk.SessionNotification{Update: acpsdk.SessionUpdate{
			AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{MessageId: &id, Content: acpsdk.TextBlock(text)},
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	chunk("old", "previous")
	d.client.FinishStreams()
	status, err := rpc.Status(ctx, connect.NewRequest(&v1.Empty{}))
	if err != nil {
		t.Fatal(err)
	}
	cursor := status.Msg.Seq
	// Первый чанк нового turn уже в журнале, но StreamEvents ещё не начался.
	chunk("new", "first")
	stream, err := rpc.StreamEvents(ctx, connect.NewRequest(&v1.DaemonStreamEventsRequest{FromSeq: cursor}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for i, want := range []agui.Event{
		{Type: agui.EventTextMessageStart}, {Type: agui.EventTextMessageContent, Delta: "first"},
		{Type: agui.EventTextMessageContent, Delta: "second"}, {Type: agui.EventTextMessageEnd},
	} {
		if i == 2 {
			chunk("new", "second")
			d.client.FinishStreams()
		}
		if !stream.Receive() {
			t.Fatalf("frame %d missing: %v", i, stream.Err())
		}
		frame := stream.Msg()
		var got agui.Event
		if err := json.Unmarshal(frame.AguiJson, &got); err != nil {
			t.Fatal(err)
		}
		if frame.Seq != cursor+int64(i)+1 || got.Type != want.Type || got.Delta != want.Delta || got.MessageID != "new" {
			t.Fatalf("frame %d: seq=%d event=%+v, cursor=%d want=%+v", i, frame.Seq, got, cursor, want)
		}
	}
}

func TestStatusIncludesPendingPermissions(t *testing.T) {
	d, err := New("s1", "", "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.client = &acp.Client{}
	d.perms.register(agui.PermissionRequest{
		ID: "p1", Title: "Run command",
		Options: []agui.PermissionOption{{OptionID: "allow", Kind: "allow_once"}},
	})

	resp, err := (&service{d: d}).Status(context.Background(), connect.NewRequest(&v1.Empty{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.PendingPermissionsJson) != 1 {
		t.Fatalf("pending permissions = %d, want 1", len(resp.Msg.PendingPermissionsJson))
	}
	if resp.Msg.Version != "v1.2.3" || resp.Msg.StartedAt == "" {
		t.Fatalf("daemon build = %q at %q", resp.Msg.Version, resp.Msg.StartedAt)
	}
	var pending agui.PermissionRequest
	if err := json.Unmarshal(resp.Msg.PendingPermissionsJson[0], &pending); err != nil {
		t.Fatal(err)
	}
	if pending.ID != "p1" || pending.Title != "Run command" {
		t.Fatalf("pending permission = %+v", pending)
	}
}

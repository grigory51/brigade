package acp

import (
	"context"
	"errors"
	"net"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/grigory51/brigade/backend/internal/agui"
)

type resumeTestAgent struct {
	acpsdk.Agent
	loadSupported bool
	loadError     error
	newCalls      atomic.Int32
}

func (a *resumeTestAgent) Initialize(context.Context, acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	return acpsdk.InitializeResponse{
		ProtocolVersion:   acpsdk.ProtocolVersionNumber,
		AgentCapabilities: acpsdk.AgentCapabilities{LoadSession: a.loadSupported},
	}, nil
}

func (a *resumeTestAgent) LoadSession(context.Context, acpsdk.LoadSessionRequest) (acpsdk.LoadSessionResponse, error) {
	return acpsdk.LoadSessionResponse{}, a.loadError
}

func (a *resumeTestAgent) NewSession(context.Context, acpsdk.NewSessionRequest) (acpsdk.NewSessionResponse, error) {
	a.newCalls.Add(1)
	return acpsdk.NewSessionResponse{SessionId: "unexpected-new-session"}, nil
}

func TestStrictResumeNeverCreatesSession(t *testing.T) {
	for _, tc := range []struct {
		name      string
		supported bool
		loadError error
		wantError bool
	}{
		{name: "unsupported", wantError: true},
		{name: "load failed", supported: true, loadError: errors.New("load failed"), wantError: true},
		{name: "loaded", supported: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientPipe, agentPipe := net.Pipe()
			defer clientPipe.Close()
			defer agentPipe.Close()
			agent := &resumeTestAgent{loadSupported: tc.supported, loadError: tc.loadError}
			acpsdk.NewAgentSideConnection(agent, agentPipe, agentPipe)
			client := &Client{opts: Options{Cwd: t.TempDir(), ResumeSessionID: "native", StrictResume: true}}
			client.conn = acpsdk.NewClientSideConnection(client, clientPipe, clientPipe)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err := client.handshake(ctx)
			if (err != nil) != tc.wantError || agent.newCalls.Load() != 0 {
				t.Fatalf("handshake error=%v new calls=%d", err, agent.newCalls.Load())
			}
			if !tc.wantError && client.SessionID() != "native" {
				t.Fatalf("native session changed: %q", client.SessionID())
			}
		})
	}
	if _, err := New(t.Context(), Options{Cwd: t.TempDir(), StrictResume: true}); err == nil {
		t.Fatal("strict resume accepted an empty native session ID")
	}
}

func TestIdleReservationAndRetiredClient(t *testing.T) {
	client := &Client{}
	release, err := client.ReserveIdle(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReserveIdle(t.Context()); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("second reservation: %v", err)
	}
	if client.promptMu.TryLock() {
		client.promptMu.Unlock()
		t.Fatal("reservation did not hold the turn barrier")
	}
	_ = client.CloseForReload()
	release()
	if _, err := client.Prompt(t.Context(), "must not enter history", nil); err == nil {
		t.Fatal("retired client accepted a prompt")
	}
	if len(client.history) != 0 {
		t.Fatal("rejected prompt changed history")
	}
	busy := &Client{promptActive: true}
	if _, err := busy.ReserveIdle(t.Context()); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("busy reservation: %v", err)
	}
}

func TestReplacementPreservesHistory(t *testing.T) {
	previous := &Client{sessionTitle: "Existing title", history: []agui.Event{
		{Type: agui.EventTextMessageStart, MessageID: "stable-id", Role: "user"},
		{Type: agui.EventTextMessageContent, MessageID: "stable-id", Delta: "Existing message"},
		{Type: agui.EventTextMessageEnd, MessageID: "stable-id"},
	}}
	replacement := &Client{history: []agui.Event{{Type: agui.EventTextMessageContent, MessageID: "replayed-id"}}}
	release, err := previous.ReserveIdle(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	replacement.PreserveHistory(previous)
	if !reflect.DeepEqual(replacement.history, previous.history) || replacement.SessionTitle() != previous.SessionTitle() {
		t.Fatal("replacement lost the existing history or title")
	}
	replacement.history[0].MessageID = "changed"
	if previous.history[0].MessageID != "stable-id" {
		t.Fatal("replacement shares the mutable history slice")
	}
}

func TestSessionTitleDoesNotMarkIdleClientBusy(t *testing.T) {
	client := &Client{}
	client.publishFallbackTitle("Completed turn")
	if busy, _ := client.Status(); busy {
		t.Fatal("fallback title marked a completed turn as generating")
	}
	release, err := client.ReserveIdle(t.Context())
	if err != nil {
		t.Fatalf("idle reservation after fallback title: %v", err)
	}
	release()
	client.emit(agui.Event{Type: agui.EventTextMessageContent, MessageID: "background", Delta: "Agent activity"})
	client.emit(agui.Event{Type: agui.EventCustom, Name: agui.CustomSessionTitleName, Value: map[string]string{"title": "Agent title"}})
	if busy, _ := client.Status(); !busy {
		t.Fatal("title update hid real background activity")
	}
	if _, err := client.ReserveIdle(t.Context()); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("reservation during background activity: %v", err)
	}
}

package acpdaemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	acpsdk "github.com/coder/acp-go-sdk"
	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/acp"
	"github.com/grigory51/brigade/backend/internal/agui"
)

type lifecycleAgent struct {
	experienceAgent
	started chan struct{}
	release chan struct{}
	fail    bool
}

func (a *lifecycleAgent) Prompt(ctx context.Context, _ acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
	a.started <- struct{}{}
	select {
	case <-a.release:
	case <-ctx.Done():
		return acpsdk.PromptResponse{}, ctx.Err()
	}
	if a.fail {
		return acpsdk.PromptResponse{}, errors.New("private agent failure")
	}
	return acpsdk.PromptResponse{StopReason: "end_turn"}, nil
}

func TestPromptJournalsCompletionWithAtomicStatus(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "error"}[fail], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			d, err := New("session", "", "test")
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			agent := &lifecycleAgent{
				experienceAgent: experienceAgent{writer: &sync.Mutex{}, owned: &atomic.Bool{}},
				started:         make(chan struct{}, 1), release: make(chan struct{}, 1), fail: fail,
			}
			client, err := acp.New(ctx, acp.Options{Cwd: t.TempDir(), ResumeSessionID: "native", StrictResume: true,
				SpawnProc: func(context.Context) (acp.AgentProc, error) {
					local, remote := net.Pipe()
					acpsdk.NewAgentSideConnection(agent, remote, remote)
					return &experienceProc{Conn: local, done: make(chan struct{}), onClose: func() { _ = remote.Close() }}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			d.client = client
			d.unbind = client.Bind(d.sink, nil)
			svc := &service{d: d}
			var previousCursor int64
			for range 2 {
				done := make(chan error, 1)
				go func() {
					_, err := svc.Prompt(ctx, connect.NewRequest(&v1.DaemonPromptRequest{Text: "question"}))
					done <- err
				}()
				select {
				case <-agent.started:
				case <-ctx.Done():
					t.Fatal("prompt did not start")
				}
				status, err := svc.Status(ctx, connect.NewRequest(&v1.Empty{}))
				if err != nil || status.Header().Get("Brigade-Turn-Events") != "active" || status.Msg.Seq < previousCursor {
					t.Fatalf("active snapshot: %v, %v", status, err)
				}
				cursor := status.Msg.Seq
				agent.release <- struct{}{}
				select {
				case err := <-done:
					if (err != nil) != fail {
						t.Fatalf("prompt error: %v", err)
					}
				case <-ctx.Done():
					t.Fatal("prompt did not finish")
				}
				entries := d.log.ReadFrom(cursor)
				completions := 0
				for _, entry := range entries {
					var event agui.Event
					if err := json.Unmarshal(entry.Data, &event); err != nil {
						t.Fatal(err)
					}
					if event.Name == agui.CustomTurnCompletedName {
						completions++
						if strings.Contains(string(entry.Data), "private") {
							t.Fatal("completion leaked error text")
						}
						previousCursor = entry.Seq
					}
				}
				status, err = svc.Status(ctx, connect.NewRequest(&v1.Empty{}))
				if err != nil || completions != 1 || status.Header().Get("Brigade-Turn-Events") != "idle" || status.Msg.Seq < previousCursor {
					t.Fatalf("idle snapshot precedes completion: status=%v count=%d err=%v", status, completions, err)
				}
			}
		})
	}
}

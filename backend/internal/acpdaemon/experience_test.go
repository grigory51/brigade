package acpdaemon

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/grigory51/brigade/backend/internal/acp"
	"github.com/grigory51/brigade/backend/internal/agui"
	"github.com/grigory51/brigade/backend/internal/plugin"
)

type experienceAgent struct {
	acpsdk.Agent
	writer *sync.Mutex
	owned  *atomic.Bool
}

func (*experienceAgent) Initialize(context.Context, acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	return acpsdk.InitializeResponse{ProtocolVersion: acpsdk.ProtocolVersionNumber, AgentCapabilities: acpsdk.AgentCapabilities{LoadSession: true}}, nil
}

func (a *experienceAgent) LoadSession(context.Context, acpsdk.LoadSessionRequest) (acpsdk.LoadSessionResponse, error) {
	if !a.writer.TryLock() {
		return acpsdk.LoadSessionResponse{}, errors.New("session already has an active writer")
	}
	a.owned.Store(true)
	return acpsdk.LoadSessionResponse{}, nil
}

type experienceProc struct {
	net.Conn
	done    chan struct{}
	once    sync.Once
	onClose func()
}

func (p *experienceProc) Stdin() io.WriteCloser { return p }
func (p *experienceProc) Stdout() io.Reader     { return p }
func (p *experienceProc) Close() error {
	p.once.Do(func() { _ = p.Conn.Close(); p.onClose(); close(p.done) })
	return nil
}
func (p *experienceProc) Kill() error   { return p.Close() }
func (p *experienceProc) Signal() error { return p.Close() }
func (p *experienceProc) Wait() error   { <-p.done; return nil }

func TestReconfigureFailureKeepsClientPluginAndJournal(t *testing.T) {
	for _, scenario := range []string{"invalid MCP", "adapter failure", "busy", "wrong native ID"} {
		t.Run(scenario, func(t *testing.T) {
			d, err := New("brigade-session", "", "test")
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			var writer sync.Mutex
			spawnProc := func(context.Context) (acp.AgentProc, error) {
				clientPipe, agentPipe := net.Pipe()
				owned := &atomic.Bool{}
				acpsdk.NewAgentSideConnection(&experienceAgent{writer: &writer, owned: owned}, agentPipe, agentPipe)
				return &experienceProc{Conn: clientPipe, done: make(chan struct{}), onClose: func() {
					_ = agentPipe.Close()
					if owned.Swap(false) {
						writer.Unlock()
					}
				}}, nil
			}
			client, err := acp.New(t.Context(), acp.Options{
				Cwd: t.TempDir(), ResumeSessionID: "native", StrictResume: true,
				SpawnProc: spawnProc,
			})
			if err != nil {
				t.Fatal(err)
			}
			d.client = client
			d.plugin = &plugin.Runtime{}
			runtime := d.plugin
			d.journal(agui.Event{Type: agui.EventTextMessageContent, MessageID: "preserved", Delta: "Existing content"})
			seq := d.log.LastSeq()
			request := &v1ConfigureRequest{Reconfigure: true, ResumeSessionId: "native", Cwd: t.TempDir(), AdapterCommand: filepath.Join(t.TempDir(), "missing-adapter")}
			switch scenario {
			case "invalid MCP":
				request.McpServersJson = []byte("invalid JSON")
			case "wrong native ID":
				request.ResumeSessionId = "another"
			case "busy":
				release, err := client.ReserveIdle(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			if _, err := d.configure(t.Context(), request); err == nil {
				t.Fatal("reconfigure should fail")
			}
			if d.client == nil || d.client.SessionID() != "native" || d.plugin != runtime || d.log.LastSeq() != seq || !d.client.Alive() {
				t.Fatal("failed reconfigure changed the original runtime or journal")
			}
			if scenario == "adapter failure" {
				if d.client == client {
					t.Fatal("rollback did not restart original adapter")
				}
			} else if d.client != client {
				t.Fatal("validation failure restarted adapter")
			}
		})
	}
}

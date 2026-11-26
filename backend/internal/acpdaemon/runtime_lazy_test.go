package acpdaemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	acpsdk "github.com/coder/acp-go-sdk"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
)

func lazyUIDaemon(t *testing.T) (*service, *v1.DaemonConfigureRequest) {
	t.Helper()
	d, err := New("lazy-ui-session", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	adapter := filepath.Join(t.TempDir(), "adapter")
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run='^TestExclusiveWriterAdapterHelper$'\n"
	if err := os.WriteFile(adapter, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BRIGADE_TEST_DAEMON_ADAPTER", "1")
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	return &service{d: d}, &v1.DaemonConfigureRequest{
		Cwd: t.TempDir(), AdapterCommand: adapter, ResumeSessionId: "native",
	}
}

func lazyUIConfig(t *testing.T, command string, args ...string) []byte {
	t.Helper()
	data, err := json.Marshal(acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{
		Name: "ui", Command: command, Args: args,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestConfigureLazyUIFailureKeepsChat(t *testing.T) {
	s, request := lazyUIDaemon(t)
	request.ExperienceMcpJson = lazyUIConfig(t, "sh", "-c", "echo started >> ui-starts; echo 'UI dependency missing' >&2; exit 1")
	request.McpServersJson = []byte(`[{"name":"agent-mcp","command":"agent-mcp","args":[],"env":[]}]`)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	response, err := s.Configure(ctx, connect.NewRequest(request))
	if err != nil || response.Msg.SessionId != "native" {
		t.Fatalf("Configure: response=%v error=%v", response, err)
	}
	client := s.d.client
	if servers := client.ResumeOptions().McpServers; len(servers) != 1 || servers[0].Stdio.Name != "agent-mcp" {
		t.Fatalf("agent MCP changed: %+v", servers)
	}
	if _, err := os.Stat(filepath.Join(request.Cwd, "ui-starts")); !os.IsNotExist(err) {
		t.Fatalf("Configure started UI MCP: %v", err)
	}
	if _, err := s.Prompt(ctx, connect.NewRequest(&v1.DaemonPromptRequest{Text: "Before UI"})); err != nil {
		t.Fatal(err)
	}
	seq := s.d.log.LastSeq()
	if _, err := s.PluginMCP(ctx, connect.NewRequest(&v1.DaemonPluginMCPRequest{Method: "ping"})); err == nil || !strings.Contains(err.Error(), "UI dependency missing") {
		t.Fatalf("UI startup error: %v", err)
	}
	if s.d.client != client || !client.Alive() || s.d.log.LastSeq() != seq {
		t.Fatal("UI failure changed agent or journal")
	}
	if _, err := s.Configure(ctx, connect.NewRequest(request)); err != nil {
		t.Fatalf("reconnect after UI failure: %v", err)
	}
	if _, err := s.Prompt(ctx, connect.NewRequest(&v1.DaemonPromptRequest{Text: "After UI"})); err != nil {
		t.Fatalf("chat after UI failure: %v", err)
	}
	if s.d.log.LastSeq() <= seq {
		t.Fatal("chat did not advance journal")
	}
}

func TestReconfigureLazyUIRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			s, request := lazyUIDaemon(t)
			request.ExperienceMcpJson = lazyUIConfig(t, filepath.Join(request.Cwd, "old-ui"))
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			if _, err := s.Configure(ctx, connect.NewRequest(request)); err != nil {
				t.Fatal(err)
			}
			oldRuntime := s.d.plugin
			if _, err := s.Prompt(ctx, connect.NewRequest(&v1.DaemonPromptRequest{Text: "Existing history"})); err != nil {
				t.Fatal(err)
			}
			before, err := s.d.client.Messages(ctx)
			if err != nil {
				t.Fatal(err)
			}
			seq := s.d.log.LastSeq()
			request.Reconfigure = true
			request.ExperienceMcpJson = lazyUIConfig(t, filepath.Join(request.Cwd, "new-ui"))
			if fail {
				if err := os.WriteFile(filepath.Join(request.Cwd, ".fail-next-load"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err = s.Configure(ctx, connect.NewRequest(request))
			if (err != nil) != fail {
				t.Fatalf("Configure: %v", err)
			}
			if (s.d.plugin == oldRuntime) != fail {
				t.Fatal("unexpected UI runtime after reconfigure")
			}
			after, err := s.d.client.Messages(ctx)
			if err != nil || !reflect.DeepEqual(before, after) || s.d.log.LastSeq() != seq {
				t.Fatalf("reconfigure changed history or journal: %v", err)
			}
			_, err = oldRuntime.Dispatch(ctx, "ping", nil)
			if fail {
				if err == nil || !strings.Contains(err.Error(), "old-ui") {
					t.Fatalf("rollback closed old runtime: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "runtime is closed") {
				t.Fatalf("reconfigure retained old runtime: %v", err)
			}
			if _, err := s.Prompt(ctx, connect.NewRequest(&v1.DaemonPromptRequest{Text: "Continue"})); err != nil {
				t.Fatalf("chat after reconfigure: %v", err)
			}
		})
	}
}

func TestReconfigureClosesLazyUIOutsideDaemonLock(t *testing.T) {
	s, request := lazyUIDaemon(t)
	// Процесс не читает stdin, поэтому закрытие handshake ждёт его завершения.
	request.ExperienceMcpJson = lazyUIConfig(t, "sh", "-c", "echo started > ui-started; while [ ! -f release-ui ]; do sleep 0.01; done")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := s.Configure(ctx, connect.NewRequest(request)); err != nil {
		t.Fatal(err)
	}
	oldRuntime := s.d.plugin
	uiDone := make(chan error, 1)
	go func() {
		_, err := s.PluginMCP(ctx, connect.NewRequest(&v1.DaemonPluginMCPRequest{Method: "ping"}))
		uiDone <- err
	}()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(request.Cwd, "ui-started")); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("UI did not start")
		case <-ticker.C:
		}
	}
	request.Reconfigure = true
	request.ExperienceMcpJson = nil
	configured := make(chan error, 1)
	go func() { _, err := s.Configure(ctx, connect.NewRequest(request)); configured <- err }()
	for {
		if s.d.mu.TryLock() {
			replaced := s.d.plugin != oldRuntime
			s.d.mu.Unlock()
			if replaced {
				break
			}
		}
		select {
		case err := <-configured:
			t.Fatalf("Configure completed before chat could acquire daemon lock: %v", err)
		case <-ctx.Done():
			t.Fatal("daemon lock blocked by old UI handshake")
		case <-ticker.C:
		}
	}
	if _, err := s.Prompt(ctx, connect.NewRequest(&v1.DaemonPromptRequest{Text: "Chat while UI closes"})); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(request.Cwd, "release-ui"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-configured:
		if err != nil {
			t.Fatalf("Configure: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Configure did not finish")
	}
	select {
	case err := <-uiDone:
		if err == nil {
			t.Fatal("closed UI handshake succeeded")
		}
	case <-ctx.Done():
		t.Fatal("UI dispatch did not finish")
	}
}

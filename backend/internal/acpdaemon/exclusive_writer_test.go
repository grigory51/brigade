package acpdaemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/grigory51/brigade/backend/internal/acp"
)

func TestExclusiveWriterAdapterHelper(t *testing.T) {
	if os.Getenv("BRIGADE_TEST_DAEMON_ADAPTER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	var writer *os.File
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		if len(request.ID) == 0 {
			continue
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "initialize":
			response["result"] = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]bool{"loadSession": true}}
		case "session/load":
			var err error
			writer, err = os.OpenFile(".native-writer", os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				os.Exit(2)
			}
			if err := syscall.Flock(int(writer.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				response["error"] = map[string]any{"code": -32603, "message": "session already has an active writer"}
			} else if _, err := os.Stat(".fail-all-loads"); err == nil {
				response["error"] = map[string]any{"code": -32603, "message": "rollback load rejected"}
			} else if os.Remove(".fail-next-load") == nil {
				response["error"] = map[string]any{"code": -32603, "message": "load rejected"}
			} else {
				if os.Remove(".block-next-load") == nil {
					if os.WriteFile(".load-entered", nil, 0o600) != nil {
						os.Exit(2)
					}
					time.Sleep(30 * time.Second)
				}
				response["result"] = map[string]any{}
			}
		case "session/prompt":
			response["result"] = map[string]any{"stopReason": "end_turn"}
		default:
			response["error"] = map[string]any{"code": -32601, "message": "unexpected method: " + request.Method}
		}
		if encoder.Encode(response) != nil {
			os.Exit(2)
		}
	}
	runtime.KeepAlive(writer)
	os.Exit(0)
}

func TestReconfigureExclusiveWriter(t *testing.T) {
	for _, scenario := range []string{"success", "load failure", "cancelled request", "rollback failure"} {
		t.Run(scenario, func(t *testing.T) {
			d, err := New("brigade-session", "", "test")
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			workspace := t.TempDir()
			adapter := filepath.Join(t.TempDir(), "adapter")
			script := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run='^TestExclusiveWriterAdapterHelper$'\n"
			if err := os.WriteFile(adapter, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BRIGADE_TEST_DAEMON_ADAPTER", "1")
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			request := &v1ConfigureRequest{Cwd: workspace, AdapterCommand: adapter, ResumeSessionId: "native"}
			if _, err := d.configure(ctx, request); err != nil {
				t.Fatal(err)
			}
			if _, err := d.client.Prompt(ctx, "Existing history", nil); err != nil {
				t.Fatal(err)
			}
			previous := d.client
			before, err := previous.Messages(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if duplicate, err := acp.New(ctx, previous.ResumeOptions()); err == nil {
				_ = duplicate.CloseForReload()
				t.Fatal("fixture allowed concurrent native writers")
			} else if !strings.Contains(err.Error(), "active writer") {
				t.Fatalf("fixture failed: %v", err)
			}
			request.Reconfigure = true
			request.SystemPrompt = "New experience instructions"
			var switchErr error
			if scenario == "cancelled request" {
				if err := os.WriteFile(filepath.Join(workspace, ".block-next-load"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
				requestCtx, abort := context.WithCancel(ctx)
				defer abort()
				done := make(chan error, 1)
				go func() { _, err := d.configure(requestCtx, request); done <- err }()
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for {
					if _, err := os.Stat(filepath.Join(workspace, ".load-entered")); err == nil {
						break
					}
					select {
					case <-ticker.C:
					case <-ctx.Done():
						t.Fatal("replacement did not start")
					}
				}
				abort()
				select {
				case switchErr = <-done:
				case <-ctx.Done():
					t.Fatal("rollback timed out")
				}
			} else {
				if scenario == "rollback failure" {
					if err := os.WriteFile(filepath.Join(workspace, ".fail-all-loads"), nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "load failure" {
					if err := os.WriteFile(filepath.Join(workspace, ".fail-next-load"), nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				_, switchErr = d.configure(ctx, request)
			}
			if (switchErr == nil) != (scenario == "success") {
				t.Fatalf("switch: %v", switchErr)
			}
			if scenario == "rollback failure" {
				if !errors.Is(switchErr, acp.ErrSessionUnrecoverable) || d.client != nil || d.unbind != nil {
					t.Fatalf("failed runtime retained: client=%v error=%v", d.client, switchErr)
				}
				return
			}
			if d.client == previous || d.client == nil || d.client.SessionID() != "native" || !d.client.Alive() {
				t.Fatalf("native runtime lost after switch: %v", switchErr)
			}
			after, err := d.client.Messages(ctx)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("history changed: before=%+v after=%+v error=%v", before, after, err)
			}
			wantPrompt := ""
			if scenario == "success" {
				wantPrompt = request.SystemPrompt
			}
			if got := d.client.ResumeOptions().SystemPrompt; got != wantPrompt {
				t.Fatalf("instructions=%q want=%q", got, wantPrompt)
			}
			if _, err := d.client.Prompt(ctx, "Continue conversation", nil); err != nil {
				t.Fatalf("restored client cannot prompt: %v", err)
			}
		})
	}
}

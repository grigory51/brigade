package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

func TestLazyRuntimeHelper(t *testing.T) {
	if os.Getenv("BRIGADE_TEST_LAZY_MCP") != "1" {
		return
	}
	record := func(name string) {
		file, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(2)
		}
		if _, err := file.WriteString("1\n"); err != nil {
			os.Exit(2)
		}
		if err := file.Close(); err != nil {
			os.Exit(2)
		}
	}
	record("starts")
	if os.Remove("fail-start") == nil {
		os.Exit(1)
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		if len(request.ID) == 0 {
			continue
		}
		var result any = map[string]any{}
		switch request.Method {
		case "initialize":
			record("initializing")
			for {
				if _, err := os.Stat("hold-start"); os.IsNotExist(err) {
					break
				}
				time.Sleep(time.Millisecond)
			}
			result = map[string]any{
				"protocolVersion": request.Params.ProtocolVersion,
				"serverInfo":      map[string]string{"name": "test", "version": "1"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			}
			if os.Remove("fail-protocol") == nil {
				result.(map[string]any)["protocolVersion"] = "unsupported"
			}
		case "tools/call":
			record("calls")
			if os.Getenv("BRIGADE_TEST_MCP_MODE") == "disconnect" {
				os.Exit(1)
			}
			if os.Getenv("BRIGADE_TEST_MCP_MODE") == "hold-call" {
				continue
			}
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": os.Getenv("BRIGADE_TEST_SNAPSHOT")}}}
		}
		if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}) != nil {
			os.Exit(2)
		}
	}
	record("exits")
	os.Exit(0)
}

func lazyRuntimeServer(t *testing.T) acpsdk.McpServer {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{
		Command: executable, Args: []string{"-test.run=^TestLazyRuntimeHelper$"},
		Env: []acpsdk.EnvVariable{
			{Name: "BRIGADE_TEST_LAZY_MCP", Value: "1"},
			{Name: "GORACE", Value: "atexit_sleep_ms=0"},
		},
	}}
}

func lazyRuntime(t *testing.T, directory string, env ...string) *Runtime {
	t.Helper()
	runtime, err := NewRuntime(lazyRuntimeServer(t), directory, env)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

func waitRuntimeFile(t *testing.T, directory, name string) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(directory, name)); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", name)
		case <-ticker.C:
		}
	}
}

func runtimeDispatchResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("runtime operation timed out")
		return nil
	}
}

func TestRuntimeLazySnapshot(t *testing.T) {
	directory := t.TempDir()
	server := lazyRuntimeServer(t)
	env := []string{"BRIGADE_TEST_SNAPSHOT=original"}
	runtime, err := NewRuntime(server, directory, env)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	server.Stdio.Command = "missing-command"
	server.Stdio.Args[0] = "changed"
	server.Stdio.Env[0].Value = "changed"
	env[0] = "BRIGADE_TEST_SNAPSHOT=changed"
	if _, err := os.Stat(filepath.Join(directory, "starts")); !os.IsNotExist(err) {
		t.Fatalf("constructor started a process: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result, err := runtime.Dispatch(ctx, "tools/call", []byte(`{"name":"snapshot"}`))
	cancel()
	if err != nil || !strings.Contains(string(result), "original") {
		t.Fatalf("snapshot result=%s error=%v", result, err)
	}
	if _, err := runtime.Dispatch(t.Context(), "ping", nil); err != nil {
		t.Fatalf("request cancellation killed runtime: %v", err)
	}
}

func TestRuntimeConcurrentStart(t *testing.T) {
	directory := t.TempDir()
	runtime := lazyRuntime(t, directory)
	const count = 16
	gate := make(chan struct{})
	done := make(chan error, count)
	for range count {
		go func() {
			<-gate
			_, err := runtime.Dispatch(t.Context(), "ping", nil)
			done <- err
		}()
	}
	close(gate)
	for range count {
		if err := runtimeDispatchResult(t, done); err != nil {
			t.Fatal(err)
		}
	}
	starts, err := os.ReadFile(filepath.Join(directory, "starts"))
	if err != nil || string(starts) != "1\n" {
		t.Fatalf("starts=%q error=%v", starts, err)
	}
}

func TestRuntimeRetryStartupFailure(t *testing.T) {
	for _, failure := range []string{"fail-start", "fail-protocol"} {
		t.Run(failure, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, failure), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			runtime := lazyRuntime(t, directory)
			if _, err := runtime.Dispatch(t.Context(), "ping", nil); err == nil {
				t.Fatal("first startup should fail")
			}
			if failure == "fail-protocol" {
				if _, err := os.Stat(filepath.Join(directory, "exits")); err != nil {
					t.Fatalf("failed handshake left process running: %v", err)
				}
			}
			if _, err := runtime.Dispatch(t.Context(), "ping", nil); err != nil {
				t.Fatalf("retry: %v", err)
			}
			starts, err := os.ReadFile(filepath.Join(directory, "starts"))
			if err != nil || string(starts) != "1\n1\n" {
				t.Fatalf("starts=%q error=%v", starts, err)
			}
		})
	}
}

func TestRuntimeStartupTimeout(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "hold-start"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := lazyRuntime(t, directory)
	ctx, cancel := context.WithTimeout(t.Context(), runtimeStartTimeout+5*time.Second)
	defer cancel()
	if _, err := runtime.Dispatch(ctx, "ping", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startup timeout: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("startup used caller deadline instead of its own timeout")
	}
}

func TestNewRuntimeRequiresStdio(t *testing.T) {
	for _, server := range []acpsdk.McpServer{{}, {Stdio: &acpsdk.McpServerStdio{}}} {
		if _, err := NewRuntime(server, t.TempDir(), nil); err == nil {
			t.Fatal("invalid stdio accepted")
		}
	}
}

func TestRuntimeCloseBeforeStart(t *testing.T) {
	directory := t.TempDir()
	runtime := lazyRuntime(t, directory)
	for range 2 {
		if err := runtime.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runtime.Dispatch(t.Context(), "ping", nil); !errors.Is(err, errRuntimeClosed) {
		t.Fatalf("Dispatch after Close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "starts")); !os.IsNotExist(err) {
		t.Fatalf("closed runtime started: %v", err)
	}
}

func TestRuntimeCloseDuringStart(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "hold-start"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := lazyRuntime(t, directory)
	done := make(chan error, 1)
	go func() { _, err := runtime.Dispatch(t.Context(), "ping", nil); done <- err }()
	waitRuntimeFile(t, directory, "initializing")
	closed := make(chan error, 1)
	go func() { closed <- runtime.Close() }()
	if err := runtimeDispatchResult(t, closed); err != nil {
		t.Fatal(err)
	}
	if err := runtimeDispatchResult(t, done); !errors.Is(err, errRuntimeClosed) {
		t.Fatalf("startup after Close: %v", err)
	}
	if _, err := runtime.Dispatch(t.Context(), "ping", nil); !errors.Is(err, errRuntimeClosed) {
		t.Fatalf("Dispatch after Close: %v", err)
	}
	if runtime.session != nil {
		t.Fatal("published a session after Close")
	}
}

func TestRuntimeStartupCancellation(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "hold-start"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := lazyRuntime(t, directory)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := runtime.Dispatch(ctx, "ping", nil); done <- err }()
	waitRuntimeFile(t, directory, "initializing")
	waiterCtx, stopWaiting := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer stopWaiting()
	if _, err := runtime.Dispatch(waiterCtx, "ping", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting Dispatch: %v", err)
	}
	cancel()
	if err := runtimeDispatchResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("startup cancellation: %v", err)
	}
	if err := os.Remove(filepath.Join(directory, "hold-start")); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Dispatch(t.Context(), "ping", nil); err != nil {
		t.Fatalf("retry after cancellation: %v", err)
	}
}

func TestRuntimeTransportFailureDoesNotRetryCall(t *testing.T) {
	directory := t.TempDir()
	runtime := lazyRuntime(t, directory, "BRIGADE_TEST_MCP_MODE=disconnect")
	if _, err := runtime.Dispatch(t.Context(), "tools/call", []byte(`{"name":"mutate"}`)); err == nil {
		t.Fatal("disconnected call should fail")
	}
	calls, err := os.ReadFile(filepath.Join(directory, "calls"))
	if err != nil || string(calls) != "1\n" {
		t.Fatalf("calls=%q error=%v", calls, err)
	}
	starts, err := os.ReadFile(filepath.Join(directory, "starts"))
	if err != nil || string(starts) != "1\n" {
		t.Fatalf("starts=%q error=%v", starts, err)
	}
}

func TestRuntimeDispatchDoesNotHoldMutex(t *testing.T) {
	directory := t.TempDir()
	runtime := lazyRuntime(t, directory, "BRIGADE_TEST_MCP_MODE=hold-call")
	done := make(chan error, 1)
	go func() {
		_, err := runtime.Dispatch(t.Context(), "tools/call", []byte(`{"name":"block"}`))
		done <- err
	}()
	waitRuntimeFile(t, directory, "calls")
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := runtime.Dispatch(ctx, "ping", nil); err != nil {
		t.Fatalf("in-flight call blocked ping: %v", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- runtime.Close() }()
	if err := runtimeDispatchResult(t, closed); err != nil {
		t.Fatal(err)
	}
	if err := runtimeDispatchResult(t, done); err == nil {
		t.Fatal("closed in-flight call should fail")
	}
}

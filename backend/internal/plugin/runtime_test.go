package plugin

import (
	"context"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

func TestRuntimeReportsProcessStderr(t *testing.T) {
	server := acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{
		Command: "sh",
		Args:    []string{"-c", "echo 'missing runtime dependency' >&2; exit 1"},
	}}
	runtime, err := NewRuntime(server, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	_, err = runtime.Dispatch(context.Background(), "ping", nil)
	if err == nil || !strings.Contains(err.Error(), "missing runtime dependency") {
		t.Fatalf("Dispatch() error = %v", err)
	}
}

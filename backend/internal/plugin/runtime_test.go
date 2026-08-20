package plugin

import (
	"context"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

func TestStartRuntimeReportsProcessStderr(t *testing.T) {
	server := acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{
		Command: "sh",
		Args:    []string{"-c", "echo 'missing runtime dependency' >&2; exit 1"},
	}}
	_, err := StartRuntime(context.Background(), server, t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "missing runtime dependency") {
		t.Fatalf("StartRuntime() error = %v", err)
	}
}

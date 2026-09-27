package acp

import (
	"context"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/grigory51/brigade/backend/internal/agui"
)

func TestRequestPermissionCommand(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{name: "command", input: map[string]any{"command": "npm test"}, want: "npm test"},
		{name: "cmd", input: map[string]any{"cmd": "ls"}, want: "ls"},
		{name: "Codex envelope", input: map[string]any{"server": "terminal", "arguments": map[string]any{"command": "go test ./..."}}, want: "go test ./..."},
		{name: "no command", input: map[string]any{"path": "src/main.go"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got agui.PermissionRequest
			c := &Client{resolver: func(_ context.Context, req agui.PermissionRequest) (string, error) {
				got = req
				return "allow", nil
			}}
			_, err := c.RequestPermission(context.Background(), acpsdk.RequestPermissionRequest{
				ToolCall: acpsdk.ToolCallUpdate{RawInput: tt.input},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.Command != tt.want {
				t.Errorf("command = %q, want %q", got.Command, tt.want)
			}
		})
	}
}

func TestTrustedBrigadeFrontendTool(t *testing.T) {
	dotTitle := "mcp.brigade.save_note"
	claudeTitle := "mcp__brigade__render_ui"
	foreignTitle := "mcp.evil.save_note"
	tests := []struct {
		name string
		call acpsdk.ToolCallUpdate
		want bool
	}{
		{name: "codex title", call: acpsdk.ToolCallUpdate{Title: &dotTitle}, want: true},
		{name: "claude title", call: acpsdk.ToolCallUpdate{Title: &claudeTitle}, want: true},
		{name: "codex envelope", call: acpsdk.ToolCallUpdate{RawInput: map[string]any{"server": "brigade", "tool": "show_choice"}}, want: true},
		{name: "foreign server", call: acpsdk.ToolCallUpdate{Title: &foreignTitle}, want: false},
		{name: "unknown brigade tool", call: acpsdk.ToolCallUpdate{RawInput: map[string]any{"server": "brigade", "tool": "shell"}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trustedBrigadeFrontendTool(tt.call); got != tt.want {
				t.Fatalf("trustedBrigadeFrontendTool() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfigValueAutoApproves(t *testing.T) {
	for _, value := range []string{"agent-full-access", "bypassPermissions"} {
		if !ConfigValueAutoApproves("mode", value) {
			t.Errorf("mode=%s must auto-approve", value)
		}
	}
	if ConfigValueAutoApproves("mode", "agent") || ConfigValueAutoApproves("model", "agent-full-access") {
		t.Error("ordinary config must not auto-approve")
	}
}

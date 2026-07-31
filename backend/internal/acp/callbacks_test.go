package acp

import (
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

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

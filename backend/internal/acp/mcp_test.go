package acp

import "testing"

func TestBrigadeMCPServerReceivesSessionID(t *testing.T) {
	server := BrigadeMCPServer("/opt/brigade-tools.mjs", "session-1")
	if server.Stdio == nil || len(server.Stdio.Env) != 1 ||
		server.Stdio.Env[0].Name != "BRIGADE_SESSION_ID" || server.Stdio.Env[0].Value != "session-1" {
		t.Fatalf("BrigadeMCPServer() env = %#v", server.Stdio)
	}
}

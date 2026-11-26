package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPToolsResourceAndSharedState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	workspace := t.TempDir()
	connect := func(store *stateStore) *mcp.ClientSession {
		t.Helper()
		server := newServer(store, "<!doctype html><title>Coder</title>")
		serverTransport, clientTransport := mcp.NewInMemoryTransports()
		serverSession, err := server.Connect(ctx, serverTransport, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { serverSession.Close() })
		client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
		session, err := client.Connect(ctx, clientTransport, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { session.Close() })
		return session
	}
	agent, host := connect(testStore(t, workspace)), connect(testStore(t, workspace))
	listed, err := host.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 9 {
		t.Fatalf("tools: %#v, %v", listed, err)
	}
	foundEntry := false
	for _, tool := range listed.Tools {
		if tool.Name == "coder.open" {
			foundEntry = true
			data, _ := json.Marshal(tool.Meta)
			if string(data) != `{"ui":{"resourceUri":"ui://coder/app.html"}}` {
				t.Fatalf("entry metadata: %s", data)
			}
		}
	}
	if !foundEntry {
		t.Fatal("missing entry tool")
	}
	resource, err := host.ReadResource(ctx, &mcp.ReadResourceParams{URI: appURI})
	if err != nil || len(resource.Contents) != 1 || resource.Contents[0].MIMEType != "text/html;profile=mcp-app" {
		t.Fatalf("resource: %#v, %v", resource, err)
	}
	result, err := agent.CallTool(ctx, &mcp.CallToolParams{Name: "coder.register_result", Arguments: Result{Name: "site", Kind: WebResult, URL: "https://preview.example.test"}})
	if err != nil || result.IsError {
		t.Fatalf("register: %#v, %v", result, err)
	}
	result, err = host.CallTool(ctx, &mcp.CallToolParams{Name: "coder.open", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("open: %#v, %v", result, err)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil || state.Revision != 1 || len(state.Results) != 1 {
		t.Fatalf("shared state: %s, %v", data, err)
	}
	result, err = agent.CallTool(ctx, &mcp.CallToolParams{Name: "coder.register_result", Arguments: Result{Name: "bad", Kind: "invalid"}})
	if err != nil || !result.IsError {
		t.Fatalf("validation must be an MCP tool error: %#v, %v", result, err)
	}
}

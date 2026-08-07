package store

import (
	"context"
	"testing"
)

func TestAgentConnectionsKeepSeparateSecrets(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	for _, item := range []AgentConnection{
		{ID: "personal", UserID: "u1", Name: "Personal", AgentType: "codex", AuthProfile: "chatgpt", Secret: "one"},
		{ID: "work", UserID: "u1", Name: "Work", AgentType: "codex", AuthProfile: "chatgpt", Secret: "two"},
	} {
		if err := st.SaveAgentConnection(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	items, err := st.ListAgentConnections(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Secret != "one" || items[1].Secret != "two" {
		t.Fatalf("connections: %+v", items)
	}
	items[0].Name, items[0].Secret = "Renamed", ""
	if err := st.SaveAgentConnection(ctx, items[0]); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetAgentConnection(ctx, "u1", "personal")
	if err != nil || got.Name != "Renamed" || got.Secret != "one" {
		t.Fatalf("updated connection: %+v err=%v", got, err)
	}
}

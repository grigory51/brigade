package acpdaemon

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/acp"
	"github.com/grigory51/brigade/backend/internal/agui"
)

func TestStatusIncludesPendingPermissions(t *testing.T) {
	d, err := New("s1", "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.client = &acp.Client{}
	d.perms.register(agui.PermissionRequest{
		ID: "p1", Title: "Run command",
		Options: []agui.PermissionOption{{OptionID: "allow", Kind: "allow_once"}},
	})

	resp, err := (&service{d: d}).Status(context.Background(), connect.NewRequest(&v1.Empty{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.PendingPermissionsJson) != 1 {
		t.Fatalf("pending permissions = %d, want 1", len(resp.Msg.PendingPermissionsJson))
	}
	var pending agui.PermissionRequest
	if err := json.Unmarshal(resp.Msg.PendingPermissionsJson[0], &pending); err != nil {
		t.Fatal(err)
	}
	if pending.ID != "p1" || pending.Title != "Run command" {
		t.Fatalf("pending permission = %+v", pending)
	}
}

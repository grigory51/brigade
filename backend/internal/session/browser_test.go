package session

import (
	"context"
	"testing"
	"time"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/store"
)

func TestBrowserRequiresSessionOwnerAndLiveAgent(t *testing.T) {
	r := newTestRegistry(t)
	if err := r.store.CreateSession(context.Background(), store.Session{ID: "browser", UserID: "u1", Mode: store.SessionModeLocal, Kind: store.SessionKindACP, Status: store.SessionStatusRunning, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	req := &v1.BrowserRequest{SessionId: "browser", RequestId: "request", Action: v1.BrowserAction_BROWSER_ACTION_STATUS}
	if _, err := r.BrowserInteract(context.Background(), "u2", req); err == nil {
		t.Fatal("foreign user accepted")
	}
	if _, err := r.BrowserInteract(context.Background(), "u1", req); err == nil {
		t.Fatal("stopped session accepted")
	}
}

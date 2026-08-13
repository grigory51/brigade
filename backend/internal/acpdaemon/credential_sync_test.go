package acpdaemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/gen/go/brigade/v1/brigadev1connect"
)

type credentialSyncHandler struct {
	brigadev1connect.UnimplementedAgentBridgeServiceHandler
	requests chan *v1.SyncCredentialRequest
}

func (h *credentialSyncHandler) SyncCredential(_ context.Context, req *connect.Request[v1.SyncCredentialRequest]) (*connect.Response[v1.SyncCredentialResponse], error) {
	h.requests <- req.Msg
	return connect.NewResponse(&v1.SyncCredentialResponse{Current: req.Msg.Current}), nil
}

func TestCredentialSyncWatchesAtomicReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := writeCredential(path, []byte("old")); err != nil {
		t.Fatal(err)
	}
	handler := &credentialSyncHandler{requests: make(chan *v1.SyncCredentialRequest, 1)}
	mux := http.NewServeMux()
	prefix, bridge := brigadev1connect.NewAgentBridgeServiceHandler(handler)
	mux.Handle(prefix, bridge)
	server := httptest.NewServer(mux)
	defer server.Close()

	sync := newCredentialSync(path, []string{
		"BRIGADE_API_URL=" + server.URL,
		"BRIGADE_SESSION_ID=session",
		"BRIGADE_SESSION_TOKEN=token",
	})
	if sync == nil {
		t.Fatal("credential sync is disabled")
	}
	defer sync.close()
	if err := writeCredential(path, []byte("new")); err != nil {
		t.Fatal(err)
	}

	select {
	case req := <-handler.requests:
		if string(req.Previous) != "old" || string(req.Current) != "new" {
			t.Fatalf("sync = %q -> %q", req.Previous, req.Current)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("credential change was not synced")
	}
}

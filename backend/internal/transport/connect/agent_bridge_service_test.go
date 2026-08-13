package connectsvc

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/memory"
	"github.com/grigory51/brigade/backend/internal/preview"
	"github.com/grigory51/brigade/backend/internal/store"
)

const testSessionID = "8c19e13f-1d52-42bb-88ed-443704e83af6"

func newBridge() (*AgentBridgeService, *preview.Service) {
	p := preview.NewService(preview.Config{Enabled: true, Domain: "localhost", Scheme: "http", ListenPort: 10000}, []byte("secret"))
	return NewAgentBridgeService(p, memory.NewService("", nil, nil), nil), p
}

func TestSyncCredentialRejectsStaleUpdate(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.DB().Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES ('u1', 'user', '', 0)`); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAgentConnection(ctx, store.AgentConnection{ID: "agent-1", UserID: "u1", Name: "Agent", AgentType: "codex", AuthProfile: "chatgpt", Secret: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, store.Session{ID: testSessionID, UserID: "u1", Mode: store.SessionModeDocker, Kind: store.SessionKindACP, AgentType: "codex", AuthProfile: "agent-1", Status: store.SessionStatusRunning, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	p := preview.NewService(preview.Config{}, []byte("secret"))
	svc := NewAgentBridgeService(p, nil, st)
	sync := func(previous, current string) string {
		req := connect.NewRequest(&v1.SyncCredentialRequest{SessionId: testSessionID, Previous: []byte(previous), Current: []byte(current)})
		req.Header().Set("Authorization", "Bearer "+p.TokenFor(testSessionID))
		resp, err := svc.SyncCredential(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		return string(resp.Msg.Current)
	}
	if got := sync("old", "new"); got != "new" {
		t.Fatalf("first sync = %q", got)
	}
	if got := sync("old", "stale"); got != "new" {
		t.Fatalf("stale sync = %q, want authoritative new", got)
	}
	connection, err := st.GetAgentConnection(ctx, "u1", "agent-1")
	if err != nil || connection.Secret != "new" {
		t.Fatalf("stored credential = %q, err=%v", connection.Secret, err)
	}
}

func registerReq(sessionID, token string, port int32) *connect.Request[v1.RegisterPreviewRequest] {
	req := connect.NewRequest(&v1.RegisterPreviewRequest{SessionId: sessionID, Port: port, Name: "vite"})
	if token != "" {
		req.Header().Set("Authorization", "Bearer "+token)
	}
	return req
}

func TestRegisterPreview(t *testing.T) {
	svc, p := newBridge()
	ctx := context.Background()
	good := p.TokenFor(testSessionID)

	t.Run("неверный токен → Unauthenticated", func(t *testing.T) {
		_, err := svc.RegisterPreview(ctx, registerReq(testSessionID, "wrong", 3000))
		if connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("code = %v, want Unauthenticated", connect.CodeOf(err))
		}
	})

	t.Run("токен другой сессии → Unauthenticated", func(t *testing.T) {
		other := p.TokenFor("00000000-0000-0000-0000-000000000000")
		_, err := svc.RegisterPreview(ctx, registerReq(testSessionID, other, 3000))
		if connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("code = %v, want Unauthenticated", connect.CodeOf(err))
		}
	})

	t.Run("порт вне диапазона → InvalidArgument", func(t *testing.T) {
		_, err := svc.RegisterPreview(ctx, registerReq(testSessionID, good, 0))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("code = %v, want InvalidArgument", connect.CodeOf(err))
		}
	})

	t.Run("валидный токен → url и регистрация", func(t *testing.T) {
		resp, err := svc.RegisterPreview(ctx, registerReq(testSessionID, good, 3000))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Msg.Url == "" {
			t.Fatal("пустой url")
		}
		if len(p.List(testSessionID)) != 1 {
			t.Fatal("preview не зарегистрирован")
		}
	})
}

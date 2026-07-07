package connectsvc

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/memory"
	"github.com/grigory51/brigade/backend/internal/preview"
)

const testSessionID = "8c19e13f-1d52-42bb-88ed-443704e83af6"

func newBridge() (*AgentBridgeService, *preview.Service) {
	p := preview.NewService(preview.Config{Enabled: true, Domain: "localhost", Scheme: "http", ListenPort: 10000}, []byte("secret"))
	return NewAgentBridgeService(p, memory.NewService("", nil), nil), p
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

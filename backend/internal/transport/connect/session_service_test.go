package connectsvc

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/auth"
	"github.com/grigory51/brigade/backend/internal/store"
)

func TestCreateRejectsOnDemand(t *testing.T) {
	req := connect.NewRequest(&v1.CreateSessionRequest{Kind: v1.SessionKind_SESSION_KIND_ACP, ExecutionPolicy: v1.SessionExecutionPolicy_SESSION_EXECUTION_POLICY_ON_DEMAND})
	token, _, err := auth.NewJWT("test", time.Hour).Issue("owner", "owner", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req.Header().Set("Authorization", "Bearer "+token)
	handler := auth.NewService(nil, "test", time.Hour, time.Hour).Interceptor().WrapUnary(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return (&SessionService{}).Create(ctx, req)
	})
	if _, err := handler(t.Context(), req); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid argument, got %v", err)
	}
}

func TestAgentOutdated(t *testing.T) {
	for _, test := range []struct {
		brigade string
		agent   string
		want    bool
	}{
		{"v0.54.0", "v0.52.0", true},
		{"v0.54.0", "v0.54.0", false},
		{"v0.54.0", "", false},
		{"dev", "v0.54.0", false},
	} {
		svc := SessionService{version: test.brigade}
		got := svc.sessionToProto(store.Session{AgentVersion: test.agent})
		if got.AgentVersion != test.agent || got.AgentOutdated != test.want {
			t.Fatalf("brigade=%q, agent=%q: version=%q outdated=%v, want outdated=%v", test.brigade, test.agent, got.AgentVersion, got.AgentOutdated, test.want)
		}
	}
}

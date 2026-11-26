package connectsvc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/auth"
	"github.com/grigory51/brigade/backend/internal/plugin"
	"github.com/grigory51/brigade/backend/internal/session"
	"github.com/grigory51/brigade/backend/internal/store"
)

type pluginCallerFunc func(context.Context, string, string, string, []byte, *plugin.Reference) ([]byte, error)

func (f pluginCallerFunc) PluginMCP(ctx context.Context, sessionID, userID, method string, params []byte, expected *plugin.Reference) ([]byte, error) {
	return f(ctx, sessionID, userID, method, params, expected)
}

func TestPluginMCPReferenceAndErrorCode(t *testing.T) {
	if _, err := (&PluginService{}).MCP(t.Context(), connect.NewRequest(&v1.PluginMCPRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated = %v", err)
	}
	for _, tc := range []struct {
		name string
		ref  *v1.PluginReference
		err  error
	}{
		{name: "legacy"},
		{name: "selected", ref: &v1.PluginReference{Id: "app", Version: "1"}},
		{name: "empty present", ref: &v1.PluginReference{}},
		{name: "stale", ref: &v1.PluginReference{Id: "old", Version: "1"}, err: fmt.Errorf("pane: %w", session.ErrPluginSelectionChanged)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			svc := &PluginService{session: pluginCallerFunc(func(_ context.Context, sessionID, userID, method string, params []byte, expected *plugin.Reference) ([]byte, error) {
				calls++
				if sessionID != "session" || userID != "owner" || method != "tools/list" || string(params) != "{}" {
					t.Errorf("unexpected dispatch arguments")
				}
				var want *plugin.Reference
				if tc.ref != nil {
					want = &plugin.Reference{ID: tc.ref.Id, Version: tc.ref.Version}
				}
				if !reflect.DeepEqual(expected, want) {
					t.Errorf("reference=%+v, want %+v", expected, want)
				}
				return []byte(`{"tools":[]}`), tc.err
			})}
			req := connect.NewRequest(&v1.PluginMCPRequest{SessionId: "session", Method: "tools/list", ParamsJson: []byte("{}"), ExpectedPlugin: tc.ref})
			token, _, err := auth.NewJWT("test", time.Hour).Issue("owner", "owner", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			req.Header().Set("Authorization", "Bearer "+token)
			handler := auth.NewService(nil, "test", time.Hour, time.Hour).Interceptor().WrapUnary(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) { return svc.MCP(ctx, req) })
			response, err := handler(t.Context(), req)
			if calls != 1 {
				t.Fatalf("dispatch calls=%d", calls)
			}
			if tc.err != nil {
				if connect.CodeOf(err) != connect.CodeFailedPrecondition || !errors.Is(err, session.ErrPluginSelectionChanged) {
					t.Fatalf("stale error=%v", err)
				}
			} else if err != nil || string(response.Any().(*v1.PluginMCPResponse).ResultJson) != `{"tools":[]}` {
				t.Fatalf("response=%v error=%v", response, err)
			}
		})
	}
}

func TestPluginAPINormalizesLegacyLayouts(t *testing.T) {
	for _, layout := range []string{"", `,"layout":""`, `,"layout":"full"`, `,"layout":"split"`, `,"layout":"unknown"`} {
		t.Run(layout, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "test.db"), nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			bundle := t.TempDir()
			if err := os.WriteFile(filepath.Join(bundle, "server.py"), []byte("pass"), 0o600); err != nil {
				t.Fatal(err)
			}
			manifest := `{"manifest_version":"0.3","name":"app","version":"1","server":{"type":"python","entry_point":"server.py"},"_meta":{"brigade":{"experience":{"entry_tool":"app.open"` + layout + `}}}}`
			owner := "owner"
			if err := st.PutPlugin(t.Context(), store.Plugin{OwnerID: owner, ID: "app", Name: "App", Version: "1", Target: "portable", BundlePath: bundle, ManifestJSON: manifest, InstalledAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateSession(t.Context(), store.Session{ID: "session", UserID: owner, Mode: store.SessionModeLocal, Kind: store.SessionKindACP, Status: store.SessionStatusRunning, CreatedAt: time.Now(), ExperienceID: "app", ExperienceVersion: "1", ExperienceOwnerID: &owner}); err != nil {
				t.Fatal(err)
			}
			svc := NewPluginService(st, plugin.New(t.TempDir(), st), nil, plugin.RuntimeTarget(false))
			req := connect.NewRequest(&v1.ListPluginsRequest{})
			token, _, err := auth.NewJWT("test", time.Hour).Issue(owner, owner, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			req.Header().Set("Authorization", "Bearer "+token)
			handler := auth.NewService(nil, "test", time.Hour, time.Hour).Interceptor().WrapUnary(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
				listed, err := svc.List(ctx, req)
				if err != nil {
					return nil, err
				}
				if len(listed.Msg.Plugins) != 1 || listed.Msg.Plugins[0].Layout != "split" {
					t.Fatalf("List = %+v", listed.Msg)
				}
				got, err := svc.Get(ctx, connect.NewRequest(&v1.GetPluginRequest{SessionId: "session"}))
				if err == nil && got.Msg.Layout != "split" {
					t.Fatalf("Get layout = %q", got.Msg.Layout)
				}
				return got, err
			})
			_, err = handler(t.Context(), req)
			if layout == `,"layout":"unknown"` {
				if connect.CodeOf(err) != connect.CodeInternal {
					t.Fatalf("invalid stored manifest error=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

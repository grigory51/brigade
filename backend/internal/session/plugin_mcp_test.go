package session

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grigory51/brigade/backend/internal/acp"
	"github.com/grigory51/brigade/backend/internal/plugin"
	"github.com/grigory51/brigade/backend/internal/store"
)

type pluginMCPTestClient struct {
	fakeACPSession
	alive    func() bool
	dispatch func(context.Context, string, []byte) ([]byte, error)
	reserve  func(context.Context) (func(), error)
}

func (c *pluginMCPTestClient) Alive() bool {
	return c.alive == nil || c.alive()
}

func (c *pluginMCPTestClient) PluginMCP(ctx context.Context, method string, params []byte) ([]byte, error) {
	return c.dispatch(ctx, method, params)
}

func (c *pluginMCPTestClient) ReserveIdle(ctx context.Context) (func(), error) {
	return c.reserve(ctx)
}

func pluginMCPTestRegistry(t *testing.T, client acpSession) (*Registry, store.Session) {
	t.Helper()
	r := newTestRegistry(t)
	owner := "owner"
	sess := store.Session{ID: "session", UserID: owner, Kind: store.SessionKindACP,
		Mode: store.SessionModeLocal, Status: store.SessionStatusRunning, AgentSessionID: "agent-session",
		ExperienceID: "app", ExperienceVersion: "1", ExperienceOwnerID: &owner, CreatedAt: time.Now()}
	if err := r.store.CreateSession(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	r.live[sess.ID] = &live{owner: owner, kind: sess.Kind, mode: sess.Mode, client: client}
	return r, sess
}

func TestPluginMCPExpectedSelection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expected *plugin.Reference
		clear    bool
		foreign  bool
		wantErr  error
	}{
		{name: "legacy"},
		{name: "matching", expected: &plugin.Reference{ID: "app", Version: "1"}},
		{name: "other id", expected: &plugin.Reference{ID: "old", Version: "1"}, wantErr: ErrPluginSelectionChanged},
		{name: "other version", expected: &plugin.Reference{ID: "app", Version: "0"}, wantErr: ErrPluginSelectionChanged},
		{name: "missing version", expected: &plugin.Reference{ID: "app"}, wantErr: ErrPluginSelectionChanged},
		{name: "empty reference", expected: &plugin.Reference{}, wantErr: ErrPluginSelectionChanged},
		{name: "cleared selection", expected: &plugin.Reference{ID: "app", Version: "1"}, clear: true, wantErr: ErrPluginSelectionChanged},
		{name: "foreign session", foreign: true, wantErr: store.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			checks := 0
			client := &pluginMCPTestClient{alive: func() bool { checks++; return true }, dispatch: func(_ context.Context, method string, params []byte) ([]byte, error) {
				calls++
				if method != "tools/list" || string(params) != "{}" {
					t.Errorf("dispatch = %s %s", method, params)
				}
				return []byte(`{"tools":[]}`), nil
			}}
			r, sess := pluginMCPTestRegistry(t, client)
			if tc.clear {
				if err := r.store.UpdateSessionExperience(t.Context(), sess.ID, "", "", nil); err != nil {
					t.Fatal(err)
				}
				delete(r.live, sess.ID)
			}
			userID := sess.UserID
			if tc.foreign {
				userID = "other"
			}
			result, err := r.PluginMCP(t.Context(), sess.ID, userID, "tools/list", []byte("{}"), tc.expected)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil && (calls != 0 || checks != 0) {
				t.Fatalf("rejected request dispatched %d times and reached EnsureACPClient %d times", calls, checks)
			}
			if tc.wantErr == nil && (calls != 1 || string(result) != `{"tools":[]}`) {
				t.Fatalf("calls=%d result=%s", calls, result)
			}
		})
	}
}

func TestPluginMCPRechecksSessionAndLiveAfterEnsure(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "replaced client", true: "changed selection"}[changed], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			ensured := make(chan struct{})
			continueEnsure := make(chan struct{})
			var oldCalls, newCalls atomic.Int32
			old := &pluginMCPTestClient{
				alive: func() bool {
					close(ensured)
					select {
					case <-continueEnsure:
						return true
					case <-ctx.Done():
						return false
					}
				},
				dispatch: func(context.Context, string, []byte) ([]byte, error) { oldCalls.Add(1); return nil, nil },
			}
			r, sess := pluginMCPTestRegistry(t, old)
			done := make(chan error, 1)
			go func() {
				_, err := r.PluginMCP(ctx, sess.ID, sess.UserID, "ping", nil, &plugin.Reference{ID: "app", Version: "1"})
				done <- err
			}()
			select {
			case <-ensured:
			case <-ctx.Done():
				t.Fatal("Ensure did not complete before acquiring the transition lock")
			}
			unlock := r.lockSession(sess.ID)
			defer func() {
				if unlock != nil {
					unlock()
				}
			}()
			if changed {
				if err := r.store.UpdateSessionExperience(ctx, sess.ID, "app", "2", sess.ExperienceOwnerID); err != nil {
					t.Fatal(err)
				}
			}
			r.mu.Lock()
			r.live[sess.ID] = &live{owner: sess.UserID, kind: sess.Kind, mode: sess.Mode, client: &pluginMCPTestClient{
				dispatch: func(context.Context, string, []byte) ([]byte, error) { newCalls.Add(1); return []byte("{}"), nil },
			}}
			r.mu.Unlock()
			close(continueEnsure)
			unlock()
			unlock = nil
			select {
			case err := <-done:
				if changed && !errors.Is(err, ErrPluginSelectionChanged) || !changed && err != nil {
					t.Fatalf("changed=%v error=%v", changed, err)
				}
			case <-ctx.Done():
				t.Fatal("PluginMCP did not finish")
			}
			wantCalls := int32(1)
			if changed {
				wantCalls = 0
			}
			if oldCalls.Load() != 0 || newCalls.Load() != wantCalls {
				t.Fatalf("dispatch old=%d new=%d", oldCalls.Load(), newCalls.Load())
			}
		})
	}
}

func TestPluginMCPEnsureCanAcquireSessionLock(t *testing.T) {
	var checks atomic.Int32
	client := &pluginMCPTestClient{
		alive:    func() bool { return checks.Add(1) > 1 },
		dispatch: func(context.Context, string, []byte) ([]byte, error) { return []byte("{}"), nil },
	}
	r, sess := pluginMCPTestRegistry(t, client)
	done := make(chan error, 1)
	go func() { _, err := r.PluginMCP(t.Context(), sess.ID, sess.UserID, "ping", nil, nil); done <- err }()
	select {
	case err := <-done:
		if err != nil || checks.Load() != 2 {
			t.Fatalf("error=%v alive checks=%d", err, checks.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PluginMCP deadlocked with EnsureACPClient")
	}
}

func TestPluginMCPSerializesExperienceTransition(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	entered, release, reserved := make(chan struct{}), make(chan struct{}), make(chan struct{})
	client := &pluginMCPTestClient{
		dispatch: func(ctx context.Context, _ string, _ []byte) ([]byte, error) {
			close(entered)
			select {
			case <-release:
				return []byte("{}"), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		reserve: func(context.Context) (func(), error) { close(reserved); return nil, acp.ErrSessionBusy },
	}
	r, sess := pluginMCPTestRegistry(t, client)
	callDone, transitionDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := r.PluginMCP(ctx, sess.ID, sess.UserID, "ping", nil, nil); callDone <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("dispatch did not start")
	}
	lock := r.sessionLock(sess.ID)
	if lock.TryLock() {
		lock.Unlock()
		t.Fatal("dispatch does not hold the transition lock")
	}
	go func() { _, err := r.SetExperience(ctx, sess.ID, sess.UserID, ""); transitionDone <- err }()
	select {
	case <-reserved:
		t.Fatal("transition entered ReserveIdle during dispatch")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-callDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("dispatch did not finish")
	}
	select {
	case err := <-transitionDone:
		if !errors.Is(err, acp.ErrSessionBusy) {
			t.Fatalf("transition error=%v", err)
		}
	case <-ctx.Done():
		t.Fatal("transition remained blocked")
	}
}

func TestSetExperienceSameIDDoesNotReserveIdle(t *testing.T) {
	client := &pluginMCPTestClient{reserve: func(context.Context) (func(), error) {
		t.Error("same-ID selection attempted ReserveIdle")
		return nil, acp.ErrSessionBusy
	}}
	client.generating = true
	r, sess := pluginMCPTestRegistry(t, client)
	original := r.live[sess.ID]
	got, err := r.SetExperience(t.Context(), sess.ID, sess.UserID, sess.ExperienceID)
	if err != nil || got.ExperienceVersion != sess.ExperienceVersion || r.live[sess.ID] != original {
		t.Fatalf("same-ID selection changed runtime: session=%+v error=%v", got, err)
	}
	if _, err := r.SetExperience(t.Context(), sess.ID, "other", sess.ExperienceID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("same-ID selection skipped ownership check: %v", err)
	}
}

func TestSetExperienceSameIDRejectsInvalidSession(t *testing.T) {
	for _, scenario := range []string{"missing live", "foreign live", "missing client", "different native session", "stopped", "CLI", "on demand", "missing session"} {
		t.Run(scenario, func(t *testing.T) {
			client := &pluginMCPTestClient{reserve: func(context.Context) (func(), error) {
				t.Error("invalid session reached ReserveIdle")
				return nil, acp.ErrSessionBusy
			}}
			r, sess := pluginMCPTestRegistry(t, client)
			wantErr := ErrExperienceNotIdleACP
			switch scenario {
			case "missing live":
				delete(r.live, sess.ID)
			case "foreign live":
				r.live[sess.ID].owner = "other"
			case "missing client":
				r.live[sess.ID].client = nil
			case "missing session":
				sess.ID = "missing"
				wantErr = store.ErrNotFound
			default:
				query := map[string]string{
					"different native session": "UPDATE sessions SET agent_session_id = 'different' WHERE id = ?",
					"stopped":                  "UPDATE sessions SET status = 'stopped' WHERE id = ?",
					"CLI":                      "UPDATE sessions SET kind = 'cli' WHERE id = ?",
					"on demand":                "UPDATE sessions SET execution_policy = 'on_demand' WHERE id = ?",
				}[scenario]
				if _, err := r.store.DB().ExecContext(t.Context(), query, sess.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.SetExperience(t.Context(), sess.ID, sess.UserID, sess.ExperienceID); !errors.Is(err, wantErr) {
				t.Fatalf("same-ID invalid session error=%v, want %v", err, wantErr)
			}
		})
	}
}

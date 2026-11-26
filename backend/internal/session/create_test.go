package session

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grigory51/brigade/backend/internal/spawn"
	"github.com/grigory51/brigade/backend/internal/store"
)

// Docker and daemon requests stay inside httptest; no host Docker socket is used.
func TestCreateRollsBackPartialACPDaemon(t *testing.T) {
	for _, scenario := range []string{"configure failure", "cancelled request", "options failure", "remove failure"} {
		t.Run(scenario, func(t *testing.T) {
			r := newTestRegistry(t)
			r.mode = store.SessionModeDocker
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if err := r.store.SaveAgentConnection(ctx, store.AgentConnection{ID: "claude-token", UserID: "u1", AgentType: "claude-code", Secret: "test", CreatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			experienceID := ""
			if scenario == "options failure" {
				experienceID = "app"
				manifest := `{"manifest_version":"0.3","name":"app","version":"1.0.0","server":{"type":"binary","entry_point":"app"},"_meta":{"brigade":{"experience":{"entry_tool":"app.open"}}}}`
				if err := r.store.PutPlugin(ctx, store.Plugin{OwnerID: "u1", ID: experienceID, Name: experienceID, Version: "1.0.0", Target: "portable", BundlePath: t.TempDir(), ManifestJSON: manifest, InstalledAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			var mu sync.Mutex
			var sessionID string
			created, removed, configured := false, false, false
			daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "/Configure") {
					mu.Lock()
					configured = true
					mu.Unlock()
					if scenario == "cancelled request" {
						cancel()
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"code":"internal","message":"configure rejected"}`))
				}
			}))
			defer daemon.Close()
			_, daemonPort, err := net.SplitHostPort(strings.TrimPrefix(daemon.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			docker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				path := req.URL.Path
				if strings.HasPrefix(path, "/v") {
					if offset := strings.Index(path[1:], "/"); offset >= 0 {
						path = path[offset+1:]
					}
				}
				w.Header().Set("Content-Type", "application/json")
				var response any = map[string]any{}
				switch {
				case path == "/_ping":
					w.Header().Set("API-Version", "1.45")
				case strings.HasPrefix(path, "/networks/"):
					response = map[string]string{"Id": "network", "Name": "brigade-agents"}
				case strings.HasPrefix(path, "/images/"):
					response = map[string]string{"Id": "sha256:0123456789abcdef"}
				case path == "/volumes":
					response = map[string]any{"Volumes": []any{}}
				case strings.HasPrefix(path, "/volumes/") && req.Method != http.MethodDelete:
					response = map[string]string{"Name": "test-volume"}
				case path == "/containers/create":
					var config struct{ Labels map[string]string }
					if err := json.NewDecoder(req.Body).Decode(&config); err != nil {
						t.Error(err)
					}
					sessionID = config.Labels["brigade.session.id"]
					created = true
					response = map[string]string{"Id": "provisional-container"}
				case path == "/containers/provisional-container/start":
					if scenario == "options failure" {
						if err := r.store.DeletePlugin(t.Context(), "u1", experienceID); err != nil {
							t.Error(err)
						}
					}
				case path == "/containers/provisional-container/json":
					response = map[string]any{"Id": "provisional-container", "NetworkSettings": map[string]any{"Networks": map[string]any{"brigade-agents": map[string]string{}}, "Ports": map[string]any{"8787/tcp": []any{map[string]string{"HostPort": daemonPort}}}}}
				case path == "/containers/json":
					response = []any{}
					if created && !removed && strings.Contains(req.URL.Query().Get("filters"), "brigade.session.id="+sessionID) {
						response = []any{map[string]string{"Id": "provisional-container"}}
					}
				case path == "/containers/provisional-container" && req.Method == http.MethodDelete:
					if req.URL.Query().Get("v") == "1" || req.URL.Query().Get("v") == "true" {
						t.Error("cleanup must not delete volumes")
					}
					if scenario == "remove failure" {
						w.WriteHeader(http.StatusInternalServerError)
						response = map[string]string{"message": "remove rejected"}
					} else {
						removed = true
					}
				case strings.HasPrefix(path, "/containers/"):
					w.WriteHeader(http.StatusNotFound)
					response = map[string]string{"message": "No such container"}
				default:
					t.Errorf("unexpected Docker request: %s %s", req.Method, path)
					w.WriteHeader(http.StatusBadRequest)
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer docker.Close()
			t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(docker.URL, "http://"))
			t.Setenv("DOCKER_TLS_VERIFY", "")
			t.Setenv("DOCKER_CERT_PATH", "")
			t.Setenv("DOCKER_API_VERSION", "1.45")
			spawner, err := spawn.NewDockerSpawner("test-image:1")
			if err != nil {
				t.Fatal(err)
			}
			defer spawner.Close()
			r.spawner = spawner
			_, err = r.Create(ctx, "u1", store.SessionKindACP, "claude-code", "claude-token", "", "", nil, "", "", "", "", experienceID)
			if err == nil {
				t.Fatal("Create should fail")
			}
			mu.Lock()
			defer mu.Unlock()
			if !created || removed != (scenario != "remove failure") || configured != (scenario != "options failure") {
				t.Fatalf("created=%v removed=%v configured=%v error=%v", created, removed, configured, err)
			}
			if scenario == "remove failure" && !strings.Contains(err.Error(), "remove rejected") {
				t.Fatalf("cleanup error lost: %v", err)
			}
			if _, err := r.store.GetSession(t.Context(), sessionID); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("provisional row remains: %v", err)
			}
			if rows, err := r.List(t.Context(), "u1"); err != nil || len(rows) != 0 {
				t.Fatalf("failed create visible: %+v %v", rows, err)
			}
		})
	}
}

type createTestSpawner struct {
	spawn.Spawner
	spawn    func(context.Context, spawn.Spec) (spawn.Handle, error)
	reattach func(context.Context, spawn.Persisted) (spawn.Handle, error)
}

func (s createTestSpawner) Spawn(ctx context.Context, spec spawn.Spec) (spawn.Handle, error) {
	return s.spawn(ctx, spec)
}
func (s createTestSpawner) Reattach(ctx context.Context, persisted spawn.Persisted) (spawn.Handle, error) {
	return s.reattach(ctx, persisted)
}

func TestCreateRollsBackSpawnFailure(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "cancelled request"}[cancelled], func(t *testing.T) {
			r := newTestRegistry(t)
			r.workDir = t.TempDir()
			spawnErr := errors.New("spawn failed")
			for attempt := 0; attempt < 2; attempt++ {
				ctx, cancel := context.WithCancel(t.Context())
				var id, workspace string
				r.spawner = createTestSpawner{spawn: func(spawnCtx context.Context, spec spawn.Spec) (spawn.Handle, error) {
					id, workspace = spec.SessionID, spec.Cwd
					if cancelled {
						cancel()
					}
					if spawnCtx.Err() != nil {
						t.Fatal("spawn inherited request cancellation")
					}
					if rows, err := r.store.ListSessionsByUser(t.Context(), "u1"); err != nil || len(rows) != 1 {
						t.Fatalf("provisional rows=%v error=%v", rows, err)
					}
					if rows, err := r.List(t.Context(), "u1"); err != nil || len(rows) != 0 {
						t.Fatalf("in-flight session visible: %v error=%v", rows, err)
					}
					if err := os.WriteFile(filepath.Join(workspace, "keep.txt"), []byte("keep"), 0o600); err != nil {
						t.Fatal(err)
					}
					return nil, spawnErr
				}}
				_, err := r.Create(ctx, "u1", store.SessionKindCLI, "claude-code", "", "", "", nil, "", "", "", "", "")
				cancel()
				if !errors.Is(err, spawnErr) {
					t.Fatalf("create: %v", err)
				}
				if _, err := r.store.GetSession(t.Context(), id); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("provisional row remains: %v", err)
				}
				if rows, err := r.List(t.Context(), "u1"); err != nil || len(rows) != 0 {
					t.Fatalf("failed create visible: %v error=%v", rows, err)
				}
				if content, err := os.ReadFile(filepath.Join(workspace, "keep.txt")); err != nil || string(content) != "keep" {
					t.Fatalf("workspace changed: %q %v", content, err)
				}
			}
		})
	}
}

type createCleanupHandle struct {
	*fakeHandle
	cleanup func(context.Context) error
}

func (h *createCleanupHandle) Terminate(ctx context.Context) error {
	return errors.Join(h.fakeHandle.Terminate(ctx), h.cleanup(ctx))
}

func TestCreateRollsBackResumePersistenceFailure(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "database failure", true: "cancelled request"}[cancelled], func(t *testing.T) {
			r := newTestRegistry(t)
			r.workDir = t.TempDir()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			terminateErr := errors.New("terminate failed")
			handle := &createCleanupHandle{fakeHandle: newFakeHandle(), cleanup: func(cleanup context.Context) error {
				if cleanup.Err() != nil {
					t.Fatalf("cleanup was cancelled: %v", cleanup.Err())
				}
				if deadline, ok := cleanup.Deadline(); !ok || time.Until(deadline) > 15*time.Second {
					t.Fatal("cleanup has no bounded timeout")
				}
				return terminateErr
			}}
			var id string
			r.spawner = createTestSpawner{spawn: func(_ context.Context, spec spawn.Spec) (spawn.Handle, error) {
				id = spec.SessionID
				if cancelled {
					cancel()
				} else {
					if _, err := r.store.DB().ExecContext(t.Context(), `CREATE TRIGGER reject_resume BEFORE UPDATE OF agent_session_id ON sessions BEGIN SELECT RAISE(FAIL, 'resume persistence failed'); END`); err != nil {
						t.Fatal(err)
					}
				}
				return handle, nil
			}}
			_, err := r.Create(ctx, "u1", store.SessionKindCLI, "claude-code", "", "", "", nil, "", "", "", "", "")
			if !errors.Is(err, terminateErr) || !handle.isTerminated() {
				t.Fatalf("termination error lost: %v", err)
			}
			if cancelled && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation error lost: %v", err)
			}
			if !cancelled && !strings.Contains(err.Error(), "resume persistence failed") {
				t.Fatalf("persistence error lost: %v", err)
			}
			if _, err := r.store.GetSession(t.Context(), id); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("provisional row remains: %v", err)
			}
		})
	}
}

func TestCreateReportsRollbackFailure(t *testing.T) {
	r := newTestRegistry(t)
	r.workDir = t.TempDir()
	if _, err := r.store.DB().ExecContext(t.Context(), `CREATE TRIGGER reject_delete BEFORE DELETE ON sessions BEGIN SELECT RAISE(FAIL, 'delete failed'); END`); err != nil {
		t.Fatal(err)
	}
	spawnErr := errors.New("spawn failed")
	r.spawner = createTestSpawner{spawn: func(context.Context, spawn.Spec) (spawn.Handle, error) { return nil, spawnErr }}
	_, err := r.Create(t.Context(), "u1", store.SessionKindCLI, "claude-code", "", "", "", nil, "", "", "", "", "")
	if !errors.Is(err, spawnErr) || !strings.Contains(err.Error(), "delete failed") {
		t.Fatalf("rollback error lost: %v", err)
	}
	if rows, err := r.List(t.Context(), "u1"); err != nil || len(rows) != 0 {
		t.Fatalf("uncommitted create visible: %+v %v", rows, err)
	}
}

func TestCreateRollbackPreservesFailedRestore(t *testing.T) {
	r := newTestRegistry(t)
	r.spawner = createTestSpawner{reattach: func(context.Context, spawn.Persisted) (spawn.Handle, error) { return nil, errors.New("restore failed") }}
	sess := store.Session{ID: "established", UserID: "u1", Mode: store.SessionModeLocal, Kind: store.SessionKindCLI, AgentType: "codex", Status: store.SessionStatusRunning, AgentSessionID: "native", Cwd: t.TempDir(), CreatedAt: time.Now()}
	if err := r.store.CreateSession(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	if err := r.RestoreAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err := r.List(t.Context(), "u1")
	if err != nil || len(rows) != 1 || rows[0].Status != store.SessionStatusFailed {
		t.Fatalf("failed restore was removed: %+v error=%v", rows, err)
	}
}

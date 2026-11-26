package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/grigory51/brigade/backend/internal/acp"
	"github.com/grigory51/brigade/backend/internal/store"
)

// TestExperienceAdapterHelper обслуживает только тестовый subprocess. Он не
// запускает модель или пользовательские инструменты.
func TestExperienceAdapterHelper(t *testing.T) {
	if os.Getenv("BRIGADE_TEST_EXPERIENCE_ADAPTER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	var writer *os.File
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage            `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			os.Exit(2)
		}
		if len(request.ID) == 0 {
			continue
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "initialize":
			if os.Getenv("BRIGADE_TEST_EXPERIENCE_MCP") == "1" {
				response["result"] = map[string]any{"protocolVersion": request.Params["protocolVersion"], "capabilities": map[string]any{}, "serverInfo": map[string]string{"name": "fixture", "version": "1"}}
			} else {
				response["result"] = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]bool{"loadSession": true}}
			}
		case "ping":
			response["result"] = map[string]any{}
		case "session/load":
			var err error
			writer, err = os.OpenFile(".native-writer", os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				os.Exit(2)
			}
			if err := syscall.Flock(int(writer.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				response["error"] = map[string]any{"code": -32603, "message": "session already has an active writer"}
			} else if _, err := os.Stat(".fail-all-loads"); err == nil {
				response["error"] = map[string]any{"code": -32603, "message": "rollback load rejected"}
			} else if err := os.Remove(".fail-next-load"); err == nil {
				response["error"] = map[string]any{"code": -32603, "message": "load rejected"}
			} else {
				response["result"] = map[string]any{}
			}
		case "session/prompt":
			response["result"] = map[string]any{"stopReason": "end_turn"}
		default:
			response["error"] = map[string]any{"code": -32601, "message": "unexpected method: " + request.Method}
		}
		if err := encoder.Encode(response); err != nil {
			os.Exit(2)
		}
	}
	runtime.KeepAlive(writer)
	os.Exit(0)
}

func TestSetExperienceLocalPreservesSessionAndRollsBack(t *testing.T) {
	for _, scenario := range []string{"new experience", "plain chat", "load failure", "persist failure", "rollback failure", "busy", "foreign owner", "invalid experience"} {
		t.Run(scenario, func(t *testing.T) {
			r := newTestRegistry(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			adapter := filepath.Join(bin, "claude-agent-acp")
			script := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run='^TestExperienceAdapterHelper$'\n"
			if err := os.WriteFile(adapter, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("BRIGADE_TEST_EXPERIENCE_ADAPTER", "1")
			workspace := t.TempDir()
			file := filepath.Join(workspace, "keep.txt")
			if err := os.WriteFile(file, []byte("workspace content"), 0o600); err != nil {
				t.Fatal(err)
			}
			client, err := acp.New(ctx, acp.Options{Cwd: workspace, AdapterCommand: adapter, ResumeSessionID: "native", StrictResume: true})
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseForReload()
			if scenario == "plain chat" {
				duplicate, err := acp.New(ctx, client.ResumeOptions())
				if err == nil {
					_ = duplicate.CloseForReload()
					t.Fatal("fixture allowed two native writers")
				}
				if !strings.Contains(err.Error(), "active writer") {
					t.Fatalf("exclusive writer fixture: %v", err)
				}
			}
			if _, err := client.Prompt(ctx, "Existing conversation", nil); err != nil {
				t.Fatal(err)
			}
			before, err := client.Messages(ctx)
			if err != nil || len(before) == 0 {
				t.Fatalf("initial history=%v error=%v", before, err)
			}
			owner := "u1"
			sess := store.Session{ID: "session", UserID: owner, Kind: store.SessionKindACP,
				Mode: store.SessionModeLocal, Status: store.SessionStatusRunning,
				AgentType: "claude-code", AgentSessionID: "native", Cwd: workspace,
				CreatedAt: time.Now(), ExperienceID: "old", ExperienceVersion: "1.0.0", ExperienceOwnerID: &owner}
			if err := r.store.CreateSession(ctx, sess); err != nil {
				t.Fatal(err)
			}
			original := &live{owner: owner, kind: sess.Kind, mode: sess.Mode, client: client}
			r.live[sess.ID] = original
			defer func() {
				if current := r.live[sess.ID]; current != nil {
					_ = current.client.Close()
					_ = current.plugin.Close()
				}
			}()
			userID, experienceID := owner, ""
			switch scenario {
			case "new experience":
				experienceID = "new"
				manifest := `{"manifest_version":"0.3","name":"new","version":"2.0.0","server":{"type":"binary","entry_point":"claude-agent-acp","mcp_config":{"env":{"BRIGADE_TEST_EXPERIENCE_MCP":"1"}}},"_meta":{"brigade":{"experience":{"entry_tool":"app.open","instructions":"New experience instructions"}}}}`
				if err := r.store.PutPlugin(ctx, store.Plugin{OwnerID: owner, ID: experienceID, Name: experienceID, Version: "2.0.0", Target: "portable", BundlePath: bin, ManifestJSON: manifest, InstalledAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
			case "load failure":
				if err := os.WriteFile(filepath.Join(workspace, ".fail-next-load"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "rollback failure":
				if err := os.WriteFile(filepath.Join(workspace, ".fail-all-loads"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "persist failure":
				if _, err := r.store.DB().ExecContext(ctx, `CREATE TRIGGER reject_experience BEFORE UPDATE OF experience_id ON sessions BEGIN SELECT RAISE(FAIL, 'persist failed'); END`); err != nil {
					t.Fatal(err)
				}
			case "busy":
				release, err := client.ReserveIdle(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			case "foreign owner":
				userID = "other"
			case "invalid experience":
				experienceID = "missing"
			}
			result, err := r.SetExperience(ctx, sess.ID, userID, experienceID)
			if scenario == "new experience" {
				if err != nil || result.ExperienceID != "new" || result.ExperienceVersion != "2.0.0" || result.ExperienceOwnerID == nil || *result.ExperienceOwnerID != owner || r.live[sess.ID].plugin == nil {
					t.Fatalf("new experience result=%+v error=%v", result, err)
				}
			} else if scenario == "plain chat" {
				if err != nil || result.ExperienceID != "" || result.ExperienceVersion != "" || result.ExperienceOwnerID != nil {
					t.Fatalf("plain chat result=%+v error=%v", result, err)
				}
			} else {
				if err == nil {
					t.Fatal("switch should fail")
				}
				if scenario == "load failure" || scenario == "persist failure" {
					wantError := "load rejected"
					if scenario == "persist failure" {
						wantError = "persist failed"
					}
					if !strings.Contains(err.Error(), wantError) {
						t.Fatalf("wrong failure: %v", err)
					}
					if r.live[sess.ID] == original || !r.live[sess.ID].client.(*acp.Client).Alive() {
						t.Fatalf("old configuration was not restarted: %v", err)
					}
				} else if scenario == "rollback failure" {
					if !errors.Is(err, acp.ErrSessionUnrecoverable) || r.live[sess.ID] != nil {
						t.Fatalf("failed runtime retained: %v", err)
					}
				} else if r.live[sess.ID] != original {
					t.Fatalf("validation failure replaced original runtime: %v", err)
				}
				if scenario == "busy" && !errors.Is(err, acp.ErrSessionBusy) {
					t.Fatalf("busy: %v", err)
				}
				if scenario == "foreign owner" && !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("ownership: %v", err)
				}
				if scenario == "invalid experience" && !errors.Is(err, ErrExperienceUnavailable) {
					t.Fatalf("validation: %v", err)
				}
			}
			stored, err := r.Get(ctx, sess.ID, owner)
			if err != nil || stored.AgentSessionID != "native" || stored.Cwd != workspace || stored.ID != sess.ID {
				t.Fatalf("session identity changed: %+v error=%v", stored, err)
			}
			if scenario != "plain chat" && scenario != "new experience" && (stored.ExperienceID != sess.ExperienceID || stored.ExperienceVersion != sess.ExperienceVersion || stored.ExperienceOwnerID == nil || *stored.ExperienceOwnerID != owner) {
				t.Fatalf("failed operation changed pin: %+v", stored)
			}
			var current acpSession = client
			if scenario == "rollback failure" {
				if stored.Status != store.SessionStatusFailed {
					t.Fatalf("unrecoverable session is %s", stored.Status)
				}
			} else {
				current = r.live[sess.ID].client
			}
			after, err := current.Messages(ctx)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("history changed: before=%+v after=%+v error=%v", before, after, err)
			}
			content, err := os.ReadFile(file)
			if err != nil || string(content) != "workspace content" {
				t.Fatalf("workspace changed: %q error=%v", content, err)
			}
		})
	}
}

func TestExperienceResolvesExactOwner(t *testing.T) {
	r := newTestRegistry(t)
	ctx := t.Context()
	if _, _, err := r.experience(ctx, store.Session{UserID: "u1", ExperienceID: "app"}); err == nil {
		t.Fatal("unresolved legacy session was allowed to select from the current catalog")
	}
	for _, owner := range []string{"", "u1"} {
		manifest := `{"manifest_version":"0.3","name":"app","version":"1.0.0","server":{"type":"binary","entry_point":"app"},"_meta":{"brigade":{"experience":{"entry_tool":"app.open"}}}}`
		if err := r.store.PutPlugin(ctx, store.Plugin{OwnerID: owner, ID: "app", Name: "app", Version: "1.0.0", Target: "portable", BundlePath: fmt.Sprintf("/bundles/%s", owner), ManifestJSON: manifest, InstalledAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	for _, owner := range []string{"", "u1", "other"} {
		sess := store.Session{UserID: "u1", Mode: store.SessionModeLocal, ExperienceID: "app", ExperienceVersion: "1.0.0", ExperienceOwnerID: &owner}
		installed, _, err := r.experience(ctx, sess)
		if owner == "other" {
			if !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("foreign owner pin: %v", err)
			}
		} else if err != nil || installed.OwnerID != owner {
			t.Fatalf("owner=%q resolved=%q error=%v", owner, installed.OwnerID, err)
		}
	}
}

package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openPluginTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "brigade.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, id := range []string{"u1", "u2", "u3"} {
		if _, err := st.db.Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES (?, ?, '', 0)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func TestManagedPluginsPinLegacyOwnersBeforeSync(t *testing.T) {
	ctx := context.Background()
	st := openPluginTestStore(t)
	for _, item := range []Plugin{
		{ID: "app", Version: "1", Target: "portable", BundlePath: "system", InstalledAt: time.Now()},
		{ID: "app", OwnerID: "u1", Version: "1", Target: "portable", BundlePath: "u1", InstalledAt: time.Now()},
		{ID: "app", OwnerID: "u2", Version: "1", Target: "linux-amd64", BundlePath: "u2", InstalledAt: time.Now()},
	} {
		if err := st.PutPlugin(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		id, userID, experienceID string
		mode                     SessionMode
	}{
		{"personal", "u1", "app", SessionModeLocal},
		{"target fallback", "u2", "app", SessionModeLocal},
		{"docker target", "u2", "app", SessionModeDocker},
		{"system", "u3", "app", SessionModeLocal},
		{"unresolved", "u3", "new", SessionModeLocal},
	} {
		systemOwner := ""
		if err := st.CreateSession(ctx, Session{ID: tc.id, UserID: tc.userID, Mode: tc.mode,
			Kind: SessionKindACP, Status: SessionStatusRunning, CreatedAt: time.Now(),
			ExperienceID: tc.experienceID, ExperienceVersion: "1", ExperienceOwnerID: &systemOwner}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`UPDATE sessions SET experience_owner_id = NULL WHERE id = ?`, tc.id); err != nil {
			t.Fatal(err)
		}
	}
	managed := []Plugin{
		{ID: "app", Version: "1", Target: "portable", Managed: true, BundlePath: "system", InstalledAt: time.Now()},
		{ID: "new", Version: "1", Target: "portable", Managed: true, BundlePath: "new", InstalledAt: time.Now()},
	}
	if err := st.SyncManagedPlugins(ctx, managed, "darwin-arm64"); err != nil {
		t.Fatal(err)
	}
	if err := st.SyncManagedPlugins(ctx, managed, "darwin-arm64"); err != nil {
		t.Fatal(err)
	}
	for id, wantOwner := range map[string]string{"personal": "u1", "target fallback": "", "docker target": "u2", "system": ""} {
		sess, err := st.GetSession(ctx, id)
		if err != nil || sess.ExperienceOwnerID == nil || *sess.ExperienceOwnerID != wantOwner {
			t.Fatalf("backfilled %q = %+v, %v", id, sess, err)
		}
		pinned, err := st.GetPluginForOwner(ctx, *sess.ExperienceOwnerID, sess.ExperienceID, sess.ExperienceVersion, "")
		if err != nil || pinned.OwnerID != wantOwner {
			t.Fatalf("pin %q = %+v, %v", id, pinned, err)
		}
	}
	if sess, err := st.GetSession(ctx, "unresolved"); err != nil || sess.ExperienceOwnerID != nil {
		t.Fatalf("unresolved session adopted new managed app = %+v, %v", sess, err)
	}
	if _, err := st.GetPlugin(ctx, "u3", "new", "1", "portable"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy fallback adopted managed app = %v", err)
	}
	if err := st.SyncManagedPlugins(ctx, nil, "darwin-arm64"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetPlugin(ctx, "u3", "app", "", "portable"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed app available for new session = %v", err)
	}
	if pinned, err := st.GetPluginForOwner(ctx, "", "app", "1", "portable"); err != nil || pinned.BundlePath != "system" {
		t.Fatalf("removed app pin = %+v, %v", pinned, err)
	}
}

func TestManagedPluginsTargetsAndRollback(t *testing.T) {
	ctx := context.Background()
	st := openPluginTestStore(t)
	personal := Plugin{ID: "app", OwnerID: "u1", Version: "1", Target: "linux-amd64", InstalledAt: time.Now()}
	if err := st.PutPlugin(ctx, personal); err != nil {
		t.Fatal(err)
	}
	managed := Plugin{ID: "app", Version: "2", Target: "darwin-arm64", Managed: true, InstalledAt: time.Now()}
	if err := st.SyncManagedPlugins(ctx, []Plugin{managed}, "darwin-arm64"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetPlugin(ctx, "u1", "app", "", "linux-amd64"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("managed target mismatch fell back to personal = %v", err)
	}
	if listed, err := st.ListPlugins(ctx, "u1"); err != nil || len(listed) != 1 || !listed[0].Managed {
		t.Fatalf("managed list = %+v, %v", listed, err)
	}
	if err := st.SyncManagedPlugins(ctx, []Plugin{personal}, "darwin-arm64"); err == nil {
		t.Fatal("invalid managed owner accepted")
	}
	if current, err := st.GetPlugin(ctx, "u1", "app", "", "darwin-arm64"); err != nil || current.Version != "2" {
		t.Fatalf("invalid sync changed catalog = %+v, %v", current, err)
	}
	managed.Target = "linux-amd64"
	if err := st.SyncManagedPlugins(ctx, []Plugin{managed}, "darwin-arm64"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetPlugin(ctx, "u1", "app", "", "darwin-arm64"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed target still available = %v", err)
	}
	if _, err := st.GetPluginForOwner(ctx, "", "app", "2", "darwin-arm64"); err != nil {
		t.Fatalf("removed target pin unavailable = %v", err)
	}
}

func TestSessionExperienceOwnerPersistence(t *testing.T) {
	ctx := context.Background()
	st := openPluginTestStore(t)
	managed := Plugin{ID: "app", Version: "1", Target: "portable", Managed: true, InstalledAt: time.Now()}
	if err := st.SyncManagedPlugins(ctx, []Plugin{managed}, "darwin-arm64"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, Session{ID: "s", UserID: "u1", Mode: SessionModeLocal, Kind: SessionKindACP,
		Status: SessionStatusRunning, ExperienceID: "app", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	sess, err := st.GetSession(ctx, "s")
	if err != nil || sess.ExperienceOwnerID == nil || *sess.ExperienceOwnerID != "" || sess.ExperienceVersion != "1" {
		t.Fatalf("new session owner/version = %+v, %v", sess, err)
	}
	ownerID := "u1"
	if err := st.UpdateSessionExperience(ctx, "s", "personal", "2", &ownerID); err != nil {
		t.Fatal(err)
	}
	sess, err = st.GetSession(ctx, "s")
	if err != nil || sess.ExperienceID != "personal" || sess.ExperienceVersion != "2" || sess.ExperienceOwnerID == nil || *sess.ExperienceOwnerID != "u1" {
		t.Fatalf("updated pin = %+v, %v", sess, err)
	}
	foreignOwner := "u2"
	if err := st.UpdateSessionExperience(ctx, "s", "foreign", "3", &foreignOwner); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign owner accepted = %v", err)
	}
	sess, err = st.GetSession(ctx, "s")
	if err != nil || sess.ExperienceID != "personal" || sess.ExperienceVersion != "2" || sess.ExperienceOwnerID == nil || *sess.ExperienceOwnerID != "u1" {
		t.Fatalf("failed update changed pin = %+v, %v", sess, err)
	}
	if err := st.UpdateSessionExperience(ctx, "s", "", "", nil); err != nil {
		t.Fatal(err)
	}
	sess, err = st.GetSession(ctx, "s")
	if err != nil || sess.ExperienceID != "" || sess.ExperienceVersion != "" || sess.ExperienceOwnerID != nil {
		t.Fatalf("cleared experience = %+v, %v", sess, err)
	}
	if err := st.UpdateSessionExperience(ctx, "missing", "app", "1", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session = %v", err)
	}
}

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brigade.db")
	rw, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	if _, err := rw.DB().Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES ('u1', 'user', '', 0)`); err != nil {
		t.Fatal(err)
	}
	if err := rw.CreateSession(context.Background(), Session{
		ID: "s1", UserID: "u1", Mode: SessionModeDocker, Kind: SessionKindACP,
		AgentType: "codex", Status: SessionStatusRunning, GroupLabel: "Telegram · @bot", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := rw.MarkSessionUnread(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if err := rw.MarkSessionRead(context.Background(), "s1", "other"); err == nil {
		t.Fatal("another user marked session read")
	}
	if sess, err := rw.GetSession(context.Background(), "s1"); err != nil || !sess.Unread {
		t.Fatalf("unread session=%+v err=%v", sess, err)
	}
	if err := rw.MarkSessionRead(context.Background(), "s1", "u1"); err != nil {
		t.Fatal(err)
	}

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if sess, err := ro.GetSession(context.Background(), "s1"); err != nil {
		t.Fatalf("read: %v", err)
	} else if sess.GroupLabel != "Telegram · @bot" {
		t.Fatalf("group label = %q", sess.GroupLabel)
	} else if sess.Unread {
		t.Fatal("session remained unread")
	}
	if err := ro.UpdateSessionName(context.Background(), "s1", "changed"); err == nil {
		t.Fatal("read-only store accepted a write")
	}
}

func TestPluginsAreScopedAndResolvedByTarget(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "brigade.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, item := range []Plugin{
		{ID: "cad", OwnerID: "", Name: "CAD system", Version: "1", Target: "linux-amd64", InstalledAt: time.Now()},
		{ID: "cad", OwnerID: "u1", Name: "CAD user", Version: "2", Target: "darwin-arm64", InstalledAt: time.Now()},
		{ID: "cad", OwnerID: "u1", Name: "CAD user", Version: "2", Target: "linux-amd64", InstalledAt: time.Now()},
	} {
		if err := st.PutPlugin(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.GetPlugin(ctx, "u1", "cad", "", "linux-amd64")
	if err != nil || got.OwnerID != "u1" || got.Version != "2" || got.Target != "linux-amd64" {
		t.Fatalf("plugin=%+v err=%v", got, err)
	}
	got, err = st.GetPlugin(ctx, "u2", "cad", "", "linux-amd64")
	if err != nil || got.OwnerID != "" || got.Version != "1" {
		t.Fatalf("system plugin=%+v err=%v", got, err)
	}
}

func TestUpdateSessionExperienceVersion(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "brigade.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.DB().Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES ('u1', 'user', '', 0)`); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, Session{ID: "s1", UserID: "u1", Mode: SessionModeDocker, Kind: SessionKindACP, AgentType: "codex", Status: SessionStatusRunning, ExperienceID: "cad", ExperienceVersion: "1", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateSessionExperienceVersion(ctx, "s1", "2"); err != nil {
		t.Fatal(err)
	}
	if session, err := st.GetSession(ctx, "s1"); err != nil || session.ExperienceVersion != "2" {
		t.Fatalf("session=%+v err=%v", session, err)
	}
}

func TestUpdateSessionNameIfEmptyDoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "brigade.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.DB().Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES ('u1', 'user', '', 0)`); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, Session{ID: "s1", UserID: "u1", Mode: SessionModeDocker, Kind: SessionKindACP, AgentType: "codex", Status: SessionStatusRunning, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if changed, err := st.UpdateSessionNameIfEmpty(ctx, "s1", "Первое имя"); err != nil || !changed {
		t.Fatalf("first update: changed=%v err=%v", changed, err)
	}
	if changed, err := st.UpdateSessionNameIfEmpty(ctx, "s1", "Второе имя"); err != nil || changed {
		t.Fatalf("second update: changed=%v err=%v", changed, err)
	}
	sess, err := st.GetSession(ctx, "s1")
	if err != nil || sess.Name != "Первое имя" {
		t.Fatalf("session=%+v err=%v", sess, err)
	}
}

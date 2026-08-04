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
		AgentType: "codex", Status: SessionStatusRunning, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.GetSession(context.Background(), "s1"); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := ro.UpdateSessionName(context.Background(), "s1", "changed"); err == nil {
		t.Fatal("read-only store accepted a write")
	}
}

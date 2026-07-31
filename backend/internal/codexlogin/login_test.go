package codexlogin

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"
)

type memoryStore struct{ auth string }

func (s *memoryStore) SetCodexAuthJSON(_ context.Context, _, auth string) error {
	s.auth = auth
	return nil
}

type fakeRunner struct{}

func (fakeRunner) Run(_ context.Context, _ string, output io.Writer) ([]byte, error) {
	_, _ = io.Copy(output, bytes.NewBufferString("Open URL\n"))
	return []byte(`{"token":"secret"}`), nil
}

func TestAttemptIsScopedToUser(t *testing.T) {
	s := New(&memoryStore{}, LocalRunner{})
	s.mu.Lock()
	s.attempts["login"] = &attempt{login: Login{ID: "login", Status: "pending"}, userID: "alice", cancel: func() {}}
	s.mu.Unlock()
	if _, err := s.Get("bob", "login"); err == nil {
		t.Fatal("чужой пользователь прочитал login")
	}
	if err := s.Cancel("bob", "login"); err == nil {
		t.Fatal("чужой пользователь отменил login")
	}
}

func TestSuccessfulLoginPersistsCredential(t *testing.T) {
	store := &memoryStore{}
	s := New(store, fakeRunner{})
	started := s.Start("alice")
	deadline := time.Now().Add(time.Second)
	for {
		login, err := s.Get("alice", started.ID)
		if err != nil {
			t.Fatal(err)
		}
		if login.Status == "completed" {
			if login.Output != "Open URL\n" || store.auth == "" {
				t.Fatalf("login=%+v auth=%q", login, store.auth)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("login не завершился")
		}
		time.Sleep(time.Millisecond)
	}
}

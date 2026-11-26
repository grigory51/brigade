package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSessionPreviewBackfillWithoutSyntheticDate(t *testing.T) {
	st := openTestStore(t)
	ctx := t.Context()
	sess := Session{ID: "old", UserID: "owner", Kind: SessionKindACP, Mode: SessionModeLocal, Status: SessionStatusStopped, Name: "test", CreatedAt: time.Now()}
	if err := st.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateSessionPreview(ctx, sess, "Старая история", time.Time{}, MessageAuthorAgent); err != nil {
		t.Fatal(err)
	}
	var timestamp sql.NullInt64
	if err := st.DB().QueryRowContext(ctx, `SELECT last_message_at FROM sessions WHERE id = ?`, sess.ID).Scan(&timestamp); err != nil || timestamp.Valid {
		t.Fatalf("backfill timestamp must be SQL NULL: %+v, %v", timestamp, err)
	}
	if err := st.UpdateSessionPreview(ctx, sess, "Устаревший backfill", time.Time{}, MessageAuthorOwner); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSession(ctx, sess.ID)
	if err != nil || got.LastMessage != "Старая история" || !got.LastMessageAt.IsZero() || got.Name != "test" {
		t.Fatalf("backfill: %+v, %v", got, err)
	}
	at := time.Now()
	if err := st.UpdateSessionPreview(ctx, sess, "Новый turn", at, MessageAuthorAgent); err != nil {
		t.Fatal(err)
	}
	for _, stale := range []Session{sess, got} {
		if err := st.UpdateSessionPreview(ctx, stale, "Поздний backfill", time.Time{}, MessageAuthorOwner); err != nil {
			t.Fatal(err)
		}
		if err := st.UpdateSessionPreview(ctx, stale, "История с timestamp", at.Add(-time.Second), MessageAuthorOwner); err != nil {
			t.Fatal(err)
		}
	}
	got, err = st.GetSession(ctx, sess.ID)
	if err != nil || got.LastMessage != "Новый turn" || !got.LastMessageAt.Equal(at) {
		t.Fatalf("stale backfill overwrote turn: %+v, %v", got, err)
	}
}

func TestSessionPreviewSurvivesReopen(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "preview.db")
	st, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sess := Session{ID: "acp", UserID: "owner", Kind: SessionKindACP, Mode: SessionModeDocker, Status: SessionStatusRunning, Name: "test", CreatedAt: time.Now()}
	if err := st.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	before, err := st.GetSession(ctx, sess.ID)
	if err != nil || before.LastMessage != "" || !before.LastMessageAt.IsZero() {
		t.Fatalf("empty preview: %+v, %v", before, err)
	}
	at := time.Unix(1730000000, 123456789)
	if err := st.UpdateSessionPreview(ctx, sess, "Последний ответ", at, MessageAuthorAgent); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateSessionPreview(ctx, sess, "Устаревший ответ", at.Add(-time.Nanosecond), MessageAuthorOwner); err != nil {
		t.Fatal(err)
	}
	if changed, err := st.UpdateSessionNameIfEmpty(ctx, sess.ID, "Agent title"); err != nil || changed {
		t.Fatalf("manual title overwritten: changed=%v err=%v", changed, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	got, err := ro.GetSession(ctx, sess.ID)
	if err != nil || got.LastMessage != "Последний ответ" || !got.LastMessageAt.Equal(at) || got.LastMessageAuthor != MessageAuthorAgent || got.Name != "test" {
		t.Fatalf("persisted preview: %+v, %v", got, err)
	}
	for name, list := range map[string]func() ([]Session, error){
		"owner":   func() ([]Session, error) { return ro.ListSessionsByUser(ctx, sess.UserID) },
		"restore": func() ([]Session, error) { return ro.ListSessionsByStatus(ctx, SessionStatusRunning) },
	} {
		t.Run(name, func(t *testing.T) {
			items, err := list()
			if err != nil || len(items) != 1 || items[0].LastMessage != got.LastMessage || !items[0].LastMessageAt.Equal(at) {
				t.Fatalf("list preview: %+v, %v", items, err)
			}
		})
	}
	if items, err := ro.ListSessionsByUser(ctx, "other"); err != nil || len(items) != 0 {
		t.Fatalf("foreign previews: %+v, %v", items, err)
	}
}

func TestSessionPreviewBoundsAndScope(t *testing.T) {
	st := openTestStore(t)
	ctx := t.Context()
	for _, tc := range []struct {
		name    string
		kind    SessionKind
		policy  SessionExecutionPolicy
		content string
		author  MessageAuthor
		want    string
	}{
		{"unicode", SessionKindACP, SessionExecutionPersistent, strings.Repeat("🙂я", 250), MessageAuthorAgent, strings.Repeat("🙂я", 150)},
		{"spaces", SessionKindACP, SessionExecutionPersistent, " \nПривет\t\x00 мир\r\n", MessageAuthorOwner, "Привет мир"},
		{"space boundary", SessionKindACP, SessionExecutionPersistent, strings.Repeat("я", 299) + " слово", MessageAuthorAgent, strings.Repeat("я", 299)},
		{"empty", SessionKindACP, SessionExecutionPersistent, "\t\n\x00", MessageAuthorAgent, ""},
		{"contact", SessionKindACP, SessionExecutionPersistent, "private", MessageAuthorContact, ""},
		{"cli", SessionKindCLI, SessionExecutionPersistent, "private", MessageAuthorAgent, ""},
		{"shadow", SessionKindACP, SessionExecutionOnDemand, "private", MessageAuthorAgent, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := Session{ID: tc.name, UserID: "owner", Kind: tc.kind, ExecutionPolicy: tc.policy, Mode: SessionModeLocal, Status: SessionStatusStopped, CreatedAt: time.Now()}
			if err := st.CreateSession(ctx, sess); err != nil {
				t.Fatal(err)
			}
			if err := st.UpdateSessionPreview(ctx, sess, tc.content, time.Now(), tc.author); err != nil {
				t.Fatal(err)
			}
			got, err := st.GetSession(ctx, sess.ID)
			if err != nil || got.LastMessage != tc.want || !utf8.ValidString(got.LastMessage) || utf8.RuneCountInString(got.LastMessage) > 300 {
				t.Fatalf("bounded preview: %+v, %v", got, err)
			}
			if tc.want == "" && !got.LastMessageAt.IsZero() {
				t.Fatalf("empty preview has timestamp: %v", got.LastMessageAt)
			}
		})
	}
}

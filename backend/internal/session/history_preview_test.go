package session

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grigory51/brigade/backend/internal/acp"
	"github.com/grigory51/brigade/backend/internal/store"
)

type previewACPSession struct {
	fakeACPSession
	onTurnEnd      func(string, error)
	onSessionTitle func(string)
	messageReads   atomic.Int32
}

func (f *previewACPSession) SetHooks(onTurnEnd func(string, error), onSessionTitle func(string)) {
	f.onTurnEnd, f.onSessionTitle = onTurnEnd, onSessionTitle
}

func (f *previewACPSession) Messages(ctx context.Context) ([]acp.Message, error) {
	f.messageReads.Add(1)
	return f.fakeACPSession.Messages(ctx)
}

type blockedPreviewACPSession struct {
	previewACPSession
	started chan time.Time
	release chan struct{}
}

type recoveredPreviewACPSession struct {
	previewACPSession
	started   chan struct{}
	completed chan struct{}
	at        time.Time
}

func (f *recoveredPreviewACPSession) ObserveRecoveredTurn(ctx context.Context, onReady func(context.Context, time.Time)) error {
	close(f.started)
	select {
	case <-f.completed:
		onReady(ctx, f.at)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestACPPreviewRecoveredTurnWaitsForCompletion(t *testing.T) {
	for _, newer := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "newer preview"}[newer], func(t *testing.T) {
			r := newTestRegistry(t)
			ctx := t.Context()
			sess := store.Session{ID: "recovered", UserID: "u1", Kind: store.SessionKindACP, Mode: store.SessionModeDocker, Status: store.SessionStatusStopped, CreatedAt: time.Now()}
			if err := r.store.CreateSession(ctx, sess); err != nil {
				t.Fatal(err)
			}
			client := &recoveredPreviewACPSession{started: make(chan struct{}), completed: make(chan struct{}), at: time.Now()}
			client.messages = []acp.Message{{Role: "assistant", Content: "Промежуточный ответ"}}
			done := make(chan struct{})
			go func() { r.restoreACPPreview(sess, client); close(done) }()
			<-client.started
			if client.messageReads.Load() != 0 {
				t.Fatal("in-flight recovery read partial history")
			}
			if newer {
				if err := r.store.UpdateSessionPreview(ctx, sess, "Более новый ответ", client.at.Add(time.Second), store.MessageAuthorAgent); err != nil {
					t.Fatal(err)
				}
			}
			client.messages = []acp.Message{{Role: "assistant", Content: "Полный ответ"}}
			close(client.completed)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("recovered preview did not complete")
			}
			got, err := r.store.GetSession(ctx, sess.ID)
			want := "Полный ответ"
			if newer {
				want = "Более новый ответ"
			}
			if err != nil || got.LastMessage != want || got.Unread || client.messageReads.Load() != 1 {
				t.Fatalf("recovered preview: %+v err=%v reads=%d", got, err, client.messageReads.Load())
			}
		})
	}
}

func (f *blockedPreviewACPSession) Messages(ctx context.Context) ([]acp.Message, error) {
	deadline, _ := ctx.Deadline()
	f.started <- deadline
	select {
	case <-f.release:
		return f.previewACPSession.Messages(ctx)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestACPPreviewHistoryBackfill(t *testing.T) {
	for _, timestamp := range []int64{0, 1730000000} {
		t.Run(time.Unix(timestamp, 0).String(), func(t *testing.T) {
			r := newTestRegistry(t)
			ctx := t.Context()
			sess := store.Session{ID: "old", UserID: "u1", Kind: store.SessionKindACP, Mode: store.SessionModeLocal, Status: store.SessionStatusRunning, Name: "test", CreatedAt: time.Unix(1700000000, 0)}
			if err := r.store.CreateSession(ctx, sess); err != nil {
				t.Fatal(err)
			}
			client := &previewACPSession{fakeACPSession: fakeACPSession{messages: []acp.Message{
				{Role: "user", Content: "Вопрос"},
				{Role: "assistant", Content: "Старый ответ", CreatedAt: timestamp},
				{Role: "tool_call", Content: "private"},
				{Role: "reasoning", Content: "private"},
			}}}
			r.live[sess.ID] = &live{owner: sess.UserID, kind: sess.Kind, mode: sess.Mode, client: client}
			history, err := r.History(ctx, sess.ID, sess.UserID)
			if err != nil || len(history.Messages) != len(client.messages) {
				t.Fatalf("history: %+v, %v", history, err)
			}
			items, err := r.List(ctx, sess.UserID)
			if err != nil || len(items) != 1 || items[0].LastMessage != "Старый ответ" || items[0].Name != "test" {
				t.Fatalf("backfill: %+v, %v", items, err)
			}
			if timestamp == 0 {
				if !items[0].LastMessageAt.IsZero() {
					t.Fatalf("synthetic timestamp: %v", items[0].LastMessageAt)
				}
			} else if !items[0].LastMessageAt.Equal(time.Unix(timestamp, 0)) {
				t.Fatalf("message timestamp lost: %v", items[0].LastMessageAt)
			}
			if client.messageReads.Load() != 1 {
				t.Fatalf("backfill read history again: %d", client.messageReads.Load())
			}
			// Повторное чтение history не заменяет уже сохранённое preview.
			client.messages = []acp.Message{{Role: "assistant", Content: "Другой текст", CreatedAt: timestamp + 10}}
			if _, err := r.History(ctx, sess.ID, sess.UserID); err != nil {
				t.Fatal(err)
			}
			got, err := r.store.GetSession(ctx, sess.ID)
			if err != nil || got.LastMessage != "Старый ответ" {
				t.Fatalf("history overwrote preview: %+v, %v", got, err)
			}
		})
	}
}

func TestACPPreviewTurnHookIsBackgroundAndOrdered(t *testing.T) {
	r := newTestRegistry(t)
	ctx := t.Context()
	sess := store.Session{ID: "background", UserID: "u1", Kind: store.SessionKindACP, Mode: store.SessionModeLocal, Status: store.SessionStatusStopped, CreatedAt: time.Now()}
	if err := r.store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	client := &blockedPreviewACPSession{
		previewACPSession: previewACPSession{fakeACPSession: fakeACPSession{messages: []acp.Message{{Role: "assistant", Content: "Задержанный ответ"}}}},
		started:           make(chan time.Time, 1), release: make(chan struct{}),
	}
	defer close(client.release)
	r.setACPHooks(sess, client)
	returned := make(chan struct{})
	go func() {
		client.onTurnEnd("end_turn", nil)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("turn callback blocked on history")
	}
	select {
	case deadline := <-client.started:
		if deadline.IsZero() || time.Until(deadline) > 5*time.Second {
			t.Fatalf("unbounded preview request: %v", deadline)
		}
	case <-time.After(time.Second):
		t.Fatal("background preview did not start")
	}
	at := time.Now()
	if err := r.store.UpdateSessionPreview(ctx, sess, "Новый ответ", at, store.MessageAuthorAgent); err != nil {
		t.Fatal(err)
	}
	client.release <- struct{}{}
	waitUntil(t, func() bool {
		got, err := r.store.GetSession(ctx, sess.ID)
		return err == nil && got.Unread
	})
	got, err := r.store.GetSession(ctx, sess.ID)
	if err != nil || got.LastMessage != "Новый ответ" || !got.LastMessageAt.Equal(at) {
		t.Fatalf("late turn overwrote newer preview: %+v, %v", got, err)
	}
}

func TestACPPreviewReattachRefresh(t *testing.T) {
	for _, newerTurn := range []bool{false, true} {
		t.Run(map[bool]string{false: "offline turn", true: "concurrent turn"}[newerTurn], func(t *testing.T) {
			r := newTestRegistry(t)
			ctx := t.Context()
			sess := store.Session{ID: "restore", UserID: "u1", Kind: store.SessionKindACP, Mode: store.SessionModeDocker, Status: store.SessionStatusStopped, CreatedAt: time.Now()}
			if err := r.store.CreateSession(ctx, sess); err != nil {
				t.Fatal(err)
			}
			if err := r.store.UpdateSessionPreview(ctx, sess, "До рестарта", time.Unix(1730000000, 0), store.MessageAuthorAgent); err != nil {
				t.Fatal(err)
			}
			sess, err := r.store.GetSession(ctx, sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			client := &blockedPreviewACPSession{
				previewACPSession: previewACPSession{fakeACPSession: fakeACPSession{messages: []acp.Message{{Role: "assistant", Content: "Во время рестарта"}}}},
				started:           make(chan time.Time, 1), release: make(chan struct{}),
			}
			defer close(client.release)
			done := make(chan struct{})
			go func() {
				r.saveACPPreview(ctx, sess, client, time.Time{})
				close(done)
			}()
			select {
			case <-client.started:
			case <-time.After(time.Second):
				t.Fatal("refresh did not start")
			}
			at := time.Now()
			if newerTurn {
				if err := r.store.UpdateSessionPreview(ctx, sess, "Новый turn", at, store.MessageAuthorAgent); err != nil {
					t.Fatal(err)
				}
			}
			client.release <- struct{}{}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("refresh did not finish")
			}
			got, err := r.store.GetSession(ctx, sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			if newerTurn {
				if got.LastMessage != "Новый turn" || !got.LastMessageAt.Equal(at) {
					t.Fatalf("refresh overwrote new turn: %+v", got)
				}
			} else if got.LastMessage != "Во время рестарта" || !got.LastMessageAt.IsZero() {
				t.Fatalf("stale preview or synthetic timestamp after refresh: %+v", got)
			}
		})
	}
}

func TestACPPreviewTurnHookAndList(t *testing.T) {
	for _, mode := range []store.SessionMode{store.SessionModeLocal, store.SessionModeDocker} {
		t.Run(string(mode), func(t *testing.T) {
			r := newTestRegistry(t)
			ctx := t.Context()
			sess := store.Session{ID: "preview", UserID: "u1", Kind: store.SessionKindACP, Mode: mode, Status: store.SessionStatusStopped, Name: "test", CreatedAt: time.Now()}
			if err := r.store.CreateSession(ctx, sess); err != nil {
				t.Fatal(err)
			}
			client := &previewACPSession{fakeACPSession: fakeACPSession{messages: []acp.Message{
				{Role: "user", Content: "Вопрос"},
				{Role: "assistant", Content: "Готово\nПроверено", ArgsText: "private arguments", Result: "private result"},
				{Role: "reasoning", Content: "private reasoning"},
				{Role: "tool_call", Content: "private tool", ArgsText: "private arguments", Result: "private result"},
				{Role: "assistant", Content: "\n "},
				{Role: "assistant", Content: "\x00\t\x1b"},
			}}}
			r.setACPHooks(sess, client)
			if client.onTurnEnd == nil || client.onSessionTitle != nil {
				t.Fatal("missing turn hook or manual title not protected")
			}
			before := time.Now()
			client.onTurnEnd("end_turn", nil)
			after := time.Now()
			waitUntil(t, func() bool {
				got, err := r.store.GetSession(ctx, sess.ID)
				return err == nil && got.Unread
			})
			for range 3 {
				items, err := r.List(ctx, sess.UserID)
				if err != nil || len(items) != 1 {
					t.Fatalf("list: %+v, %v", items, err)
				}
				got := items[0]
				if got.LastMessage != "Готово Проверено" || got.LastMessageAuthor != store.MessageAuthorAgent || got.LastMessageAt.Before(before) || got.LastMessageAt.After(after) || got.Name != "test" {
					t.Fatalf("preview: %+v", got)
				}
			}
			// Новый реестр имеет только store, без spawner и живого клиента.
			restored := &Registry{store: r.store}
			items, err := restored.List(ctx, sess.UserID)
			if err != nil || len(items) != 1 || items[0].LastMessage != "Готово Проверено" {
				t.Fatalf("metadata-only list: %+v, %v", items, err)
			}
			if client.messageReads.Load() != 1 || len(r.live) != 0 {
				t.Fatalf("list read history or started an agent: reads=%d live=%d", client.messageReads.Load(), len(r.live))
			}
		})
	}
}

func TestACPPreviewFailedTurnAndUnavailableHistory(t *testing.T) {
	r := newTestRegistry(t)
	ctx := t.Context()
	sess := store.Session{ID: "preview", UserID: "u1", Kind: store.SessionKindACP, Mode: store.SessionModeLocal, Status: store.SessionStatusStopped, CreatedAt: time.Now()}
	if err := r.store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1730000000, 0)
	client := &previewACPSession{fakeACPSession: fakeACPSession{messages: []acp.Message{
		{Role: "assistant", Content: "Старый ответ"},
		{Role: "user", Content: "Новый вопрос", CreatedAt: at.Unix()},
		{Role: "reasoning", Content: "private"},
	}}}
	r.setACPHooks(sess, client)
	client.onTurnEnd("cancelled", context.Canceled)
	waitUntil(t, func() bool {
		got, err := r.store.GetSession(ctx, sess.ID)
		return err == nil && got.Unread
	})
	for _, readErr := range []error{nil, errors.New("history unavailable")} {
		client.messages = []acp.Message{{Role: "tool", Content: "private"}, {Role: "system", Content: "private"}}
		client.messagesErr = readErr
		r.saveACPPreview(ctx, sess, client, time.Time{})
		got, err := r.store.GetSession(ctx, sess.ID)
		if err != nil || got.LastMessage != "Новый вопрос" || !got.LastMessageAt.Equal(at) || got.LastMessageAuthor != store.MessageAuthorOwner {
			t.Fatalf("previous preview lost: %+v, %v", got, err)
		}
	}
	reads := client.messageReads.Load()
	sess.ExecutionPolicy = store.SessionExecutionOnDemand
	r.saveACPPreview(ctx, sess, client, time.Time{})
	if client.messageReads.Load() != reads {
		t.Fatal("shadow preview read agent-owned history")
	}
}

func TestACPSessionTitleHookNormalizesAgentTitle(t *testing.T) {
	r := newTestRegistry(t)
	ctx := t.Context()
	sess := store.Session{ID: "title", UserID: "u1", Kind: store.SessionKindACP, Mode: store.SessionModeLocal, Status: store.SessionStatusStopped, CreatedAt: time.Now()}
	if err := r.store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	client := &previewACPSession{}
	r.setACPHooks(sess, client)
	if client.onSessionTitle == nil {
		t.Fatal("empty session has no title hook")
	}
	client.onSessionTitle("  " + strings.Repeat("я", 70) + "\nsecond line")
	waitUntil(t, func() bool {
		got, err := r.store.GetSession(ctx, sess.ID)
		return err == nil && got.Name == strings.Repeat("я", 60)+"…"
	})
}

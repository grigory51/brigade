package session

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/grigory51/brigade/backend/internal/spawn"
	"github.com/grigory51/brigade/backend/internal/preview"
	"github.com/grigory51/brigade/backend/internal/store"
)

// fakeHandle — тестовая реализация spawn.Handle. Wait блокируется на канале exited,
// который закрывается либо тестом напрямую («процесс завершился сам»), либо Terminate
// («процесс завершил Stop/Delete»). Флаги вызовов фиксируются под мьютексом.
type fakeHandle struct {
	mu         sync.Mutex
	exited     chan struct{}
	closed     bool
	terminated bool
}

func newFakeHandle() *fakeHandle {
	return &fakeHandle{exited: make(chan struct{})}
}

func (h *fakeHandle) Read(p []byte) (int, error)  { return 0, nil }
func (h *fakeHandle) Write(p []byte) (int, error) { return len(p), nil }

func (h *fakeHandle) Close() error {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	return nil
}

// Terminate завершает «процесс»: закрывает канал exited, разблокируя Wait. Идемпотентна.
func (h *fakeHandle) Terminate(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.terminated {
		h.terminated = true
		close(h.exited)
	}
	return nil
}

func (h *fakeHandle) Resize(cols, rows uint16) error { return nil }

func (h *fakeHandle) Wait() error {
	<-h.exited
	return nil
}

func (h *fakeHandle) ExitCode() int            { return 0 }
func (h *fakeHandle) AgentSessionID() string   { return "" }
func (h *fakeHandle) ContainerLabel() string   { return "" }
func (h *fakeHandle) History() []byte          { return nil }

func (h *fakeHandle) isTerminated() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.terminated
}

// newTestRegistry поднимает реестр поверх временного SQLite-store. spawner не задан:
// тесты регистрируют живые объекты напрямую, не проходя через реальный спавн.
func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewRegistry(st, nil, store.SessionModeLocal, "/tmp", "", preview.NewService(preview.Config{}, []byte("test")))
}

// seedSession записывает running CLI-сессию в store и регистрирует её живой объект с
// переданным handle. Возвращает идентификатор сессии.
func seedSession(t *testing.T, r *Registry, userID string, handle spawn.Handle) string {
	t.Helper()
	sess := store.Session{
		ID:        "sess-" + userID,
		UserID:    userID,
		Mode:      store.SessionModeLocal,
		Kind:      store.SessionKindCLI,
		AgentType: "claude-code-cli",
		Status:    store.SessionStatusRunning,
		Cwd:       "/tmp",
		CreatedAt: time.Now(),
	}
	if err := r.store.CreateSession(context.Background(), sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	r.mu.Lock()
	r.live[sess.ID] = &live{owner: userID, kind: store.SessionKindCLI, handle: handle}
	r.mu.Unlock()
	return sess.ID
}

func (r *Registry) liveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.live)
}

// waitUntil опрашивает cond до истинности либо до таймаута. Используется вместо sync
// между тестом и фоновым watchExit, поведение которого наблюдается лишь по эффекту.
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("условие не выполнилось за отведённое время")
}

// TestWatchExitDoesNotResurrectAfterStop проверяет, что watchExit не воскрешает и не
// обновляет запись сессии, снятую Stop'ом. Stop снимает live и завершает handle
// (Terminate → Wait разблокирован), после чего watchExit просыпается, но обнаруживает,
// что живой объект уже снят не им, и статус не пишет. Сессию сразу удаляем из store —
// если бы watchExit её тронул, GetSession перестал бы возвращать ErrNotFound.
func TestWatchExitDoesNotResurrectAfterStop(t *testing.T) {
	r := newTestRegistry(t)
	handle := newFakeHandle()
	id := seedSession(t, r, "user-1", handle)

	go r.watchExit(id, handle)

	if err := r.Stop(context.Background(), id, "user-1"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Stop завершил handle: watchExit проснулся и вот-вот отработает.
	waitUntil(t, handle.isTerminated)

	// Удаляем сессию из store сразу после Stop. Дальше watchExit не должен её
	// пересоздавать/обновлять.
	if err := r.store.DeleteSession(context.Background(), id); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	// Даём фоновому watchExit гарантированно завершиться и убеждаемся, что сессия так и
	// осталась удалённой (ErrNotFound), а паник не было.
	for i := 0; i < 50; i++ {
		if _, err := r.store.GetSession(context.Background(), id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("GetSession после Stop+Delete: err = %v, want ErrNotFound", err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestWatchExitMarksStoppedOnSelfExit проверяет обратный сценарий: процесс завершается
// сам (без Stop). watchExit снимает живой объект и помечает сессию stopped.
func TestWatchExitMarksStoppedOnSelfExit(t *testing.T) {
	r := newTestRegistry(t)
	handle := newFakeHandle()
	id := seedSession(t, r, "user-1", handle)

	go r.watchExit(id, handle)

	// «Процесс вышел сам»: закрываем канал напрямую, минуя Terminate (terminated при
	// этом остаётся false — Stop/Delete не вызывались).
	handle.mu.Lock()
	close(handle.exited)
	handle.mu.Unlock()

	waitUntil(t, func() bool {
		sess, err := r.store.GetSession(context.Background(), id)
		return err == nil && sess.Status == store.SessionStatusStopped
	})
	waitUntil(t, func() bool { return r.liveCount() == 0 })
}

// TestStopForeignUser проверяет изоляцию: Stop чужой сессии возвращает ErrNotFound и
// не трогает её живой объект.
func TestStopForeignUser(t *testing.T) {
	r := newTestRegistry(t)
	handle := newFakeHandle()
	id := seedSession(t, r, "user-1", handle)

	err := r.Stop(context.Background(), id, "user-2")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Stop чужого пользователя: err = %v, want ErrNotFound", err)
	}
	if handle.isTerminated() {
		t.Fatal("handle чужой сессии завершён, want нетронут")
	}
	if r.liveCount() != 1 {
		t.Fatalf("live-объект снят при чужом Stop: count = %d, want 1", r.liveCount())
	}
}

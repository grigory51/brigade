// Package session держит реестр живых сессий агентов и восстанавливает их при
// старте бэкенда.
//
// Реестр — связующее звено между доменами: он пишет сессию в store, спавнит агента
// (CLI — через spawn.Spawner, ACP — через acp.New, который сам поднимает adapter), и
// держит живой объект (spawn.Handle для CLI либо *acp.Client для ACP) в памяти до
// остановки. Транспорты получают живой объект по sessionID через Registry: WS-терминал
// (termws.HandleProvider) — Handle, AG-UI-транспорт (SSE) — *acp.Client через ACPClient.
//
// Persist/resume: каждая сессия персистится в store со статусом и resume-полями
// (agent_session_id, container_label). При старте RestoreAll поднимает running-сессии
// заново; упавшие при восстановлении помечаются failed и не роняют старт.
//
// store хранит mode/kind строками ("local"/"docker"/"cli"/"acp"); маппинг в
// proto-перечисления выполняет транспортный слой (connect), а не реестр.
package session

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/grigory51/brigade/backend/internal/acp"
	"github.com/grigory51/brigade/backend/internal/spawn"
	"github.com/grigory51/brigade/backend/internal/store"
	"github.com/grigory51/brigade/backend/internal/transport/termws"
)

// Registry удовлетворяет провайдеру termws: тот берёт у него Handle CLI-сессии.
// Проверяется на этапе компиляции. ACP-режим (AG-UI поверх SSE) подключается через
// тонкий адаптер в main, который берёт у реестра живого *acp.Client (метод ACPClient).
var _ termws.HandleProvider = (*Registry)(nil)

// live — живая сессия в памяти. Для CLI заполнено handle, для ACP — client; второе
// поле в каждом случае nil. owner фиксирует владельца для проверки доступа из WS.
type live struct {
	owner  string
	kind   store.SessionKind
	handle spawn.Handle // CLI-режим
	client *acp.Client  // ACP-режим
}

// Registry — реестр живых сессий поверх store и spawn.Spawner.
type Registry struct {
	store   *store.Store
	spawner spawn.Spawner
	// mode — режим спавна сервиса (local|docker). Влияет на путь восстановления ACP
	// (см. restoreOne) и фиксируется в сессии при создании.
	mode       store.SessionMode
	workDir    string
	oauthToken string

	mu   sync.Mutex
	live map[string]*live
}

// NewRegistry собирает реестр. spawner используется для CLI-режима; ACP-клиент
// спавнит adapter самостоятельно. mode — режим сервиса (store.SessionModeLocal|Docker);
// workDir — корневая рабочая директория (дефолт Cwd сессии); oauthToken —
// подписочный токен Claude Code, пробрасывается агентам.
func NewRegistry(st *store.Store, spawner spawn.Spawner, mode store.SessionMode, workDir, oauthToken string) *Registry {
	return &Registry{
		store:      st,
		spawner:    spawner,
		mode:       mode,
		workDir:    workDir,
		oauthToken: oauthToken,
		live:       make(map[string]*live),
	}
}

// Create регистрирует новую сессию: пишет её в store (status=running), спавнит агента
// и сохраняет resume-поля. agentType — тип агента; cwd пустой означает дефолт workDir;
// prompt передаётся ACP-агенту первым ходом (для CLI игнорируется — ввод идёт по WS).
func (r *Registry) Create(ctx context.Context, userID string, mode store.SessionMode, kind store.SessionKind, agentType, cwd, prompt string) (store.Session, error) {
	if cwd == "" {
		cwd = r.workDir
	}
	// ACP-агент требует абсолютный cwd (относительный путь вроде "./workspace" он
	// отвергает). Нормализуем здесь, чтобы и дефолт из конфига, и пользовательский ввод
	// были абсолютными; в docker-режиме это к тому же корректный путь хоста для bind-mount.
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return store.Session{}, fmt.Errorf("session: resolve cwd %q: %w", cwd, err)
	}
	cwd = abs

	sess := store.Session{
		ID:        uuid.NewString(),
		UserID:    userID,
		Mode:      mode,
		Kind:      kind,
		AgentType: agentType,
		Status:    store.SessionStatusRunning,
		Cwd:       cwd,
		CreatedAt: time.Now(),
		Name:      autoName(prompt),
	}
	if err := r.store.CreateSession(ctx, sess); err != nil {
		log.Printf("session: create %s failed (store): %v", sess.ID, err)
		return store.Session{}, err
	}
	log.Printf("session: creating %s user=%s mode=%s kind=%s agent=%s cwd=%s",
		sess.ID, userID, mode, kind, agentType, cwd)

	// Агент должен пережить запрос Create: его жизнь равна жизни сессии, а не
	// вызову RPC. Отвязываем спавн от ctx запроса, иначе по завершении Create его
	// отмена убила бы дочерний процесс (exec.CommandContext) ещё до подключения WS.
	lv, agentSessionID, containerLabel, err := r.spawnFor(context.WithoutCancel(ctx), sess, prompt)
	if err != nil {
		// Спавн не удался — сессия в store остаётся как failed для аудита, живой
		// объект не регистрируется.
		log.Printf("session: spawn %s (%s/%s) failed: %v", sess.ID, mode, kind, err)
		_ = r.store.UpdateSessionStatus(ctx, sess.ID, store.SessionStatusFailed)
		return store.Session{}, err
	}

	if err := r.store.UpdateSessionResume(ctx, sess.ID, agentSessionID, containerLabel); err != nil {
		_ = lv.close()
		_ = r.store.UpdateSessionStatus(ctx, sess.ID, store.SessionStatusFailed)
		return store.Session{}, err
	}
	sess.AgentSessionID = agentSessionID
	sess.ContainerLabel = containerLabel

	r.mu.Lock()
	r.live[sess.ID] = lv
	r.mu.Unlock()

	log.Printf("session: created %s (%s/%s) agent_session_id=%q container_label=%q",
		sess.ID, mode, kind, agentSessionID, containerLabel)
	return sess, nil
}

// spawnFor спавнит агента под сессию и возвращает живой объект вместе с resume-полями
// (agent_session_id, container_label). prompt отправляется ACP-агенту первым ходом.
func (r *Registry) spawnFor(ctx context.Context, sess store.Session, prompt string) (*live, string, string, error) {
	switch sess.Kind {
	case store.SessionKindCLI:
		// В CLI-режиме `claude` запускается интерактивно и аутентифицируется
		// подписочным токеном (CLAUDE_CODE_OAUTH_TOKEN). Намеренно НЕ используем
		// ANTHROPIC_API_KEY: подписочный токен и API-ключ — разные модели доступа,
		// а одновременно заданные ключ и claude.ai-логин ломают auth агента.
		handle, err := r.spawner.Spawn(ctx, spawn.Spec{
			SessionID: sess.ID,
			AgentType: sess.AgentType,
			Cwd:       sess.Cwd,
			Env:       r.agentEnv(),
		})
		if err != nil {
			return nil, "", "", fmt.Errorf("session: spawn cli: %w", err)
		}
		// Следим за завершением агента: когда процесс выходит (например, пользователь
		// набрал /quit), помечаем сессию stopped и убираем её из реестра, чтобы
		// переподключение не находило мёртвый Handle.
		go r.watchExit(sess.ID, handle)
		lv := &live{owner: sess.UserID, kind: sess.Kind, handle: handle}
		return lv, handle.AgentSessionID(), handle.ContainerLabel(), nil

	case store.SessionKindACP:
		// ACP-клиент сам поднимает adapter-subprocess локально. В docker-режиме adapter
		// должен запускаться внутри контейнера сессии — это требует отдельной реализации
		// спавна.
		// TODO(docker+acp): спавнить claude-agent-acp внутри контейнера сессии и
		// связывать с ним ACP-соединение (по аналогии с DockerSpawner для CLI).
		client, err := acp.New(ctx, acp.Options{Cwd: sess.Cwd, OAuthToken: r.oauthToken})
		if err != nil {
			return nil, "", "", fmt.Errorf("session: spawn acp: %w", err)
		}
		if prompt != "" {
			// Стартовый промпт отправляем в фоне: turn доходит до конца независимо от
			// того, подключился ли уже WS-клиент (события буферизуются клиентом ACP).
			go func() { _, _ = client.Prompt(context.WithoutCancel(ctx), prompt) }()
		}
		lv := &live{owner: sess.UserID, kind: sess.Kind, client: client}
		return lv, client.SessionID(), "", nil

	default:
		return nil, "", "", fmt.Errorf("session: неизвестный kind %q", sess.Kind)
	}
}

// agentEnv формирует переменные окружения агента. Подписочный токен Claude Code
// пробрасывается как CLAUDE_CODE_OAUTH_TOKEN, если задан в конфиге.
func (r *Registry) agentEnv() []string {
	if r.oauthToken == "" {
		return nil
	}
	return []string{"CLAUDE_CODE_OAUTH_TOKEN=" + r.oauthToken}
}

// watchExit блокируется до завершения процесса агента CLI-сессии и затем
// фиксирует остановку: помечает сессию stopped в store и убирает её живой объект
// из реестра. Без этого после выхода агента (например, по /quit) сессия осталась
// бы running с мёртвым Handle, и переподключение находило бы нерабочий поток.
//
// Удаление из реестра выполняется только если зарегистрирован именно этот handle:
// иначе гонка с Stop/повторным спавном могла бы удалить чужой живой объект.
func (r *Registry) watchExit(sessionID string, handle spawn.Handle) {
	_ = handle.Wait()
	log.Printf("session: agent exited %s (exit_code=%d), marking stopped", sessionID, handle.ExitCode())

	r.mu.Lock()
	if lv, ok := r.live[sessionID]; ok && lv.handle == handle {
		delete(r.live, sessionID)
	}
	r.mu.Unlock()

	if err := r.store.UpdateSessionStatus(context.Background(), sessionID, store.SessionStatusStopped); err != nil {
		log.Printf("session: mark stopped %s failed: %v", sessionID, err)
	}
}

// Handle реализует termws.HandleProvider: отдаёт Handle CLI-сессии её владельцу.
func (r *Registry) Handle(sessionID, userID string) (spawn.Handle, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lv, ok := r.live[sessionID]
	if !ok || lv.owner != userID || lv.handle == nil {
		return nil, false
	}
	return lv.handle, true
}

// ACPClient отдаёт живого ACP-клиента сессии её владельцу. Используется AG-UI-транспортом
// (через адаптер): *acp.Client удовлетворяет его интерфейсу Bindable напрямую. ok=false,
// если сессия неизвестна, не в ACP-режиме или принадлежит другому пользователю.
func (r *Registry) ACPClient(sessionID, userID string) (*acp.Client, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lv, ok := r.live[sessionID]
	if !ok || lv.owner != userID || lv.client == nil {
		return nil, false
	}
	return lv.client, true
}

// List возвращает сессии пользователя из store (включая остановленные/упавшие).
func (r *Registry) List(ctx context.Context, userID string) ([]store.Session, error) {
	return r.store.ListSessionsByUser(ctx, userID)
}

// Get возвращает сессию пользователя по идентификатору. Чужая сессия трактуется как
// ненайденная (store.ErrNotFound), чтобы не раскрывать её существование.
func (r *Registry) Get(ctx context.Context, sessionID, userID string) (store.Session, error) {
	sess, err := r.store.GetSession(ctx, sessionID)
	if err != nil {
		return store.Session{}, err
	}
	if sess.UserID != userID {
		return store.Session{}, store.ErrNotFound
	}
	return sess, nil
}

// Rename меняет отображаемое имя сессии пользователя и возвращает обновлённую запись.
// Чужая сессия трактуется как ненайденная (см. Get).
func (r *Registry) Rename(ctx context.Context, sessionID, userID, name string) (store.Session, error) {
	sess, err := r.Get(ctx, sessionID, userID)
	if err != nil {
		return store.Session{}, err
	}
	if err := r.store.UpdateSessionName(ctx, sessionID, name); err != nil {
		return store.Session{}, err
	}
	sess.Name = name
	return sess, nil
}

// Stop останавливает живую сессию: закрывает Handle/Client и помечает её stopped.
// Идемпотентен по живому объекту: если сессия уже не в памяти, обновляет лишь статус.
func (r *Registry) Stop(ctx context.Context, sessionID, userID string) error {
	if _, err := r.Get(ctx, sessionID, userID); err != nil {
		return err
	}

	r.mu.Lock()
	lv := r.live[sessionID]
	delete(r.live, sessionID)
	r.mu.Unlock()

	if lv != nil {
		tctx, cancel := terminateCtx(ctx)
		_ = lv.terminate(tctx)
		cancel()
	}
	log.Printf("session: stopped %s by user=%s", sessionID, userID)
	return r.store.UpdateSessionStatus(ctx, sessionID, store.SessionStatusStopped)
}

// Delete останавливает сессию (если жива) и удаляет её запись из store.
func (r *Registry) Delete(ctx context.Context, sessionID, userID string) error {
	if _, err := r.Get(ctx, sessionID, userID); err != nil {
		return err
	}

	r.mu.Lock()
	lv := r.live[sessionID]
	delete(r.live, sessionID)
	r.mu.Unlock()

	if lv != nil {
		tctx, cancel := terminateCtx(ctx)
		_ = lv.terminate(tctx)
		cancel()
	}
	log.Printf("session: deleted %s by user=%s", sessionID, userID)
	return r.store.DeleteSession(ctx, sessionID)
}

// terminateCtx порождает контекст завершения сессии, отвязанный от отмены исходного
// запроса (RPC мог уже вернуться), но с собственным жёстким дедлайном, чтобы graceful
// teardown не подвис навсегда на нездоровом процессе/контейнере. Возвращает cancel,
// который вызывающий обязан вызвать (обычно через defer).
func terminateCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
}

// RestoreAll восстанавливает живые (running) сессии при старте бэкенда: CLI — через
// spawner.Reattach, ACP — повторным acp.New с resume. Сессия, которую не удалось
// восстановить, помечается failed и логируется — старт сервиса при этом не прерывается.
func (r *Registry) RestoreAll(ctx context.Context) error {
	sessions, err := r.store.ListSessionsByStatus(ctx, store.SessionStatusRunning)
	if err != nil {
		return err
	}

	for _, sess := range sessions {
		if err := r.restoreOne(ctx, sess); err != nil {
			log.Printf("session: восстановление %s (%s/%s) не удалось: %v",
				sess.ID, sess.Mode, sess.Kind, err)
			_ = r.store.UpdateSessionStatus(ctx, sess.ID, store.SessionStatusFailed)
		}
	}
	return nil
}

// restoreOne восстанавливает одну сессию и регистрирует её живой объект.
func (r *Registry) restoreOne(ctx context.Context, sess store.Session) error {
	switch sess.Kind {
	case store.SessionKindCLI:
		// Восстановить CLI-сессию можно только через `claude --resume <id>`, для чего
		// нужен agent_session_id. В local-режиме brigade его не получает (claude не
		// сообщает идентификатор структурно), поэтому для свежих CLI-сессий он пуст.
		// Рестарт бэкенда к тому же завершает дочерние процессы, так что такая сессия
		// объективно мертва — помечаем её stopped, а не пытаемся (заведомо неудачно)
		// переподключиться. Это штатный исход, не ошибка восстановления.
		if sess.AgentSessionID == "" {
			return r.store.UpdateSessionStatus(ctx, sess.ID, store.SessionStatusStopped)
		}
		handle, err := r.spawner.Reattach(ctx, spawn.Persisted{
			SessionID:      sess.ID,
			AgentSessionID: sess.AgentSessionID,
			ContainerLabel: sess.ContainerLabel,
			Cwd:            sess.Cwd,
			Env:            r.agentEnv(),
		})
		if err != nil {
			return fmt.Errorf("reattach cli: %w", err)
		}
		r.mu.Lock()
		r.live[sess.ID] = &live{owner: sess.UserID, kind: sess.Kind, handle: handle}
		r.mu.Unlock()
		// Следим за завершением восстановленного агента, как и при первичном спавне.
		go r.watchExit(sess.ID, handle)
		return nil

	case store.SessionKindACP:
		// TODO(docker+acp): в docker-режиме adapter живёт внутри контейнера сессии —
		// восстановление должно переподключаться к нему, а не поднимать новый процесс.
		client, err := acp.New(ctx, acp.Options{Cwd: sess.Cwd, OAuthToken: r.oauthToken, ResumeSessionID: sess.AgentSessionID})
		if err != nil {
			return fmt.Errorf("reattach acp: %w", err)
		}
		// agent_session_id мог измениться (adapter заводит новую ACP-сессию); фиксируем
		// актуальный, чтобы последующий resume опирался на него.
		if err := r.store.UpdateSessionResume(ctx, sess.ID, client.SessionID(), ""); err != nil {
			_ = client.Close()
			return fmt.Errorf("persist acp resume: %w", err)
		}
		r.mu.Lock()
		r.live[sess.ID] = &live{owner: sess.UserID, kind: sess.Kind, client: client}
		r.mu.Unlock()
		return nil

	default:
		return fmt.Errorf("неизвестный kind %q", sess.Kind)
	}
}

// Close останавливает все живые сессии (закрывает их Handle/Client). Статус в store не
// меняется: при следующем старте RestoreAll попытается их восстановить. Вызывается при
// graceful-остановке сервиса.
func (r *Registry) Close() {
	r.mu.Lock()
	snapshot := r.live
	r.live = make(map[string]*live)
	r.mu.Unlock()

	for _, lv := range snapshot {
		_ = lv.close()
	}
}

// autoName формирует имя сессии по умолчанию из стартового промпта: первая строка,
// обрезанная до разумной длины. Пустой промпт даёт пустое имя — клиент покажет
// производную подпись (тип агента + вид).
func autoName(prompt string) string {
	line := strings.TrimSpace(prompt)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	const maxLen = 60
	if len([]rune(line)) > maxLen {
		line = string([]rune(line)[:maxLen]) + "…"
	}
	return line
}

// close закрывает живой объект сессии (Handle для CLI, Client для ACP). Мягкое
// закрытие потока: для docker-Handle контейнер при этом продолжает работать (нужно для
// reconnect). Используется при сворачивании реестра (CloseAll), не при Stop/Delete.
func (l *live) close() error {
	if l.handle != nil {
		return l.handle.Close()
	}
	if l.client != nil {
		return l.client.Close()
	}
	return nil
}

// terminate окончательно завершает живой объект сессии и освобождает его ресурсы:
// CLI — Handle.Terminate (завершает процесс/удаляет контейнер, реапит без зомби),
// ACP — Client.Close (graceful session/close → EOF → SIGTERM → SIGKILL → reap).
// Вызывается при Stop/Delete сессии.
func (l *live) terminate(ctx context.Context) error {
	if l.handle != nil {
		return l.handle.Terminate(ctx)
	}
	if l.client != nil {
		return l.client.Close()
	}
	return nil
}

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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	"github.com/grigory51/brigade/backend/internal/acp"
	"github.com/grigory51/brigade/backend/internal/acp/acpremote"
	"github.com/grigory51/brigade/backend/internal/agent"
	"github.com/grigory51/brigade/backend/internal/agui"
	"github.com/grigory51/brigade/backend/internal/cliremote"
	"github.com/grigory51/brigade/backend/internal/codexlogin"
	"github.com/grigory51/brigade/backend/internal/mcp"
	"github.com/grigory51/brigade/backend/internal/memory"
	"github.com/grigory51/brigade/backend/internal/notify"
	"github.com/grigory51/brigade/backend/internal/preview"
	"github.com/grigory51/brigade/backend/internal/spawn"
	"github.com/grigory51/brigade/backend/internal/store"
	"github.com/grigory51/brigade/backend/internal/transport/termws"
)

// Registry удовлетворяет провайдерам termws: HandleProvider отдаёт Handle CLI-сессии,
// ShellProvider спавнит вспомогательный шелл рядом с любой сессией. Проверяется на
// этапе компиляции. ACP-режим (AG-UI поверх SSE) подключается через тонкий адаптер в
// main, который берёт у реестра живого *acp.Client (метод ACPClient).
var (
	_ termws.HandleProvider = (*Registry)(nil)
	_ termws.ShellProvider  = (*Registry)(nil)
)

// live — живая сессия в памяти. Для CLI заполнено handle, для ACP — client; второе
// поле в каждом случае nil. owner фиксирует владельца для проверки доступа из WS;
// mode нужен учёту docker-CLI сессий пользователя (общий контейнер удаляется, когда
// закрыта последняя — см. releaseUserContainerIfIdle).
type live struct {
	owner  string
	kind   store.SessionKind
	mode   store.SessionMode
	handle spawn.Handle // CLI-режим
	client acpSession   // ACP-режим (локальный *acp.Client или *acpremote.Client к демону)
	// teardown — доп. освобождение ресурсов при ЯВНОМ teardown (Stop/Delete/Archive), НЕ при
	// close() (сворачивание реестра). Для docker-ACP это удаление контейнера демона:
	// client.Close() (acpremote) лишь отцепляет поток, а контейнер должен пережить рестарт
	// brigade и уйти только по явной остановке. nil — нет доп. действий.
	teardown func(context.Context) error
}

// Run реализует codexlogin.Runner. В Docker официальный login выполняется внутри
// доверенного per-user демона с Codex runtime; credential читается backend'ом из
// bind-mounted agent home и не проходит через терминальный RPC.
func (r *Registry) Run(ctx context.Context, userID string, output io.Writer) ([]byte, error) {
	if r.mode != store.SessionModeDocker {
		return codexlogin.LocalRunner{}.Run(ctx, userID, output)
	}
	ds, ok := r.spawner.(*spawn.DockerSpawner)
	if !ok {
		return nil, errors.New("session: docker mode without DockerSpawner")
	}
	sess := store.Session{ID: "codex-login", UserID: userID, Mode: r.mode, Kind: store.SessionKindCLI, AgentType: agent.Codex.ID, Cwd: spawn.AgentHome}
	home := r.homeHost(sess)
	if home == "" {
		return nil, errors.New("session: Codex device login requires agent_home_dir")
	}
	hostCodexHome := filepath.Join(home, ".codex-login")
	if err := os.MkdirAll(hostCodexHome, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chown(hostCodexHome, spawn.AgentUID, spawn.AgentGID)
	defer os.RemoveAll(hostCodexHome)

	unlock := r.lockUser(userID)
	addr, err := ds.EnsureUserDaemon(ctx, r.agentSpec(ctx, sess), r.previews.DaemonPublicKey())
	if err != nil {
		unlock()
		return nil, err
	}
	handle := cliremote.New(addr, "codex-login-"+uuid.NewString(), r.daemonTokenFn(userID))
	if err := handle.StartEphemeral(ctx, []string{"codex", "login", "--device-auth"}, spawn.AgentHome, []string{"CODEX_HOME=" + spawn.AgentHome + "/.codex-login"}, 0, 0); err != nil {
		unlock()
		return nil, err
	}
	unlock()
	defer r.releaseUserContainerIfIdle(userID)

	copyDone := make(chan struct{})
	go func() { _, _ = io.Copy(output, handle); close(copyDone) }()
	terminated := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = handle.Terminate(context.Background())
		case <-terminated:
		}
	}()
	waitDone := make(chan error, 1)
	go func() { waitDone <- handle.Wait() }()
	select {
	case err = <-waitDone:
	case <-ctx.Done():
		_ = handle.Terminate(context.Background())
		err = ctx.Err()
	}
	close(terminated)
	<-copyDone
	if err != nil {
		return nil, err
	}
	if code := handle.ExitCode(); code != 0 {
		return nil, fmt.Errorf("codex login exited with code %d", code)
	}
	return os.ReadFile(filepath.Join(hostCodexHome, "auth.json"))
}

// acpSession — живой ACP-объект сессии: локальный *acp.Client (adapter в процессе brigade,
// local-режим) либо *acpremote.Client (adapter в durable-демоне контейнера, docker-режим).
// Реестр работает с ACP-сессией только через этот интерфейс; он же — надмножество
// transport/agui.Bindable (ACPClient отдаёт объект транспорту как Bindable).
type acpSession interface {
	Bind(sink acp.EventSink, resolver acp.PermissionResolver) (unbind func())
	Prompt(ctx context.Context, text string, onTurnStart func()) (stopReason string, err error)
	PromptAutoApprove(ctx context.Context, text string, onTurnStart func()) (stopReason string, err error)
	Cancel(ctx context.Context) error
	FinishStreams()
	Messages() []acp.Message
	// SeedMessages засеивает ленту снимком родителя при fork (session/fork историю не
	// реплеит — без засева чат ветки пуст). Разворачивается в начало Messages().
	SeedMessages(msgs []acp.Message)
	Commands() []agui.AvailableCommand
	ConfigOptions() []acpsdk.SessionConfigOption
	SetConfigOption(ctx context.Context, configID, value string) ([]acpsdk.SessionConfigOption, error)
	Status() (generating bool, seq int)
	SessionID() string
	Summarize(ctx context.Context, prompt string) (string, error)
	// WriteFile кладёт файл в рабочую директорию агента (path — относительно cwd). Единая
	// ручка фасада для заливки вложений: brigade не завязывается на среду (docker и т.п.).
	WriteFile(ctx context.Context, path string, content []byte) error
	Close() error
}

var (
	_ acpSession = (*acp.Client)(nil)
	_ acpSession = (*acpremote.Client)(nil)
)

// AgentKeyProvider выдаёт per-user SSH-ключ агента (генерируя пару при первом обращении):
// приватный ключ (OpenSSH PEM) для подкладывания в контейнер и публичный (authorized_keys).
// Реализуется auth.Service.
type AgentKeyProvider interface {
	EnsureAgentSSHKey(ctx context.Context, userID string) (privatePEM, publicKey string, err error)
}

// SessionArchive — хранилище архива сессий. Архив живёт в личной памяти пользователя
// (git-репозиторий заметок, каталог archive/), а не в БД brigade: у пользователя одно
// хранилище личных данных, и оно переживает пересоздание инстанса. Реализуется
// memory.Service.
type SessionArchive interface {
	ArchiveSession(ctx context.Context, userID string, sess memory.ArchivedSession, messages []byte) error
	ListArchivedSessions(ctx context.Context, userID string) ([]memory.ArchivedSession, error)
	ArchivedMessages(ctx context.Context, userID, sessionID string) ([]byte, error)
	DeleteArchivedSession(ctx context.Context, userID, sessionID string) error
}

// Registry — реестр живых сессий поверх store и спавнера.
//
// Режим спавна (local|docker) — свойство ИНСТАНСА (BRIGADE_MODE), не сессии:
// пользователь его не выбирает, все сессии наследуют режим сервиса. Реестр держит
// один спавнер, соответствующий этому режиму, и фиксирует mode в каждой сессии при
// создании (нужно и для restore после рестарта).
type Registry struct {
	store   *store.Store
	spawner spawn.Spawner
	mode    store.SessionMode
	workDir string
	// maxContainers — потолок на число одновременно живущих docker-контейнеров brigade
	// (ACP — контейнер на сессию, docker-CLI — общий на пользователя). 0 — без лимита.
	// Применяется только в docker-режиме, при создании сессии, добавляющей контейнер.
	maxContainers int
	// claudeHomeDir — базовый каталог per-user ~/.claude на хосте (docker-режим).
	// Пусто — фича выключена (fallback на named volume состояния по дереву сессий).
	claudeHomeDir string
	// previews — сервис публикации dev-серверов: окружение агента (env), скилл в
	// cwd, реестр зарегистрированных preview. Всегда не-nil; при выключенном preview
	// его методы деградируют до no-op.
	previews *preview.Service
	// notify шлёт пер-юзерные push-уведомления (ntfy) о завершении turn'ов. Может быть nil
	// (тесты) — тогда хук не вешается. Настройки берёт из store пер-юзер.
	notify *notify.Service
	// agentKeys выдаёт (генерируя при отсутствии) per-user SSH-ключ агента для провижининга
	// в контейнер сессии. Может быть nil (тесты) — тогда ключ не подкладывается.
	agentKeys AgentKeyProvider
	// archive — хранилище архива сессий (репозиторий личной памяти пользователя). Может
	// быть nil (тесты) — тогда архивация недоступна.
	archive SessionArchive

	mu   sync.Mutex
	live map[string]*live
	// tearingDown — сессии, для которых сейчас выполняется Stop/Delete. Guard от
	// параллельного teardown одной сессии (повторный клик «удалить», двойной запрос):
	// второй вызов получает ErrTeardownInProgress, не дублируя terminate.
	tearingDown map[string]struct{}
	// userLocks сериализует операции над общим per-user контейнером CLI-сессий
	// (docker): создание при спавне и удаление при закрытии последней сессии. Без
	// лока create/remove гонялись бы (новая сессия видит контейнер → release его
	// удаляет → exec в удалённый контейнер). Доступ к map — под mu; сами локи
	// держатся дольше (на время docker-вызовов).
	userLocks map[string]*sync.Mutex
	// sessionLocks сериализует ленивую пере-подъёмку среды сессии (EnsureACPClient):
	// без лока два параллельных turn'а на мёртвую сессию подняли бы два контейнера/адаптера.
	// Доступ к map — под mu; сам лок держится на время respawn. Ключ — sessionID.
	sessionLocks map[string]*sync.Mutex
}

// NewRegistry собирает реестр. spawner соответствует режиму инстанса (mode); mode
// фиксируется в каждой создаваемой сессии. workDir — корневая рабочая директория
// (дефолт Cwd сессии); claudeHomeDir — базовый каталог per-user ~/.claude (docker);
// previews — сервис публикации dev-серверов. Подписочный токен Claude берётся
// per-user из store при создании сессии.
func NewRegistry(st *store.Store, spawner spawn.Spawner, mode store.SessionMode, workDir, claudeHomeDir string, maxContainers int, previews *preview.Service, notifier *notify.Service, agentKeys AgentKeyProvider, archive SessionArchive) *Registry {
	return &Registry{
		store:         st,
		spawner:       spawner,
		mode:          mode,
		workDir:       workDir,
		claudeHomeDir: claudeHomeDir,
		maxContainers: maxContainers,
		previews:      previews,
		notify:        notifier,
		agentKeys:     agentKeys,
		archive:       archive,
		live:          make(map[string]*live),
		tearingDown:   make(map[string]struct{}),
		userLocks:     make(map[string]*sync.Mutex),
		sessionLocks:  make(map[string]*sync.Mutex),
	}
}

// lockUser берёт per-user лок операций над общим контейнером пользователя и
// возвращает функцию освобождения. Локи живут в map навсегда (пользователей мало,
// утечка незначима) — упрощение против reference counting.
func (r *Registry) lockUser(userID string) (unlock func()) {
	r.mu.Lock()
	l, ok := r.userLocks[userID]
	if !ok {
		l = &sync.Mutex{}
		r.userLocks[userID] = l
	}
	r.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// lockSession берёт per-session лок ленивой пере-подъёмки среды и возвращает функцию
// освобождения. Локи живут в map навсегда (как userLocks — сессий немного, утечка незначима).
func (r *Registry) lockSession(sessionID string) (unlock func()) {
	r.mu.Lock()
	l, ok := r.sessionLocks[sessionID]
	if !ok {
		l = &sync.Mutex{}
		r.sessionLocks[sessionID] = l
	}
	r.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// releaseUserContainerIfIdle удаляет общий per-user контейнер CLI-сессий, если живых
// docker-CLI сессий пользователя не осталось. Вызывается после снятия сессии с учёта
// (Stop/Delete/выход агента/провал восстановления). Под per-user локом: параллельный
// спавн новой сессии либо дождётся удаления и создаст контейнер заново, либо успеет
// первым — тогда счётчик не нулевой и удаление не выполняется. no-op вне docker-режима.
func (r *Registry) releaseUserContainerIfIdle(userID string) {
	ds, ok := r.spawner.(*spawn.DockerSpawner)
	if !ok {
		return
	}
	unlock := r.lockUser(userID)
	defer unlock()

	r.mu.Lock()
	inUse := false
	for _, lv := range r.live {
		if lv.owner == userID && lv.kind == store.SessionKindCLI && lv.mode == store.SessionModeDocker {
			inUse = true
			break
		}
	}
	r.mu.Unlock()
	if inUse {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := ds.RemoveUserContainer(ctx, userID); err != nil {
		log.Printf("session: release user container %s: %v", userID, err)
	}
}

// homeHost возвращает путь на хосте к персональному home пользователя
// (<claudeHomeDir>/<userID>), монтируемому в /home/agent контейнера. Создаёт home и
// подкаталог workspace (рабочая директория агента), chown'ит их на uid/gid agent.
// Пусто — фича выключена (claudeHomeDir не задан) либо сессия не docker.
//
// chown обязателен: bind-mount, созданный brigade (обычно root), был бы root-owned,
// и агент (uid 1001) падал бы с EACCES при записи. Ошибки логируются, не роняют спавн.
func (r *Registry) homeHost(sess store.Session) string {
	if r.claudeHomeDir == "" || sess.Mode != store.SessionModeDocker {
		return ""
	}
	home := filepath.Join(r.claudeHomeDir, sess.UserID)
	// Создаём per-session рабочий каталог ~/workspace/<id> (cwd агента): claude стартует
	// в несуществующей директории с ошибкой. Каталог на сессию изолирует Claude-проект
	// (memory/транскрипты/todos выводятся из cwd). Сам home и общий workspace создаются
	// попутно (MkdirAll — все уровни).
	//
	// В ACP-режиме home НЕ монтируется целиком: bind'ятся отдельные подпути
	// (.claude, .ssh, .brigade/<id>, workspace/<id>) — см. spawn.StartDaemon. Поэтому
	// source-каталоги этих mount'ов должны существовать на хосте заранее, иначе docker
	// создаст их root-owned и агент (uid 1001) упрётся в EACCES. .ssh готовит
	// provisionAgentSSH; .claude и .brigade/<id> создаём здесь.
	ws := filepath.Join(home, "workspace")
	sessWs := filepath.Join(ws, sess.ID)
	claudeDir := filepath.Join(home, ".claude")
	sessBrigade := filepath.Join(home, ".brigade", sess.ID)
	for _, dir := range []string{sessWs, claudeDir, sessBrigade} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			log.Printf("session: create home dir %s: %v", dir, err)
			return ""
		}
	}
	for _, dir := range []string{home, ws, sessWs, claudeDir, filepath.Dir(sessBrigade), sessBrigade} {
		if err := os.Chown(dir, spawn.AgentUID, spawn.AgentGID); err != nil {
			log.Printf("session: chown %s to %d:%d: %v", dir, spawn.AgentUID, spawn.AgentGID, err)
		}
	}
	return home
}

// userHostname возвращает hostname для контейнеров пользователя — его логин. У всех
// контейнеров пользователя hostname одинаковый: Claude привязывает креды к
// machine/hostname, и при разных hostname авторизация одной сессии не видна в другой.
// Ошибка резолва — пустой hostname (docker назначит по container id).
func (r *Registry) userHostname(ctx context.Context, userID string) string {
	u, err := r.store.GetUserByID(ctx, userID)
	if err != nil {
		log.Printf("session: get user %s for hostname: %v", userID, err)
		return ""
	}
	return sanitizeHostname(u.Username)
}

// sanitizeHostname приводит логин к валидному DNS-hostname (docker отвергает
// недопустимые): оставляет буквы/цифры/дефис, прочее заменяет дефисом, обрезает до 63.
func sanitizeHostname(name string) string {
	var b strings.Builder
	for _, ch := range name {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '-':
			b.WriteRune(ch)
		default:
			b.WriteByte('-')
		}
	}
	h := strings.Trim(b.String(), "-")
	if len(h) > 63 {
		h = h[:63]
	}
	return h
}

// userClaudeToken возвращает подписочный токен Claude пользователя из store (пусто,
// если не задан). Ошибка чтения — пустой токен (сессия поднимется, но потребует
// авторизации).
func (r *Registry) userClaudeToken(ctx context.Context, userID string) string {
	settings, err := r.store.GetUserSettings(ctx, userID)
	if err != nil {
		log.Printf("session: get user settings %s: %v", userID, err)
		return ""
	}
	return settings.ClaudeToken
}

func (r *Registry) agentToken(ctx context.Context, sess store.Session) string {
	if agent.Get(sess.AgentType).ID != agent.Claude.ID {
		return ""
	}
	return r.userClaudeToken(ctx, sess.UserID)
}

// ErrTeardownInProgress возвращается Stop/Delete, если teardown этой сессии уже
// выполняется другим запросом.
var ErrTeardownInProgress = errors.New("session: teardown already in progress")

// ErrClaudeTokenRequired возвращается Create для ACP-сессии, если у пользователя не
// задан токен Claude: ACP-агент стартует сразу (non-interactive) и без авторизации не
// поднимется. CLI-сессию создать можно — там пользователь авторизуется в терминале.
var ErrClaudeTokenRequired = errors.New("session: требуется токен Claude (задайте его в настройках) для ACP-сессии")

// ErrContainerLimitReached возвращается Create, если создание сессии превысило бы лимит
// одновременных docker-контейнеров (config.max_containers).
var ErrContainerLimitReached = errors.New("session: достигнут лимит контейнеров")

// ErrReloadWhileGenerating запрещает неявную переинициализацию занятого агента при смене
// настроек. Явный ReloadAgent сам отменяет текущий turn.
var ErrReloadWhileGenerating = errors.New("session: агент генерирует — остановите turn перед перезагрузкой")

// InstructionProfileTelegramGuest задаёт краткий фактологичный стиль для запросов из
// Telegram Guest Mode. Профиль хранится в сессии, а не добавляется к сообщениям пользователя.
const InstructionProfileTelegramGuest = "telegram-guest"

const telegramGuestInstructions = `You are answering a focused factual question invoked from Telegram Guest Mode during a live conversation between people. Respond in the language of the question. Give a direct, self-contained answer with no greeting, task narration, tool-use narration, meta-commentary, or offer to continue. Answer exactly what was asked and include only facts needed to understand the answer. Clearly state any material uncertainty.`

func instructionPrompt(profile string) string {
	if profile == InstructionProfileTelegramGuest {
		return telegramGuestInstructions
	}
	return ""
}

// atContainerLimit сообщает, превысит ли новая сессия (userID, kind) лимит docker-
// контейнеров. Учёт зеркалит releaseUserContainerIfIdle: ACP-сессия = 1 контейнер,
// docker-CLI сессии одного пользователя делят общий контейнер (1 на владельца). Новая
// сессия добавляет контейнер, если она ACP либо у пользователя ещё нет docker-CLI
// контейнера. Только docker-режим; maxContainers<=0 — без лимита.
//
// Проверка и спавн не атомарны (спавн идёт без r.mu), поэтому лимит мягкий: при гонке
// параллельных Create возможен разовый перебор на 1-2 — это потолок безопасности, не
// жёсткая квота.
func (r *Registry) atContainerLimit(userID string, kind store.SessionKind) bool {
	if r.mode != store.SessionModeDocker || r.maxContainers <= 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	acp := 0
	cliOwners := make(map[string]struct{})
	userHasCLI := false
	for _, lv := range r.live {
		if lv.mode != store.SessionModeDocker {
			continue
		}
		switch lv.kind {
		case store.SessionKindACP:
			acp++
		case store.SessionKindCLI:
			cliOwners[lv.owner] = struct{}{}
			if lv.owner == userID {
				userHasCLI = true
			}
		}
	}
	count := acp + len(cliOwners)
	adds := kind == store.SessionKindACP || !userHasCLI
	return adds && count >= r.maxContainers
}

// beginTeardown помечает сессию как останавливаемую и снимает её живой объект.
// Возвращает ErrTeardownInProgress, если teardown уже идёт. Парный endTeardown
// обязателен по завершении (defer).
func (r *Registry) beginTeardown(sessionID string) (*live, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, busy := r.tearingDown[sessionID]; busy {
		return nil, ErrTeardownInProgress
	}
	r.tearingDown[sessionID] = struct{}{}
	lv := r.live[sessionID]
	delete(r.live, sessionID)
	return lv, nil
}

func (r *Registry) endTeardown(sessionID string) {
	r.mu.Lock()
	delete(r.tearingDown, sessionID)
	r.mu.Unlock()
}

// Create регистрирует новую сессию: пишет её в store (status=running), спавнит агента
// и сохраняет resume-поля. Режим спавна берётся у инстанса (r.mode), не выбирается
// пользователем. agentType — тип агента; cwd пустой означает дефолт workDir; prompt
// передаётся ACP-агенту первым ходом (для CLI игнорируется — ввод идёт по WS).
func (r *Registry) Create(ctx context.Context, userID string, kind store.SessionKind, agentType, authProfile, cwd, prompt string, mcpServerIDs []string, image, instructionProfile string) (store.Session, error) {
	at := agent.Get(agentType)
	if at.ID == agent.Codex.ID {
		settings, err := r.store.GetUserSettings(ctx, userID)
		if err != nil {
			return store.Session{}, err
		}
		if authProfile == "" {
			authProfile = settings.CodexDefaultProfile
		}
		if authProfile == "" {
			if settings.CodexAuthJSON != "" {
				authProfile = "chatgpt"
			} else if settings.CodexAPIKey != "" {
				authProfile = "api-key"
			}
		}
		if (authProfile == "chatgpt" && settings.CodexAuthJSON == "") || (authProfile == "api-key" && settings.CodexAPIKey == "") {
			return store.Session{}, errors.New("session: выбранный профиль Codex не настроен")
		}
		if authProfile == "chatgpt" && r.mode == store.SessionModeDocker && r.claudeHomeDir == "" {
			return store.Session{}, errors.New("session: ChatGPT-профиль Codex в Docker требует настроенный agent home")
		}
	} else {
		authProfile = "claude-token"
	}
	// ACP стартует агента сразу (non-interactive) — без токена не авторизуется.
	// CLI можно создать без токена: пользователь авторизуется в терминале.
	if at.ID == agent.Claude.ID && kind == store.SessionKindACP && r.userClaudeToken(ctx, userID) == "" {
		return store.Session{}, ErrClaudeTokenRequired
	}
	// Набор MCP проверяем до записи сессии: сломанную ссылку на секрет пользователь должен
	// увидеть сразу, а не отсутствием инструментов в готовой сессии.
	if err := r.checkMcp(ctx, userID, mcpServerIDs); err != nil {
		return store.Session{}, err
	}
	// Потолок на число docker-контейнеров: проверяем до записи сессии в store, чтобы не
	// плодить failed-записи. Мягкий лимит (см. atContainerLimit).
	if r.atContainerLimit(userID, kind) {
		return store.Session{}, ErrContainerLimitReached
	}
	// Рабочая директория — на сессию, а не общая: Claude Code выводит проект
	// (~/.claude/projects/<slug> — memory, транскрипты, todos) из cwd, поэтому общий
	// cwd смешивал бы состояние несвязанных сессий. Каждая сессия получает свой
	// подкаталог <id>. docker: путь внутри контейнера (~/workspace/<id> в персональном
	// home, каталог создаёт homeHost на хосте). local: путь на хосте под work_dir.
	id := uuid.NewString()
	if r.mode == store.SessionModeDocker {
		cwd = spawn.ContainerWorkdir + "/" + id
	} else {
		base := cwd
		if base == "" {
			base = r.workDir
		}
		abs, err := filepath.Abs(filepath.Join(base, id))
		if err != nil {
			return store.Session{}, fmt.Errorf("session: resolve cwd %q: %w", base, err)
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return store.Session{}, fmt.Errorf("session: create cwd %q: %w", abs, err)
		}
		cwd = abs
	}

	sess := store.Session{
		ID:                 id,
		UserID:             userID,
		Mode:               r.mode,
		Kind:               kind,
		AgentType:          agentType,
		Status:             store.SessionStatusRunning,
		Cwd:                cwd,
		CreatedAt:          time.Now(),
		Name:               autoName(prompt),
		McpServers:         mcpServerIDs,
		Image:              image,
		AuthProfile:        authProfile,
		InstructionProfile: instructionProfile,
	}
	if err := r.store.CreateSession(ctx, sess); err != nil {
		log.Printf("session: create %s failed (store): %v", sess.ID, err)
		return store.Session{}, err
	}
	log.Printf("session: creating %s user=%s mode=%s kind=%s agent=%s cwd=%s",
		sess.ID, userID, sess.Mode, kind, agentType, cwd)

	// Скилл публикации preview кладётся до спавна: агент должен видеть его с первого
	// хода. В docker-режиме cwd — путь хоста, файл попадает в контейнер через bind-mount.
	r.installSkill(sess)
	r.provisionAgentSSH(ctx, sess)

	// Агент должен пережить запрос Create: его жизнь равна жизни сессии, а не
	// вызову RPC. Отвязываем спавн от ctx запроса, иначе по завершении Create его
	// отмена убила бы дочерний процесс (exec.CommandContext) ещё до подключения WS.
	// Для docker-CLI держим per-user лок от спавна до регистрации live-объекта:
	// releaseUserContainerIfIdle (тоже под этим локом) считает живые сессии по r.live,
	// и без удержания в окне «спавн готов, но live не записан» он снёс бы общий
	// контейнер вместе со свежим exec'ом. Для прочих режимов — no-op.
	unlock := func() {}
	if r.mode == store.SessionModeDocker && kind == store.SessionKindCLI {
		unlock = r.lockUser(userID)
	}

	// Спавн отвязан от ctx запроса: агент переживает Create (иначе отмена RPC убила бы
	// его до подключения WS), а долгоживущий hijacked-attach нельзя привязывать к
	// отменяемому контексту — cancel закрыл бы соединение сессии. Плата: зависший на
	// спавне docker-демон удерживает per-user лок этого пользователя (деградация
	// ограничена одним юзером), пока вызов не вернётся.
	lv, agentSessionID, containerLabel, err := r.spawnFor(context.WithoutCancel(ctx), sess, prompt)
	if err != nil {
		// Спавн не удался — сессия в store остаётся как failed для аудита, живой
		// объект не регистрируется. Общий контейнер мог быть создан впустую — подчищаем.
		log.Printf("session: spawn %s (%s/%s) failed: %v", sess.ID, sess.Mode, kind, err)
		_ = r.store.UpdateSessionStatus(ctx, sess.ID, store.SessionStatusFailed)
		unlock()
		r.releaseUserContainerIfIdle(userID)
		return store.Session{}, err
	}

	if err := r.store.UpdateSessionResume(ctx, sess.ID, agentSessionID, containerLabel); err != nil {
		_ = lv.terminate(context.WithoutCancel(ctx))
		_ = r.store.UpdateSessionStatus(ctx, sess.ID, store.SessionStatusFailed)
		unlock()
		r.releaseUserContainerIfIdle(userID)
		return store.Session{}, err
	}
	sess.AgentSessionID = agentSessionID
	sess.ContainerLabel = containerLabel

	r.mu.Lock()
	r.live[sess.ID] = lv
	r.mu.Unlock()
	// Live-объект зарегистрирован — теперь releaseUserContainerIfIdle увидит сессию.
	unlock()

	log.Printf("session: created %s (%s/%s) agent_session_id=%q container_label=%q",
		sess.ID, sess.Mode, kind, agentSessionID, containerLabel)
	return sess, nil
}

// spawnFor спавнит агента под сессию и возвращает живой объект вместе с resume-полями
// (agent_session_id, container_label). prompt отправляется ACP-агенту первым ходом.
func (r *Registry) spawnFor(ctx context.Context, sess store.Session, prompt string) (*live, string, string, error) {
	token := r.agentToken(ctx, sess)
	switch sess.Kind {
	case store.SessionKindCLI:
		// В CLI-режиме `claude` запускается интерактивно и аутентифицируется через
		// смонтированный ~/.claude (интерактивный claude не берёт CLAUDE_CODE_OAUTH_TOKEN
		// из env); токен в env кладём как запасной путь. Намеренно НЕ используем
		// ANTHROPIC_API_KEY: подписочный токен и API-ключ — разные модели доступа.
		// UserID включает shared-схему docker-спавна (общий контейнер на пользователя):
		// авторизация Claude привязана к контейнеру, контейнер-на-сессию сбрасывал её.
		// Сериализацию с удалением общего контейнера держит вызывающий (Create/restoreOne
		// удерживают per-user лок до регистрации live-объекта — иначе releaseUserContainer
		// в окне «спавн готов, но live ещё не записан» снёс бы контейнер под ногами).
		if sess.Mode == store.SessionModeDocker {
			// docker: CLI-агент — durable-терминал per-user демона (не docker-exec).
			return r.spawnCLIDaemon(ctx, sess, token, "")
		}
		// local: claude-процесс в pty на хосте (LocalSpawner).
		spec := r.agentSpec(ctx, sess)
		spec.Env = r.agentEnv(ctx, sess, token)
		handle, err := r.spawner.Spawn(ctx, spec)
		if err != nil {
			return nil, "", "", fmt.Errorf("session: spawn cli: %w", err)
		}
		// Следим за завершением агента: когда процесс выходит (например, пользователь
		// набрал /quit), помечаем сессию stopped и убираем её из реестра, чтобы
		// переподключение не находило мёртвый Handle.
		go r.watchExit(sess.ID, handle)
		lv := &live{owner: sess.UserID, kind: sess.Kind, mode: sess.Mode, handle: handle}
		agentSessionID := handle.AgentSessionID()
		if agent.Get(sess.AgentType).ID == agent.Codex.ID {
			agentSessionID = sess.ID
		}
		return lv, agentSessionID, handle.ContainerLabel(), nil

	case store.SessionKindACP:
		if sess.Mode == store.SessionModeDocker {
			// docker: durable-демон (`brigade acp-agent`) в контейнере сессии владеет
			// адаптером и переживает рестарт brigade; brigade — acpremote-клиент к нему.
			return r.spawnACPDaemon(ctx, sess, token, prompt, "", "")
		}
		// local: acp.New сам поднимает adapter-subprocess в процессе brigade (без демона —
		// он умрёт с brigade, restore пере-спавнит через session/load).
		client, err := acp.New(ctx, r.acpLocalOptions(ctx, sess, token))
		if err != nil {
			return nil, "", "", fmt.Errorf("session: spawn acp: %w", err)
		}
		client.OnTurnEnd = r.turnEndHook(sess)
		if prompt != "" {
			// Стартовый промпт отправляем в фоне: turn доходит до конца независимо от
			// того, подключился ли уже WS-клиент (события буферизуются клиентом ACP).
			go func() { _, _ = client.Prompt(context.WithoutCancel(ctx), prompt, nil) }()
		}
		lv := &live{owner: sess.UserID, kind: sess.Kind, mode: sess.Mode, client: client}
		return lv, client.SessionID(), "", nil

	default:
		return nil, "", "", fmt.Errorf("session: неизвестный kind %q", sess.Kind)
	}
}

// daemonTokenFn — подписант токена для вызовов демона сессии (asymmetric-auth): замыкает
// preview.DaemonToken по sessionID; acpremote/cliremote подписывают им каждый вызов.
func (r *Registry) daemonTokenFn(sessionID string) func() (string, error) {
	return func() (string, error) { return r.previews.DaemonToken(sessionID) }
}

// turnEndHook возвращает колбэк OnTurnEnd для ACP-клиента: по завершении пользовательского
// turn'а шлёт владельцу push-уведомление (ntfy) в фоне, чтобы доставка не тормозила turn.
// nil, если уведомления не сконфигурированы (r.notify == nil) — тогда клиент хук не вешает.
func (r *Registry) turnEndHook(sess store.Session) func(string, error) {
	if r.notify == nil {
		return nil
	}
	userID, label := sess.UserID, sess.Name
	return func(stopReason string, turnErr error) {
		go r.notify.TurnEnded(context.Background(), userID, label, stopReason, turnErr)
	}
}

// spawnACPDaemon поднимает durable ACP-демон в контейнере сессии (docker-режим) и
// возвращает acpremote-клиент к нему. Секреты (OAuth-токен, preview-env) уходят демону
// через Configure — в env контейнера их НЕТ (не видны из /ws/shell docker exec).
// resumeSessionID непуст → session/load существующей ACP-сессии (restore после смерти
// контейнера при живом volume).
func (r *Registry) spawnACPDaemon(ctx context.Context, sess store.Session, token, prompt, resumeSessionID, forkFromSessionID string) (*live, string, string, error) {
	ds, ok := r.spawner.(*spawn.DockerSpawner)
	if !ok {
		return nil, "", "", fmt.Errorf("session: docker-режим без DockerSpawner")
	}
	stateID, err := r.rootID(ctx, sess)
	if err != nil {
		return nil, "", "", err
	}
	addr, err := ds.ACP().StartDaemon(ctx, r.agentSpec(ctx, sess), stateID, r.previews.DaemonPublicKey())
	if err != nil {
		return nil, "", "", fmt.Errorf("session: start acp daemon: %w", err)
	}

	rc := acpremote.New(addr, "", r.daemonTokenFn(sess.ID))
	rc.OnTurnEnd = r.turnEndHook(sess)
	r.loadAgentSSHKey(ctx, sess.UserID, rc.SetSSHKey)
	sid, err := rc.Configure(ctx, acpremote.ConfigureOptions{
		OAuthToken:        token,
		ExtraEnv:          r.agentEnv(ctx, sess, token), // auth и preview — только процессу адаптера
		AdapterCommand:    agent.Get(sess.AgentType).CommandFor(store.SessionKindACP),
		Cwd:               sess.Cwd,
		ResumeSessionID:   resumeSessionID,
		ForkFromSessionID: forkFromSessionID,
		McpServers:        r.mcpServers(ctx, sess),
		PluginDirs:        r.acpPluginDirs(sess),
		SystemPrompt:      instructionPrompt(sess.InstructionProfile),
	})
	if err != nil {
		return nil, "", "", fmt.Errorf("session: configure acp daemon: %w", err)
	}

	if prompt != "" {
		go func() { _, _ = rc.Prompt(context.WithoutCancel(ctx), prompt, nil) }()
	}
	lv := &live{owner: sess.UserID, kind: sess.Kind, mode: sess.Mode, client: rc, teardown: r.acpDaemonTeardown(sess.ID)}
	return lv, sid, "", nil
}

// spawnCLIDaemon поднимает CLI-агента (claude) durable-терминалом per-user демона (общий
// контейнер пользователя — логин claude привязан к контейнеру и переживает несколько сессий).
// brigade ходит к демону по Terminal RPC, а не docker-exec'ом. resumeSessionID непусто →
// `claude --resume` (restore/respawn), иначе `--session-id` (первый запуск). Возвращает
// cliremote-Handle; удаление контейнера — releaseUserContainerIfIdle (доп. teardown не нужен).
func (r *Registry) spawnCLIDaemon(ctx context.Context, sess store.Session, token, resumeSessionID string) (*live, string, string, error) {
	ds, ok := r.spawner.(*spawn.DockerSpawner)
	if !ok {
		return nil, "", "", fmt.Errorf("session: docker-режим без DockerSpawner")
	}
	addr, err := ds.EnsureUserDaemon(ctx, r.agentSpec(ctx, sess), r.previews.DaemonPublicKey())
	if err != nil {
		return nil, "", "", fmt.Errorf("session: ensure user daemon: %w", err)
	}
	// Команда агента — из манифеста: терминальный режим у каждого агента свой.
	bin := agent.Get(sess.AgentType).CommandFor(store.SessionKindCLI)
	cmd := []string{bin, "--session-id", sess.ID}
	if resumeSessionID != "" {
		cmd = []string{bin, "--resume", resumeSessionID}
	}
	if agent.Get(sess.AgentType).ID == agent.Codex.ID {
		cmd = []string{bin}
		if resumeSessionID != "" {
			cmd = []string{bin, "resume", "--last"}
		}
	}
	// aud подписи = userID (per-user демон обслуживает все CLI-сессии пользователя); id
	// терминала = sess.ID (сессия). OAuth-токен и preview-env — в env процесса, не контейнера.
	hc := cliremote.New(addr, sess.ID, r.daemonTokenFn(sess.UserID))
	r.loadAgentSSHKey(ctx, sess.UserID, hc.SetSSHKey)
	if err := hc.Start(cmd, sess.Cwd, r.agentEnv(ctx, sess, token), 0, 0); err != nil {
		return nil, "", "", fmt.Errorf("session: start cli terminal: %w", err)
	}
	go r.watchExit(sess.ID, hc)
	lv := &live{owner: sess.UserID, kind: sess.Kind, mode: sess.Mode, handle: hc}
	return lv, hc.AgentSessionID(), hc.ContainerLabel(), nil
}

// agentSpec собирает спецификацию запуска сессии: образ (пользовательский либо дефолтный),
// компоненты runtime и команду агента — всё из манифеста агента (internal/agent), чтобы
// добавление второго агента не требовало правок здесь и в спавнере.
func (r *Registry) agentSpec(ctx context.Context, sess store.Session) spawn.Spec {
	at := agent.Get(sess.AgentType)
	layers := at.LayersFor(sess.Kind)
	// CLI-демон общий для всех агентов пользователя, поэтому при первом создании получает
	// объединение CLI-runtime. Иначе контейнер, впервые созданный для Claude, не смог бы
	// позже запустить Codex (и наоборот).
	if sess.Kind == store.SessionKindCLI {
		seen := map[string]bool{}
		layers = nil
		for _, candidate := range agent.List() {
			for _, layer := range candidate.LayersFor(store.SessionKindCLI) {
				if !seen[layer.Name] {
					seen[layer.Name] = true
					layers = append(layers, layer)
				}
			}
		}
	}
	return spawn.Spec{
		SessionID: sess.ID,
		UserID:    sess.UserID,
		Cwd:       sess.Cwd,
		Image:     sess.Image,
		Layers:    layers,
		Command:   at.CommandFor(sess.Kind),
		HomeHost:  r.homeHost(sess),
		Hostname:  r.userHostname(ctx, sess.UserID),
	}
}

// acpPluginDirs — плагин-директории агента (per-session плагин brigade со скиллами), если
// preview включён. Путь — внутри контейнера (cwd агента).
func (r *Registry) acpPluginDirs(sess store.Session) []string {
	if !r.previews.Config().Enabled || agent.Get(sess.AgentType).ID != agent.Claude.ID {
		return nil
	}
	return []string{sess.Cwd + "/" + preview.PluginDirRel}
}

// acpLocalOptions — общие опции local-адаптера ACP (acp.New): cwd, токен, preview-env, набор
// MCP-серверов и каталог плагина со скиллами. ЕДИНАЯ точка, чтобы ВСЕ пути (создание, restore,
// respawn, reload, fork) поднимали адаптер с одинаковым набором — иначе часть путей теряет
// --plugin-dir/MCP и скиллы (/note) либо render_ui пропадают. Resume/Fork вызывающий
// доставляет сам поверх.
func (r *Registry) acpLocalOptions(ctx context.Context, sess store.Session, token string) acp.Options {
	return acp.Options{
		Cwd:            sess.Cwd,
		OAuthToken:     token,
		AdapterCommand: agent.Get(sess.AgentType).CommandFor(store.SessionKindACP),
		ExtraEnv:       r.agentEnv(ctx, sess, token),
		McpServers:     r.mcpServers(ctx, sess),
		PluginDirs:     r.acpPluginDirs(sess),
		SystemPrompt:   instructionPrompt(sess.InstructionProfile),
	}
}

// mcpServers — набор MCP-серверов сессии: служебный сервер brigade (render_ui/show_choice)
// плюс включённые пользователем (sess.McpServers). Вызывается на всех путях спавна и
// переконфигурации агента.
//
// Сервер, чью ссылку на секрет развернуть не удалось, ИСКЛЮЧАЕТСЯ с записью в лог: пути
// восстановления и resume не должны падать из-за удалённого секрета. Пользователь узнаёт о
// проблеме раньше — checkMcp отвергает такой набор при создании сессии и при смене набора,
// когда человек у экрана и может починить конфиг.
func (r *Registry) mcpServers(ctx context.Context, sess store.Session) []acpsdk.McpServer {
	// Путь к служебному серверу — из манифеста агента: агент, который его не объявляет,
	// кастомных UI-инструментов не получает. В local-режиме сервер берётся с хоста (бандл
	// приложения): контейнерных слоёв там нет, а путь манифеста ведёт внутрь контейнера.
	var servers []acpsdk.McpServer
	script := agent.Get(sess.AgentType).McpServerScript
	if sess.Mode == store.SessionModeLocal {
		script = acp.LocalMCPServerPath()
	}
	if script != "" {
		servers = append(servers, acp.BrigadeMCPServer(script))
	}
	if len(sess.McpServers) == 0 {
		return servers
	}
	selected, secrets, err := r.userMcpConfigs(ctx, sess.UserID, sess.McpServers)
	if err != nil {
		log.Printf("session %s: mcp: %v", sess.ID, err)
		return servers
	}
	for _, srv := range selected {
		built, err := mcp.Build([]store.McpServer{srv}, secrets)
		if err != nil {
			log.Printf("session %s: mcp-сервер пропущен: %v", sess.ID, err)
			continue
		}
		servers = append(servers, built...)
	}
	return servers
}

// checkMcp проверяет, что набор серверов существует и все ссылки на секреты разрешимы.
// Используется там, где ошибку видит пользователь: создание сессии и смена набора.
func (r *Registry) checkMcp(ctx context.Context, userID string, serverIDs []string) error {
	if len(serverIDs) == 0 {
		return nil
	}
	selected, secrets, err := r.userMcpConfigs(ctx, userID, serverIDs)
	if err != nil {
		return err
	}
	if len(selected) != len(serverIDs) {
		return fmt.Errorf("session: часть MCP-серверов не найдена")
	}
	_, err = mcp.Build(selected, secrets)
	return err
}

// userMcpConfigs отбирает конфиги пользователя по идентификаторам (в порядке serverIDs) и
// отдаёт их вместе с расшифрованными секретами vault.
func (r *Registry) userMcpConfigs(ctx context.Context, userID string, serverIDs []string) ([]store.McpServer, map[string]string, error) {
	all, err := r.store.ListMcpServers(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[string]store.McpServer, len(all))
	for _, srv := range all {
		byID[srv.ID] = srv
	}
	selected := make([]store.McpServer, 0, len(serverIDs))
	for _, id := range serverIDs {
		if srv, ok := byID[id]; ok {
			selected = append(selected, srv)
		}
	}
	secrets, err := r.store.SecretValues(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	return selected, secrets, nil
}

// acpDaemonTeardown — замыкание удаления контейнера ACP-демона (для live.teardown, docker).
// nil, если спавнер не docker.
func (r *Registry) acpDaemonTeardown(sessionID string) func(context.Context) error {
	ds, ok := r.spawner.(*spawn.DockerSpawner)
	if !ok {
		return nil
	}
	return func(ctx context.Context) error { return ds.ACP().RemoveContainer(ctx, sessionID) }
}

// agentEnv формирует переменные окружения агента: персональный подписочный токен
// Claude Code пользователя (CLAUDE_CODE_OAUTH_TOKEN, если задан) и, при включённом
// preview, переменные публикации dev-серверов (см. previewEnv). token — per-user из
// store; для CLI-режима агент дополнительно опирается на смонтированный ~/.claude.
func (r *Registry) agentEnv(ctx context.Context, sess store.Session, token string) []string {
	env := []string{"BRIGADE_SESSION_ID=" + sess.ID}
	if token != "" {
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+token)
	}
	if agent.Get(sess.AgentType).ID == agent.Codex.ID {
		if instructions := instructionPrompt(sess.InstructionProfile); instructions != "" {
			config, _ := json.Marshal(map[string]string{"developer_instructions": instructions})
			env = append(env, "CODEX_CONFIG="+string(config))
		}
		settings, err := r.store.GetUserSettings(ctx, sess.UserID)
		if err != nil {
			log.Printf("session %s: codex auth: %v", sess.ID, err)
		} else {
			hostHome := filepath.Join(r.hostCwd(sess), ".brigade", "codex-home")
			codexHome := hostHome
			if sess.Mode == store.SessionModeDocker {
				codexHome = filepath.Join(sess.Cwd, ".brigade", "codex-home")
			}
			env = append(env, "CODEX_HOME="+codexHome)
			if sess.AuthProfile == "api-key" {
				env = append(env, "CODEX_API_KEY="+settings.CodexAPIKey, "OPENAI_API_KEY="+settings.CodexAPIKey)
			} else if err := os.MkdirAll(hostHome, 0o700); err == nil {
				authPath := filepath.Join(hostHome, "auth.json")
				if current, err := os.ReadFile(authPath); err == nil && json.Valid(current) {
					// Codex ротирует refresh-токены сам. После аварийного рестарта файл может
					// быть новее БД — импортируем его до запуска и не затираем старой копией.
					if err := r.store.SetCodexAuthJSON(ctx, sess.UserID, string(current)); err != nil {
						log.Printf("session %s: persist recovered codex auth: %v", sess.ID, err)
					}
				} else if err := os.WriteFile(authPath, []byte(settings.CodexAuthJSON), 0o600); err != nil {
					log.Printf("session %s: write codex auth: %v", sess.ID, err)
				}
			}
		}
	}
	env = append(env, r.previewEnv(sess)...)
	env = append(env, r.installMcpProject(ctx, sess)...)
	r.chownCodexHome(sess)
	return env
}

// chownCodexHome передаёт агенту созданные backend'ом CODEX_HOME и файлы внутри.
// В docker-инсталляции backend обычно root, а Codex работает как uid 1001.
func (r *Registry) chownCodexHome(sess store.Session) {
	if sess.Mode != store.SessionModeDocker || agent.Get(sess.AgentType).ID != agent.Codex.ID {
		return
	}
	cwd := r.hostCwd(sess)
	if cwd == "" {
		return
	}
	base := filepath.Join(cwd, ".brigade")
	for _, path := range []string{base, filepath.Join(base, "codex-home"), filepath.Join(base, "codex-home", "auth.json"), filepath.Join(base, "codex-home", "config.toml")} {
		if err := os.Lchown(path, spawn.AgentUID, spawn.AgentGID); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("session %s: chown codex home %s: %v", sess.ID, path, err)
		}
	}
}

// installMcpProject раскладывает .mcp.json рядом с CLI-агентом и возвращает переменные
// окружения со значениями секретов. CLI-агент (`claude` в терминале) читает набор серверов
// из рабочей директории — по протоколу, как ACP-адаптеру, ему их не передать.
//
// Ошибка не прерывает запуск сессии: без MCP агент работоспособен, а сообщение в логе
// говорит, что именно не сложилось.
func (r *Registry) installMcpProject(ctx context.Context, sess store.Session) []string {
	cwd := r.hostCwd(sess)
	if cwd == "" {
		return nil
	}
	selected, secrets, err := r.userMcpConfigs(ctx, sess.UserID, sess.McpServers)
	if err != nil {
		log.Printf("session %s: mcp: %v", sess.ID, err)
		return nil
	}
	var env []string
	if agent.Get(sess.AgentType).ID == agent.Codex.ID {
		env, err = mcp.WriteCodexConfig(filepath.Join(cwd, ".brigade", "codex-home"), selected, secrets)
	} else {
		env, err = mcp.WriteProjectConfig(cwd, selected, secrets)
	}
	if err != nil {
		log.Printf("session %s: mcp: %v", sess.ID, err)
		return nil
	}
	return env
}

// hostCwd — рабочая директория сессии на ХОСТЕ (куда brigade кладёт файлы для агента).
// В docker-режиме cwd сессии — путь внутри контейнера, а тот же каталог на хосте лежит в
// персональном home пользователя. Пусто — класть некуда (эфемерный контейнер без home).
func (r *Registry) hostCwd(sess store.Session) string {
	if sess.Mode != store.SessionModeDocker {
		return sess.Cwd
	}
	home := r.homeHost(sess)
	if home == "" {
		return ""
	}
	return filepath.Join(home, "workspace", sess.ID)
}

// previewEnv формирует preview-переменные агента: идентификатор сессии, токен
// регистрации, адрес API brigade и шаблон публичного URL. Пусто при выключенном
// preview. Адрес API — plain-listener brigade: локальному процессу он доступен как
// 127.0.0.1; контейнеру — по hostname brigade в общей docker-сети (если brigade сам
// в контейнере) либо через host.docker.internal (если brigade — процесс на хосте).
func (r *Registry) previewEnv(sess store.Session) []string {
	cfg := r.previews.Config()
	if !cfg.Enabled {
		return nil
	}
	apiHost := "127.0.0.1"
	if sess.Mode == store.SessionModeDocker {
		apiHost = r.dockerAPIHost()
	}
	return []string{
		"BRIGADE_PREVIEW_TOKEN=" + r.previews.TokenFor(sess.ID),
		fmt.Sprintf("BRIGADE_API_URL=http://%s:%d", apiHost, cfg.APIPort),
		"BRIGADE_PREVIEW_URL_TEMPLATE=" + cfg.URLTemplate(sess.ID),
	}
}

// dockerAPIHost возвращает hostname, по которому агент-контейнер обращается к API
// brigade. Определяется docker-спавнером: имя контейнера brigade в общей сети либо
// host.docker.internal. Фолбэк host.docker.internal, если спавнер не docker.
func (r *Registry) dockerAPIHost() string {
	if ds, ok := r.spawner.(*spawn.DockerSpawner); ok {
		return ds.APIHost()
	}
	return "host.docker.internal"
}

// installSkill кладёт скилл brigade-preview в рабочую директорию сессии на ХОСТЕ
// (при включённом preview). В docker-режиме sess.Cwd — путь внутри контейнера, а файл
// нужно положить в хостовый workspace персонального home (<home>/workspace); в local
// это и есть sess.Cwd. Ошибка не роняет создание сессии — скилл вспомогателен.
func (r *Registry) installSkill(sess store.Session) {
	dir := r.hostCwd(sess)
	if dir == "" {
		return // фича home выключена — скилл класть некуда (эфемерный контейнер)
	}
	// В Codex MCP-tools deferred и не входят в постоянный контекст. Этот короткий skill
	// сообщает о generative UI и направляет модель к встроенному render_ui. Он нужен во
	// всех ACP-сессиях независимо от preview.
	if sess.Kind == store.SessionKindACP && agent.Get(sess.AgentType).ID == agent.Codex.ID {
		if err := preview.InstallCodexUISkill(dir); err != nil {
			log.Printf("session: install UI skill %s: %v", sess.ID, err)
		}
		if err := preview.InstallCodexFilesSkill(dir); err != nil {
			log.Printf("session: install files skill %s: %v", sess.ID, err)
		}
	}
	if !r.previews.Config().Enabled {
		return
	}
	if sess.Mode == store.SessionModeDocker {
		// Убираем стейл-копию прежней схемы: раньше скилл ставился в ОБЩИЙ workspace
		// (<home>/workspace/.claude/skills), и при cwd=<id> Claude Code находил его вверх
		// по дереву вторым — отсюда дубль в slash-меню.
		_ = os.RemoveAll(filepath.Join(r.homeHost(sess), "workspace", ".claude", "skills", "brigade-preview"))
	}
	// marketplaceID уникален на сессию: Claude Code кеширует локальный marketplace глобально
	// по ID и пинит его на каталог первой сессии — при константном ID новые сессии грузили бы
	// старые скиллы. "brigade-<sessionID>" даёт каждой сессии свежую регистрацию из её каталога.
	var err error
	if agent.Get(sess.AgentType).ID == agent.Codex.ID {
		err = preview.InstallCodexSkills(dir)
	} else {
		err = preview.InstallSkill(dir, "brigade-"+sess.ID)
	}
	if err != nil {
		log.Printf("session: install preview skill %s: %v", sess.ID, err)
	}
}

// loadAgentSSHKey отдаёт демону приватный ключ агента: тот держит его в ssh-agent'е в
// памяти и подставляет SSH_AUTH_SOCK адаптеру и терминалам. Ключ идёт по RPC и НЕ пишется
// в среду агента — иначе его мог бы забрать любой процесс, запущенный агентом (а в
// docker-режиме home ещё и общий для сессий пользователя). Шлём на каждый спавн среды —
// так подхватывается перевыпуск ключа. Ошибка не роняет сессию: без ключа не работает
// только push по git@, агент остаётся рабочим.
func (r *Registry) loadAgentSSHKey(ctx context.Context, userID string, load func(context.Context, string) error) {
	if r.agentKeys == nil {
		return
	}
	priv, _, err := r.agentKeys.EnsureAgentSSHKey(ctx, userID)
	if err != nil {
		log.Printf("session: ensure agent ssh key %s: %v", userID, err)
		return
	}
	if err := load(ctx, priv); err != nil {
		log.Printf("session: load agent ssh key %s: %v", userID, err)
	}
}

// provisionAgentSSH готовит ~/.ssh агента в его home на ХОСТЕ (docker-режим): только
// config, без ключей — приватный ключ живёт в ssh-agent демона (см. loadAgentSSHKey), а
// публичный агенту не нужен. config задаёт accept-new, чтобы первый push по git@github.com
// не повис на интерактивной проверке host key; IdentityFile намеренно НЕ указываем —
// ключ приходит из агента, а IdentitiesOnly перекрыл бы его. В local-режиме (home пуст)
// no-op: агент пользуется хостовым ~/.ssh. Ошибки логируются, не роняют создание сессии.
func (r *Registry) provisionAgentSSH(_ context.Context, sess store.Session) {
	home := r.homeHost(sess)
	if home == "" {
		return
	}
	// Ключи прежних версий: в CLI-контейнер home монтируется целиком и переживает
	// обновление brigade, так что без явной уборки приватный ключ остался бы лежать в среде
	// агента. Ничего другого brigade в ~/.ssh не кладёт: ключ живёт в ssh-agent демона, а
	// config с путём к его сокету демон пишет сам внутри контейнера (см.
	// acpdaemon.writeSSHConfig).
	sshDir := filepath.Join(home, ".ssh")
	for _, stale := range []string{"id_ed25519", "id_ed25519.pub"} {
		if err := os.Remove(filepath.Join(sshDir, stale)); err != nil && !os.IsNotExist(err) {
			log.Printf("session: remove legacy key %s: %v", stale, err)
		}
	}
}

// rootID возвращает идентификатор корневой сессии дерева (подъём по parent_id).
// Ветки монтируют volume состояния корня: форкнутый агент читает исходную сессию из
// общего хранилища. Родитель, удалённый из store, обрывает подъём — корнем считается
// последняя достижимая сессия.
func (r *Registry) rootID(ctx context.Context, sess store.Session) (string, error) {
	cur := sess
	for cur.ParentID != "" {
		parent, err := r.store.GetSession(ctx, cur.ParentID)
		if err != nil {
			break
		}
		cur = parent
	}
	return cur.ID, nil
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

	r.mu.Lock()
	owned := false
	var owner string
	if lv, ok := r.live[sessionID]; ok && lv.handle == handle {
		delete(r.live, sessionID)
		owned = true
		owner = lv.owner
	}
	r.mu.Unlock()

	// Статус пишем только если живой объект сняли именно мы. Иначе жизненным циклом
	// уже распорядился Stop/Delete (записал stopped или удалил запись из store) —
	// повторная запись создала бы гонку двух писателей (перезапись статуса, запись по
	// удалённой сессии).
	if !owned {
		return
	}
	log.Printf("session: agent exited %s (exit_code=%d), marking stopped", sessionID, handle.ExitCode())
	if err := r.store.UpdateSessionStatus(context.Background(), sessionID, store.SessionStatusStopped); err != nil {
		log.Printf("session: mark stopped %s failed: %v", sessionID, err)
	}
	// Агент вышел сам (например, /quit в последней сессии) — общий per-user контейнер
	// мог остаться без сессий.
	r.releaseUserContainerIfIdle(owner)
}

// Handle реализует termws.HandleProvider: отдаёт Handle CLI-сессии её владельцу, при
// необходимости пере-подняв мёртвую среду (симметрично EnsureACPClient для ACP). docker:
// per-user демон/терминал мог умереть вне рестарта brigade (docker rm) — поток stale-хэндла
// оборван (pump помечает streamDead, но waitCh не закрывает, поэтому watchExit сессию не
// снимает), и терминал сразу отвалился бы. Мёртвый хэндл → пере-поднимаем демон и терминал с
// resume. local: процесс durable в рамках работы brigade; его смерть снимает watchExit,
// мёртвый local-хэндл сюда не долетает (Alive у него нет — считаем живым).
func (r *Registry) Handle(ctx context.Context, sessionID, userID string) (spawn.Handle, bool) {
	r.mu.Lock()
	lv, ok := r.live[sessionID]
	r.mu.Unlock()
	if !ok || lv.owner != userID || lv.handle == nil {
		return nil, false
	}
	if lv.mode != store.SessionModeDocker || handleAlive(lv.handle) {
		return lv.handle, true
	}

	// Мёртв — сериализуем респавн per-user (демон общий на все CLI-сессии пользователя).
	unlock := r.lockUser(userID)
	defer unlock()
	r.mu.Lock()
	lv = r.live[sessionID]
	r.mu.Unlock()
	if lv == nil || lv.handle == nil {
		return nil, false
	}
	if handleAlive(lv.handle) {
		return lv.handle, true
	}

	sess, err := r.Get(ctx, sessionID, userID)
	if err != nil {
		log.Printf("session: ensure cli %s: get session: %v", sessionID, err)
		return nil, false
	}
	newLv, _, _, err := r.spawnCLIDaemon(ctx, sess, r.agentToken(ctx, sess), sess.AgentSessionID)
	if err != nil {
		log.Printf("session: ensure cli %s: respawn: %v", sessionID, err)
		return nil, false
	}
	r.mu.Lock()
	old := r.live[sessionID]
	r.live[sessionID] = newLv
	r.mu.Unlock()
	// Старый хэндл мёртв окончательно — Abandon отцепляет И разблокирует его watchExit
	// (иначе горутина повисла бы на Wait: обрыв стрима waitCh не закрывает).
	if old != nil && old.handle != nil {
		if ab, ok := old.handle.(interface{ Abandon() }); ok {
			ab.Abandon()
		} else {
			_ = old.handle.Close()
		}
	}
	return newLv.handle, true
}

// handleAlive — жив ли поток терминала (cliremote реализует Alive). Хэндл без Alive считаем
// живым: respawn был бы не по адресу.
func handleAlive(h spawn.Handle) bool {
	if a, ok := h.(interface{ Alive() bool }); ok {
		return a.Alive()
	}
	return true
}

// Shell реализует termws.ShellProvider: спавнит вспомогательный шелл рядом с сессией
// для ручного осмотра её рабочей директории. Режим берётся у сессии (см.
// applyACPSpawnMode): local — интерактивный шелл хоста в pty с cwd сессии; docker —
// exec в работающий контейнер сессии. Жизненный цикл шелла — на вызывающей стороне
// (termws завершает его при разрыве WS); реестр шеллы не отслеживает.
func (r *Registry) Shell(ctx context.Context, sessionID, userID string) (termws.Shell, error) {
	sess, err := r.Get(ctx, sessionID, userID)
	if err != nil {
		return nil, err
	}

	switch sess.Mode {
	case store.SessionModeDocker:
		// ACP-docker: шелл через фасад-демон (bash в pty внутри контейнера), а не docker-exec.
		// Работает независимо от способа спавна; kill-by-mark-костыль не нужен (демон владеет
		// pty и гасит его сам при закрытии стрима).
		if sess.Kind == store.SessionKindACP {
			rc, ok := r.ACPClient(sessionID, userID)
			if !ok {
				return nil, fmt.Errorf("session: нет живого демона для шелла сессии %s", sessionID)
			}
			remote, ok := rc.(*acpremote.Client)
			if !ok {
				return nil, fmt.Errorf("session: шелл сессии %s без acpremote-клиента", sessionID)
			}
			return remote.OpenShell(sess.Cwd)
		}
		// CLI-docker: вспом. шелл — эфемерный терминал per-user демона (не docker-exec).
		ds, ok := r.spawner.(*spawn.DockerSpawner)
		if !ok {
			return nil, fmt.Errorf("session: docker shell without DockerSpawner")
		}
		addr, alive := ds.UserDaemonAddr(ctx, sess.UserID)
		if !alive {
			return nil, fmt.Errorf("session: нет per-user демона для шелла сессии %s", sessionID)
		}
		return cliremote.OpenShell(addr, sess.Cwd, r.daemonTokenFn(sess.UserID))
	default:
		return spawn.StartLocalShell(ctx, sess.Cwd)
	}
}

// ACPClient отдаёт живого ACP-клиента сессии её владельцу. Используется AG-UI-транспортом
// (через адаптер): acpSession — надмножество Bindable. ok=false, если сессия неизвестна,
// не в ACP-режиме или принадлежит другому пользователю.
func (r *Registry) ACPClient(sessionID, userID string) (acpSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lv, ok := r.live[sessionID]
	if !ok || lv.owner != userID || lv.client == nil {
		return nil, false
	}
	return lv.client, true
}

// EnsureACPClient возвращает живого ACP-клиента сессии, при необходимости пере-подняв среду
// агента с resume. Нужен для случая, когда среда умерла ВНЕ рестарта brigade (RestoreAll не
// сработал): docker-контейнер демона остановлен (`docker stop`) или убит, local-адаптер упал.
// In-memory клиент при этом указывает на мёртвую среду — первый же turn упёрся бы в отказ.
// Проверяем живость (docker — по контейнеру демона, local — по Alive адаптера) и, если мертва,
// пере-поднимаем через resume (session/load реплеит thread из durable-состояния), подменяя
// живой объект. ok=false — сессия неизвестна/чужая/не ACP либо respawn не удался. Вызывается
// AG-UI-транспортом перед выдачей Bindable для turn'а.
func (r *Registry) EnsureACPClient(ctx context.Context, sessionID, userID string) (acpSession, bool) {
	r.mu.Lock()
	lv, ok := r.live[sessionID]
	r.mu.Unlock()
	if !ok || lv.owner != userID || lv.client == nil || lv.kind != store.SessionKindACP {
		return nil, false
	}
	if r.acpAlive(ctx, lv, sessionID) {
		return lv.client, true
	}

	// Мертва — сериализуем respawn per-session (без лока два turn'а подняли бы две среды).
	unlock := r.lockSession(sessionID)
	defer unlock()
	// Повторная проверка под локом: параллельный ensure мог уже пере-поднять среду.
	r.mu.Lock()
	lv = r.live[sessionID]
	r.mu.Unlock()
	if lv == nil || lv.client == nil {
		return nil, false
	}
	if r.acpAlive(ctx, lv, sessionID) {
		return lv.client, true
	}

	sess, err := r.Get(ctx, sessionID, userID)
	if err != nil {
		log.Printf("session: ensure acp %s: get session: %v", sessionID, err)
		return nil, false
	}
	newLv, err := r.reviveACP(ctx, sess)
	if err != nil {
		log.Printf("session: ensure acp %s: revive: %v", sessionID, err)
		return nil, false
	}
	r.mu.Lock()
	old := r.live[sessionID]
	r.live[sessionID] = newLv
	r.mu.Unlock()
	if old != nil && old.client != nil {
		_ = old.client.Close() // старый поток мёртв — отцепляем без ожидания
	}
	return newLv.client, true
}

// PromptResult — содержательный ответ turn'а для внешнего персонального транспорта.
type PromptResult struct {
	Text   string
	Images []acp.GeneratedImageFile
}

// PromptAutoApprove отправляет prompt в ACP-сессию из доверенного персонального канала.
// История остаётся общей с web-чатом.
func (r *Registry) PromptAutoApprove(ctx context.Context, sessionID, userID, text string) (PromptResult, error) {
	client, ok := r.EnsureACPClient(ctx, sessionID, userID)
	if !ok {
		return PromptResult{}, store.ErrNotFound
	}
	if _, err := client.PromptAutoApprove(ctx, text, nil); err != nil {
		return PromptResult{}, err
	}
	messages := client.Messages()
	start := len(messages)
	for start > 0 {
		start--
		if messages[start].Role == "user" {
			start++
			break
		}
	}
	var parts []string
	var images []acp.GeneratedImageFile
	for _, message := range messages[start:] {
		if message.Role == "assistant" && strings.TrimSpace(message.Content) != "" {
			parts = append(parts, strings.TrimSpace(message.Content))
		} else if message.Role == "tool_call" {
			images = append(images, acp.GeneratedImageFiles(message.Result)...)
		}
	}
	return PromptResult{Text: strings.Join(parts, "\n\n"), Images: images}, nil
}

// acpAlive проверяет живость среды ACP-сессии: docker — существует ли запущенный контейнер
// демона с опубликованным портом (тот же зонд, что и RestoreAll); local — жив ли процесс
// адаптера (Alive у *acp.Client). Спавнер не docker или клиент без Alive — считаем живым
// (не мешаем работать, respawn не по адресу).
func (r *Registry) acpAlive(ctx context.Context, lv *live, sessionID string) bool {
	if lv.mode == store.SessionModeDocker {
		ds, ok := r.spawner.(*spawn.DockerSpawner)
		if !ok {
			return true
		}
		_, alive := ds.ACP().DaemonAddr(ctx, sessionID)
		return alive
	}
	if a, ok := lv.client.(interface{ Alive() bool }); ok {
		return a.Alive()
	}
	return true
}

// reviveACP пере-поднимает среду ACP-сессии с resume и возвращает новый live-объект (в реестр
// НЕ пишет — это делает вызывающий). docker: пересоздаёт контейнер демона (StartDaemon сносит
// остановленный и поднимает свежий) + session/load; local: новый subprocess-адаптер + session/load.
// Диалог сохраняется — агент реплеит thread по agent_session_id.
func (r *Registry) reviveACP(ctx context.Context, sess store.Session) (*live, error) {
	token := r.agentToken(ctx, sess)
	if sess.Mode == store.SessionModeDocker {
		lv, sid, _, err := r.spawnACPDaemon(ctx, sess, token, "", sess.AgentSessionID, "")
		if err != nil {
			return nil, err
		}
		if err := r.store.UpdateSessionResume(ctx, sess.ID, sid, ""); err != nil {
			_ = lv.client.Close()
			return nil, fmt.Errorf("persist acp resume: %w", err)
		}
		return lv, nil
	}
	opts := r.acpLocalOptions(ctx, sess, token)
	opts.ResumeSessionID = sess.AgentSessionID
	client, err := acp.New(ctx, opts)
	if err != nil {
		return nil, err
	}
	client.OnTurnEnd = r.turnEndHook(sess)
	if err := r.store.UpdateSessionResume(ctx, sess.ID, client.SessionID(), ""); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("persist acp resume: %w", err)
	}
	return &live{owner: sess.UserID, kind: sess.Kind, mode: sess.Mode, client: client}, nil
}

// ReloadAgent перезапускает ACP-агента сессии и сохраняет диалог через session/load того же
// agent_session_id. local получает свежий subprocess, docker — новый контейнер с актуальным
// runtime-образом и всеми его слоями. Активный turn сначала отменяется: иначе зависший на
// permission старый daemon невозможно обновить до версии, которая восстановит этот запрос.
func (r *Registry) ReloadAgent(ctx context.Context, sessionID, userID string) error {
	lv, sess, err := r.reloadable(ctx, sessionID, userID, true)
	if err != nil {
		return err
	}
	return r.reinit(ctx, lv, sess, true)
}

// SetInstructionProfile применяет внутренние инструкции к уже существующей ACP-сессии.
// Нужен для guest-сессий, созданных до появления профиля: адаптер переинициализируется через
// session/load, поэтому исходная переписка сохраняется и служебный текст в неё не попадает.
func (r *Registry) SetInstructionProfile(ctx context.Context, sessionID, userID, profile string) error {
	lv, sess, err := r.reloadable(ctx, sessionID, userID, false)
	if err != nil {
		return err
	}
	if sess.InstructionProfile == profile {
		return nil
	}
	previous := sess.InstructionProfile
	if err := r.store.UpdateSessionInstructionProfile(ctx, sessionID, profile); err != nil {
		return err
	}
	sess.InstructionProfile = profile
	if err := r.reinit(ctx, lv, sess, false); err != nil {
		_ = r.store.UpdateSessionInstructionProfile(context.WithoutCancel(ctx), sessionID, previous)
		return err
	}
	return nil
}

// SetSessionMcpServers меняет набор MCP-серверов сессии. У ACP-сессии набор применяется
// сразу — переинициализацией агента (session/load того же диалога: переписка сохраняется,
// инструменты меняются). У CLI-сессии переписывается .mcp.json: `claude` читает его при
// старте, поэтому новый набор подхватится при следующем запуске агента.
//
// Неразрешимая ссылка на секрет отвергается здесь, а не при следующем спавне: пользователь
// у экрана и может починить конфиг.
func (r *Registry) SetSessionMcpServers(ctx context.Context, sessionID, userID string, serverIDs []string) error {
	sess, err := r.Get(ctx, sessionID, userID)
	if err != nil {
		return err
	}
	// Живой ACP-агент переинициализируется на месте — отказываем сразу, если он занят
	// ходом, чтобы не сохранить набор, который не удастся применить.
	var lv *live
	if sess.Kind == store.SessionKindACP {
		if lv, sess, err = r.reloadable(ctx, sessionID, userID, false); err != nil {
			return err
		}
	}
	if err := r.checkMcp(ctx, userID, serverIDs); err != nil {
		return err
	}
	if err := r.store.UpdateSessionMcp(ctx, sessionID, serverIDs); err != nil {
		return err
	}
	sess.McpServers = serverIDs
	if lv == nil {
		r.installMcpProject(ctx, sess)
		return nil
	}
	return r.reinit(ctx, lv, sess, false)
}

// reloadable отдаёт живую ACP-сессию пользователя, готовую к переинициализации. Явный reload
// может отменить активный turn; изменения настроек по-прежнему отказывают во время генерации.
func (r *Registry) reloadable(ctx context.Context, sessionID, userID string, cancelGenerating bool) (*live, store.Session, error) {
	r.mu.Lock()
	lv, ok := r.live[sessionID]
	r.mu.Unlock()
	if !ok || lv.owner != userID || lv.client == nil {
		return nil, store.Session{}, store.ErrNotFound
	}
	if gen, _ := lv.client.Status(); gen {
		if !cancelGenerating {
			return nil, store.Session{}, ErrReloadWhileGenerating
		}
		if err := lv.client.Cancel(ctx); err != nil {
			return nil, store.Session{}, fmt.Errorf("session: cancel before reload: %w", err)
		}
	}
	sess, err := r.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, store.Session{}, err
	}
	return lv, sess, nil
}

// reinit переподнимает адаптер сессии с актуальными плагинами и MCP-серверами, сохраняя
// диалог (session/load того же agent_session_id).
func (r *Registry) reinit(ctx context.Context, lv *live, sess store.Session, refreshRuntime bool) error {
	sessionID := sess.ID
	// Обновляем файлы скиллов до реинициализации, чтобы свежая загрузка их прочитала.
	r.installSkill(sess)

	if sess.Mode == store.SessionModeDocker {
		unlock := r.lockSession(sessionID)
		defer unlock()
		r.mu.Lock()
		current := r.live[sessionID]
		r.mu.Unlock()
		if current != lv {
			return store.ErrNotFound
		}
		if refreshRuntime {
			ds, ok := r.spawner.(*spawn.DockerSpawner)
			if !ok {
				return fmt.Errorf("session: docker-режим без DockerSpawner")
			}
			// Сначала готовим свежий runtime: при ошибке pull текущий контейнер остаётся
			// живым, и пользователь не теряет рабочую сессию.
			if err := ds.RefreshRuntime(ctx); err != nil {
				return fmt.Errorf("session: refresh agent runtime: %w", err)
			}
		}

		// Пересоздаём контейнер; workspace, agent home и журнал остаются в bind-mount,
		// а session/load восстанавливает тот же диалог.
		_ = lv.terminate(ctx)
		newLv, err := r.reviveACP(ctx, sess)
		if err != nil {
			return fmt.Errorf("session: reload acp daemon: %w", err)
		}
		r.mu.Lock()
		if r.live[sessionID] != lv {
			r.mu.Unlock()
			_ = newLv.terminate(context.WithoutCancel(ctx))
			return store.ErrNotFound
		}
		r.live[sessionID] = newLv
		r.mu.Unlock()
		return nil
	}

	// local: переподнимаем subprocess-адаптер с resume — session/load читает свежие плагины из
	// проектного settings.json (--setting-sources project). Новый клиент заменяет старый.
	token := r.agentToken(ctx, sess)
	opts := r.acpLocalOptions(ctx, sess, token)
	opts.ResumeSessionID = sess.AgentSessionID
	client, err := acp.New(ctx, opts)
	if err != nil {
		return fmt.Errorf("session: reload acp: %w", err)
	}
	client.OnTurnEnd = r.turnEndHook(sess)
	r.mu.Lock()
	// Проверяем, что live не сменился (Stop/Delete в гонке) перед подменой.
	if cur, ok := r.live[sessionID]; !ok || cur != lv {
		r.mu.Unlock()
		_ = client.Close()
		return store.ErrNotFound
	}
	old := lv.client
	lv.client = client
	r.mu.Unlock()
	_ = old.Close()
	_ = r.store.UpdateSessionResume(ctx, sess.ID, client.SessionID(), "")
	return nil
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

// OpenWorkspaceFile открывает обычный файл внутри workspace сессии её владельцу.
// os.Root не позволяет пути и симлинкам выйти за границы workspace.
func (r *Registry) OpenWorkspaceFile(ctx context.Context, sessionID, userID, name string) (*os.File, error) {
	sess, err := r.Get(ctx, sessionID, userID)
	if err != nil {
		return nil, err
	}
	name = filepath.FromSlash(name)
	if !filepath.IsLocal(name) {
		return nil, os.ErrNotExist
	}
	rootPath := r.hostCwd(sess)
	if rootPath == "" {
		return nil, os.ErrNotExist
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		if err != nil {
			return nil, err
		}
		return nil, os.ErrNotExist
	}
	return f, nil
}

// Fork создаёт ветку ACP-сессии: агент клонирует исходную сессию с историей
// (session/fork), brigade регистрирует новую запись с parent_id и живым клиентом.
// Ветка продолжается независимо от родителя. Только ACP-сессии: CLI (pty) ветвлению не
// подлежит. Чужая сессия трактуется как ненайденная (см. Get).
func (r *Registry) Fork(ctx context.Context, sessionID, userID string) (store.Session, error) {
	src, err := r.Get(ctx, sessionID, userID)
	if err != nil {
		return store.Session{}, err
	}
	if src.Kind != store.SessionKindACP {
		return store.Session{}, fmt.Errorf("session: fork поддержан только для acp-сессий")
	}
	if src.AgentSessionID == "" {
		return store.Session{}, fmt.Errorf("session: исходная сессия не имеет agent_session_id")
	}

	name := src.Name
	if name == "" {
		name = src.AgentType
	}
	sess := store.Session{
		ID:        uuid.NewString(),
		UserID:    userID,
		Mode:      src.Mode,
		Kind:      store.SessionKindACP,
		AgentType: src.AgentType,
		Status:    store.SessionStatusRunning,
		Cwd:       src.Cwd,
		CreatedAt: time.Now(),
		Name:      name + " · ветка",
		ParentID:  src.ID,
		// Ветка продолжает ту же работу — набор инструментов и образ наследуются от
		// родителя.
		McpServers:         src.McpServers,
		Image:              src.Image,
		AuthProfile:        src.AuthProfile,
		InstructionProfile: src.InstructionProfile,
	}
	if err := r.store.CreateSession(ctx, sess); err != nil {
		return store.Session{}, err
	}
	r.installSkill(sess)
	r.provisionAgentSSH(ctx, sess)

	// Как и в Create: жизнь агента равна жизни сессии, спавн отвязывается от ctx запроса.
	token := r.agentToken(ctx, sess)
	var lv *live
	var sid string
	if sess.Mode == store.SessionModeDocker {
		// docker: ветка тоже через durable-демон (session/fork в Configure).
		l, s, _, err := r.spawnACPDaemon(context.WithoutCancel(ctx), sess, token, "", "", src.AgentSessionID)
		if err != nil {
			_ = r.store.DeleteSession(ctx, sess.ID)
			return store.Session{}, fmt.Errorf("session: fork acp: %w", err)
		}
		lv, sid = l, s
	} else {
		opts := r.acpLocalOptions(ctx, sess, token)
		opts.ForkFromSessionID = src.AgentSessionID
		client, err := acp.New(context.WithoutCancel(ctx), opts)
		if err != nil {
			// Ветка не создалась — запись убираем целиком, полусозданное состояние хуже ошибки.
			_ = r.store.DeleteSession(ctx, sess.ID)
			return store.Session{}, fmt.Errorf("session: fork acp: %w", err)
		}
		client.OnTurnEnd = r.turnEndHook(sess)
		lv, sid = &live{owner: userID, kind: sess.Kind, mode: sess.Mode, client: client}, client.SessionID()
	}

	if err := r.store.UpdateSessionResume(ctx, sess.ID, sid, ""); err != nil {
		_ = lv.client.Close()
		_ = r.store.DeleteSession(ctx, sess.ID)
		return store.Session{}, err
	}
	sess.AgentSessionID = sid

	// session/fork (в отличие от session/load) историю НЕ реплеит — агент новую сессию
	// сообщениями не наполняет, поэтому ledger ветки пуст и её чат открывается пустым.
	// Засеиваем ветку снимком ленты родителя, который brigade уже держит в памяти (тот же
	// источник, что кормит GetHistory). Родитель не в памяти (напр. не открыт после рестарта) —
	// baseline пустой; допустимая деградация, т.к. форкают обычно открытую сессию.
	r.mu.Lock()
	srcLive := r.live[src.ID]
	r.mu.Unlock()
	if srcLive != nil && srcLive.client != nil {
		lv.client.SeedMessages(srcLive.client.Messages())
	}

	r.mu.Lock()
	r.live[sess.ID] = lv
	r.mu.Unlock()

	log.Printf("session: forked %s -> %s (agent %s -> %s)",
		src.ID, sess.ID, src.AgentSessionID, sess.AgentSessionID)
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
	sess, err := r.Get(ctx, sessionID, userID)
	if err != nil {
		return err
	}

	lv, err := r.beginTeardown(sessionID)
	if err != nil {
		return err
	}
	defer r.endTeardown(sessionID)

	if lv != nil {
		tctx, cancel := terminateCtx(ctx)
		_ = lv.terminate(tctx)
		cancel()
	}
	r.previews.Drop(sessionID)
	r.removeCodexAuth(sess)
	// Последняя docker-CLI сессия пользователя закрыта → общий контейнер не нужен.
	r.releaseUserContainerIfIdle(userID)
	log.Printf("session: stopped %s by user=%s", sessionID, userID)
	return r.store.UpdateSessionStatus(ctx, sessionID, store.SessionStatusStopped)
}

// Delete останавливает сессию (если жива) и удаляет её запись из store.
func (r *Registry) Delete(ctx context.Context, sessionID, userID string) error {
	sess, err := r.Get(ctx, sessionID, userID)
	if err != nil {
		return err
	}

	lv, err := r.beginTeardown(sessionID)
	if err != nil {
		return err
	}
	defer r.endTeardown(sessionID)

	if lv != nil {
		tctx, cancel := terminateCtx(ctx)
		_ = lv.terminate(tctx)
		cancel()
	}
	r.previews.Drop(sessionID)
	r.removeCodexAuth(sess)
	// Последняя docker-CLI сессия пользователя закрыта → общий контейнер не нужен.
	r.releaseUserContainerIfIdle(userID)
	log.Printf("session: deleted %s by user=%s", sessionID, userID)
	return r.store.DeleteSession(ctx, sessionID)
}

func (r *Registry) removeCodexAuth(sess store.Session) {
	if agent.Get(sess.AgentType).ID != agent.Codex.ID || sess.AuthProfile != "chatgpt" {
		return
	}
	path := filepath.Join(r.hostCwd(sess), ".brigade", "codex-home", "auth.json")
	if data, err := os.ReadFile(path); err == nil && json.Valid(data) {
		if err := r.store.SetCodexAuthJSON(context.Background(), sess.UserID, string(data)); err != nil {
			log.Printf("session %s: persist codex auth: %v", sess.ID, err)
		}
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("session %s: remove codex auth: %v", sess.ID, err)
	}
}

// UploadFile кладёт файл в рабочую директорию агента (uploads/<имя>) ЧЕРЕЗ ФАСАД сессии
// (acpSession.WriteFile), а не через docker-API: docker-сессия пишет демоном внутри
// контейнера, local — напрямую в свой cwd. brigade не завязан на способ спавна. Возвращает
// путь относительно cwd (агент читает файл по нему). Транспорт AG-UI текстовый, поэтому
// вложения доставляются через файловую систему, а не в теле сообщения.
func (r *Registry) UploadFile(ctx context.Context, sessionID, userID, filename string, content []byte) (string, error) {
	client, ok := r.ACPClient(sessionID, userID)
	if !ok {
		return "", fmt.Errorf("session: нет живого агента для сессии %s", sessionID)
	}
	rel := "uploads/" + sanitizeUploadName(filename)
	if err := client.WriteFile(ctx, rel, content); err != nil {
		return "", fmt.Errorf("session: upload: %w", err)
	}
	return rel, nil
}

// sanitizeUploadName берёт базовое имя файла и заменяет небезопасные символы, гарантируя
// непустое безопасное имя (защита от path traversal и экзотических имён).
func sanitizeUploadName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, name)
	name = strings.Trim(name, ".-")
	if name == "" {
		name = "file"
	}
	return name
}

// archiveRecapPrompt — служебный промпт recap при архивации. Просит краткий пересказ
// сессии из контекста агента; ответ сохраняется как summary архивной карточки.
const archiveRecapPrompt = "Кратко, в 1–2 предложениях, суммируй эту сессию для архива: " +
	"что делали и чем закончили. Ответь только самим пересказом, без вступлений и списков."

// Archive переносит сессию в архив: пока агент жив — снимает ленту чата в БД (для
// readonly-просмотра без агента) и генерирует recap (summary), затем останавливает
// контейнер как Stop и помечает сессию archived. Идемпотентно для уже архивной. Снимок и
// recap возможны только для ACP с живым клиентом; для прочих summary остаётся пустым.
func (r *Registry) Archive(ctx context.Context, sessionID, userID string) (memory.ArchivedSession, error) {
	sess, err := r.Get(ctx, sessionID, userID)
	if err != nil {
		return memory.ArchivedSession{}, err
	}
	if r.archive == nil {
		return memory.ArchivedSession{}, errors.New("session: архив недоступен — не настроена память")
	}

	r.mu.Lock()
	lv := r.live[sessionID]
	r.mu.Unlock()

	var messages []byte
	summary := ""
	if lv != nil && lv.client != nil {
		// Снимок ленты ДО recap: служебный recap-turn не должен попасть в архивную историю.
		if data, err := json.Marshal(lv.client.Messages()); err != nil {
			log.Printf("session: archive %s marshal history: %v", sessionID, err)
		} else {
			messages = data
		}
		// Recap на неотменяемом контексте: RPC мог вернуться раньше, чем агент ответит.
		if s, err := lv.client.Summarize(context.WithoutCancel(ctx), archiveRecapPrompt); err != nil {
			log.Printf("session: archive %s recap: %v", sessionID, err)
		} else {
			summary = s
		}
	}

	archived := memory.ArchivedSession{
		ID:        sess.ID,
		Name:      sess.Name,
		AgentType: sess.AgentType,
		Kind:      string(sess.Kind),
		ParentID:  sess.ParentID,
		Summary:   summary,
		Created:   sess.CreatedAt,
		Archived:  time.Now(),
	}
	// Перенос в память — ДО удаления из БД: пока сессия не уехала в git (а значит и на
	// remote пользователя), удалять её нельзя, иначе сбой push'а стоил бы всей истории.
	if err := r.archive.ArchiveSession(ctx, userID, archived, messages); err != nil {
		return memory.ArchivedSession{}, fmt.Errorf("session: archive %s: %w", sessionID, err)
	}

	// Teardown контейнера (как Stop): снять live-объект и завершить среду.
	if tv, err := r.beginTeardown(sessionID); err == nil {
		defer r.endTeardown(sessionID)
		if tv != nil {
			tctx, cancel := terminateCtx(ctx)
			_ = tv.terminate(tctx)
			cancel()
		}
		r.previews.Drop(sessionID)
		r.removeCodexAuth(sess)
		r.releaseUserContainerIfIdle(userID)
	}
	// Сессия целиком переехала в память — в БД ей больше не место.
	if err := r.store.DeleteSession(ctx, sessionID); err != nil {
		log.Printf("session: archive %s delete row: %v", sessionID, err)
	}

	log.Printf("session: archived %s by user=%s", sessionID, userID)
	return archived, nil
}

// ListArchived возвращает архивные сессии пользователя из памяти (новые первыми).
func (r *Registry) ListArchived(ctx context.Context, userID string) ([]memory.ArchivedSession, error) {
	if r.archive == nil {
		return nil, nil
	}
	return r.archive.ListArchivedSessions(ctx, userID)
}

// DeleteArchived удаляет сессию из архива насовсем.
func (r *Registry) DeleteArchived(ctx context.Context, sessionID, userID string) error {
	if r.archive == nil {
		return store.ErrNotFound
	}
	return r.archive.DeleteArchivedSession(ctx, userID, sessionID)
}

// ArchivedHistory возвращает снимок ленты чата архивной сессии для readonly-рендера.
// Владение проверять не нужно: архив читается из репозитория самого пользователя.
func (r *Registry) ArchivedHistory(ctx context.Context, sessionID, userID string) ([]acp.Message, error) {
	if r.archive == nil {
		return nil, store.ErrNotFound
	}
	data, err := r.archive.ArchivedMessages(ctx, userID, sessionID)
	if err != nil {
		return nil, err
	}
	var msgs []acp.Message
	if err := json.Unmarshal(data, &msgs); err != nil {
		return nil, fmt.Errorf("session: unmarshal snapshot %s: %w", sessionID, err)
	}
	return msgs, nil
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

	cliUsers := make(map[string]struct{})
	for _, sess := range sessions {
		if sess.Kind == store.SessionKindCLI && sess.Mode == store.SessionModeDocker {
			cliUsers[sess.UserID] = struct{}{}
		}
		if err := r.restoreOne(ctx, sess); err != nil {
			log.Printf("session: восстановление %s (%s/%s) не удалось: %v",
				sess.ID, sess.Mode, sess.Kind, err)
			_ = r.store.UpdateSessionStatus(ctx, sess.ID, store.SessionStatusFailed)
			r.removeCodexAuth(sess)
		}
	}
	// Свип общих per-user контейнеров: если все docker-CLI сессии пользователя не
	// восстановились (failed/stopped), его контейнер остался без сессий — удаляем.
	for userID := range cliUsers {
		r.releaseUserContainerIfIdle(userID)
	}
	return nil
}

// restoreOne восстанавливает одну сессию и регистрирует её живой объект.
func (r *Registry) restoreOne(ctx context.Context, sess store.Session) error {
	// Переустанавливаем скиллы перед респавном агента (как в Create): апгрейд brigade,
	// добавивший новый скилл, должен долетать и в уже существующие сессии при рестарте, а
	// не только в новые. Идемпотентно; для сессий, которые не респавнятся, — безвредно.
	r.installSkill(sess)
	r.provisionAgentSSH(ctx, sess)

	switch sess.Kind {
	case store.SessionKindCLI:
		// В local-режиме восстановить CLI-сессию можно только через `claude --resume
		// <id>`, для чего нужен agent_session_id. brigade его не получает (claude не
		// сообщает идентификатор структурно), поэтому для свежих CLI-сессий он пуст.
		// Рестарт бэкенда к тому же завершает дочерние процессы, так что такая сессия
		// объективно мертва — помечаем её stopped, а не пытаемся (заведомо неудачно)
		// переподключиться. Это штатный исход, не ошибка восстановления.
		//
		// В docker-режиме агент живёт в контейнере, переживающем рестарт бэкенда.
		// Legacy-схема (непустой container_label) переподключается по label
		// brigade.session.id без agent_session_id; shared-схема (пустой label)
		// перезапускает `claude --resume <agent_session_id>` exec'ом в общем
		// контейнере пользователя — id обязателен и задан при спавне (--session-id).
		if sess.Mode == store.SessionModeDocker {
			// docker: CLI-агент живёт durable-терминалом per-user демона (переживает рестарт
			// brigade). Восстановление = reconnect (демон переотдаёт scrollback, claude не
			// прерывался); контейнер мёртв → EnsureUserDaemon поднимет заново, `claude --resume`
			// реплеит переписку. Лок держим до регистрации live (как в Create): свип контейнеров
			// в конце RestoreAll под тем же локом не снесёт контейнер в окне «готов, но не записан».
			unlock := r.lockUser(sess.UserID)
			lv, _, _, err := r.spawnCLIDaemon(ctx, sess, r.agentToken(ctx, sess), sess.AgentSessionID)
			if err != nil {
				unlock()
				return fmt.Errorf("restore cli: %w", err)
			}
			r.mu.Lock()
			r.live[sess.ID] = lv
			r.mu.Unlock()
			unlock()
			return nil
		}
		if agent.Get(sess.AgentType).ID == agent.Codex.ID && sess.AgentSessionID != "" {
			handle, err := r.spawner.Reattach(ctx, spawn.Persisted{SessionID: sess.ID, AgentSessionID: sess.AgentSessionID, Cwd: sess.Cwd, Env: r.agentEnv(ctx, sess, ""), Command: agent.Codex.CommandFor(store.SessionKindCLI)})
			if err != nil {
				return fmt.Errorf("restore codex cli: %w", err)
			}
			go r.watchExit(sess.ID, handle)
			r.mu.Lock()
			r.live[sess.ID] = &live{owner: sess.UserID, kind: sess.Kind, mode: sess.Mode, handle: handle}
			r.mu.Unlock()
			return nil
		}
		// local: процесс агента умер с рестартом brigade — сессия объективно мертва.
		return r.store.UpdateSessionStatus(ctx, sess.ID, store.SessionStatusStopped)

	case store.SessionKindACP:
		token := r.agentToken(ctx, sess)
		if sess.Mode == store.SessionModeDocker {
			// docker: демон и адаптер живут в контейнере и ПЕРЕЖИВАЮТ рестарт brigade.
			// Восстановление = reconnect к живому демону (turn не прерывался). Если контейнер
			// мёртв (перезагрузка хоста) — respawn демона + session/load из durable-volume.
			var rc acpSession
			sid := sess.AgentSessionID
			if ds, ok := r.spawner.(*spawn.DockerSpawner); ok {
				if addr, alive := ds.ACP().DaemonAddr(ctx, sess.ID); alive {
					c := acpremote.New(addr, sess.AgentSessionID, r.daemonTokenFn(sess.ID))
					c.OnTurnEnd = r.turnEndHook(sess)
					rc = c
				}
			}
			if rc == nil {
				lv, newSid, _, err := r.spawnACPDaemon(ctx, sess, token, "", sess.AgentSessionID, "")
				if err != nil {
					return fmt.Errorf("restore acp daemon: %w", err)
				}
				rc, sid = lv.client, newSid
			}
			if err := r.store.UpdateSessionResume(ctx, sess.ID, sid, ""); err != nil {
				_ = rc.Close()
				return fmt.Errorf("persist acp resume: %w", err)
			}
			r.mu.Lock()
			r.live[sess.ID] = &live{owner: sess.UserID, kind: sess.Kind, mode: sess.Mode, client: rc, teardown: r.acpDaemonTeardown(sess.ID)}
			r.mu.Unlock()
			return nil
		}
		// local: adapter-subprocess пере-спавнится + session/load (агент реплеит thread).
		// PluginDirs обязателен и здесь (как в Create/ReloadAgent): session/load шлёт плагин-мета
		// из него, иначе resume-агент не перечитает скиллы и оставит их из старого состояния
		// сессии (напр. переименованный скилл не подхватится при рестарте brigade).
		opts := r.acpLocalOptions(ctx, sess, token)
		opts.ResumeSessionID = sess.AgentSessionID
		client, err := acp.New(ctx, opts)
		if err != nil {
			return fmt.Errorf("reattach acp: %w", err)
		}
		client.OnTurnEnd = r.turnEndHook(sess)
		if err := r.store.UpdateSessionResume(ctx, sess.ID, client.SessionID(), ""); err != nil {
			_ = client.Close()
			return fmt.Errorf("persist acp resume: %w", err)
		}
		r.mu.Lock()
		r.live[sess.ID] = &live{owner: sess.UserID, kind: sess.Kind, mode: sess.Mode, client: client}
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
	sessions, _ := r.store.ListSessionsByStatus(context.Background(), store.SessionStatusRunning)
	r.mu.Lock()
	snapshot := r.live
	r.live = make(map[string]*live)
	r.mu.Unlock()

	for _, lv := range snapshot {
		_ = lv.close()
	}
	for _, sess := range sessions {
		r.removeCodexAuth(sess)
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
//
// ctx ограничивает только CLI-путь: Client.Close сигнатуры с контекстом не имеет
// (io.Closer), но самоограничен по времени внутренним бюджетом (~6s суммарно, см.
// gracefulCloseTimeout) — заведомо короче дедлайна terminateCtx.
func (l *live) terminate(ctx context.Context) error {
	if l.handle != nil {
		return l.handle.Terminate(ctx)
	}
	if l.client != nil {
		err := l.client.Close()
		if l.teardown != nil {
			// docker-ACP: удаляем контейнер демона (client.Close лишь отцепил поток).
			if terr := l.teardown(ctx); terr != nil && err == nil {
				err = terr
			}
		}
		return err
	}
	return nil
}

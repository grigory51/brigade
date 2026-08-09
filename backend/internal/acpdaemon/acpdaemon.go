// Package acpdaemon — durable ACP-демон brigade (субкоманда `brigade acp-agent`).
//
// Демон — pid1 контейнера сессии. Владеет ACP-адаптером (claude-agent-acp и т.п.) через
// acp.Client, журналит поток AG-UI событий в eventlog (durable, seq) и отдаёт его brigade
// resumable Connect-RPC (AgentDaemonService). Ключевое отличие от прежней схемы: адаптер и
// журнал живут в контейнере и ПЕРЕЖИВАЮТ рестарт brigade — turn не прерывается, brigade
// переподключается и дочитывает журнал с последнего seq.
//
// acp.Client не меняется: демон использует его публичный API (New/Bind/Prompt/…), а
// durability добавляет сбоку — постоянный sink журналит каждое эмитнутое событие в eventlog.
// Секреты (OAuth-токен, preview-env) приходят в Configure (не в env контейнера) и кладутся
// только в env дочернего процесса адаптера — `/ws/shell` (docker exec) их не видит.
package acpdaemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/grigory51/brigade/backend/gen/go/brigade/v1/brigadev1connect"
	"github.com/grigory51/brigade/backend/internal/acp"
	"github.com/grigory51/brigade/backend/internal/agentauth"
	"github.com/grigory51/brigade/backend/internal/agui"
	"github.com/grigory51/brigade/backend/internal/eventlog"
	"github.com/grigory51/brigade/backend/internal/httpaccess"
	"github.com/grigory51/brigade/backend/internal/sshagent"
)

// customPermissionName — Name CUSTOM-события запроса разрешения (human-in-the-loop).
// Совпадает с переупаковкой в transport/agui: SSE-клиент получает permission именно так.
const customPermissionName = "permission_request"

// errPermissionCancelled — исход, когда решение по разрешению отменено (пустая строка).
var errPermissionCancelled = errors.New("acpdaemon: permission cancelled")

// Daemon — состояние демона одной сессии.
type Daemon struct {
	sessionID string
	version   string
	startedAt time.Time
	log       *eventlog.Log
	perms     *permStore
	terminals *terminalMgr // pty-терминалы (вспом. шелл, CLI-агент)

	mu     sync.Mutex
	client *acp.Client // nil до Configure
	unbind func()
	// ssh — ssh-agent с ключом пользователя в памяти (nil, пока ключ не загружен).
	// Приватный ключ на диск среды не пишется.
	ssh *sshagent.Agent
	// lastConfig — последняя попытка смены опции через daemon RPC. Нужна dump-команде,
	// чтобы отличить неотправленный UI-вызов от ошибки или отката адаптера.
	lastConfig configChangeDebug
}

type configChangeDebug struct {
	ID, Value, Before, After, Error, At string
}

// New создаёт демон с открытым журналом (durable по пути logPath).
func New(sessionID, logPath, version string) (*Daemon, error) {
	l, err := eventlog.Open(logPath)
	if err != nil {
		return nil, err
	}
	return &Daemon{
		sessionID: sessionID, version: version, startedAt: time.Now().UTC(),
		log: l, perms: newPermStore(), terminals: newTerminalMgr(),
	}, nil
}

// journal сериализует AG-UI событие и добавляет его в журнал (получая seq).
func (d *Daemon) journal(evt agui.Event) {
	data, err := json.Marshal(evt)
	if err != nil {
		log.Printf("acpdaemon: marshal event: %v", err)
		return
	}
	if _, err := d.log.Append(data); err != nil {
		log.Printf("acpdaemon: journal append: %v", err)
	}
}

// sink — постоянная привязка доставки: каждое эмитнутое клиентом событие уходит в журнал
// (это и есть поток, эквивалентный SSE). Ошибку не возвращаем — журналирование не должно
// прерывать turn.
func (d *Daemon) sink(evt agui.Event) error {
	d.journal(evt)
	return nil
}

// resolve — постоянный permission-resolver: журналит запрос как CUSTOM-событие (brigade
// увидит его в StreamEvents и покажет диалог), регистрирует ожидание и блокируется до
// ResolvePermission. Отмена (пустое решение / ctx) → cancelled.
func (d *Daemon) resolve(ctx context.Context, req agui.PermissionRequest) (string, error) {
	d.journal(agui.Event{Type: agui.EventCustom, Name: customPermissionName, Value: req})
	ch := d.perms.register(req)
	select {
	case decision := <-ch:
		if decision == "" {
			return "", errPermissionCancelled
		}
		return decision, nil
	case <-ctx.Done():
		d.perms.remove(req.ID)
		return "", ctx.Err()
	}
}

// configure (пере)поднимает адаптер. Идемпотентна: на уже настроенный демон — no-op
// (адаптер не рестартуется), возвращает текущий session_id. Секреты кладутся в env
// адаптера здесь, а не в окружение контейнера.
func (d *Daemon) configure(ctx context.Context, req *v1ConfigureRequest) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.client != nil {
		return d.client.SessionID(), nil // уже настроен — reconnect, адаптер жив
	}

	var mcp []acpsdk.McpServer
	if len(req.McpServersJson) > 0 {
		if err := json.Unmarshal(req.McpServersJson, &mcp); err != nil {
			return "", fmt.Errorf("acpdaemon: mcp_servers_json: %w", err)
		}
	}

	// Respawn после смерти контейнера (resume): агент реплеит переписку через session/load,
	// поэтому старый журнал из durable-volume надо обнулить, иначе он задвоится.
	if req.ResumeSessionId != "" {
		_ = d.log.Reset()
	}

	client, err := acp.New(ctx, acp.Options{
		Cwd:        req.Cwd,
		OAuthToken: req.OauthToken,
		// sshEnv добавляет SSH_AUTH_SOCK: агент подписывает git-операции ключом из памяти
		// демона, не имея самого ключа.
		ExtraEnv:         append(append([]string{}, req.ExtraEnv...), d.sshEnvLocked()...),
		AdapterCommand:   req.AdapterCommand,
		ResumeSessionID:  req.ResumeSessionId,
		McpServers:       mcp,
		PluginDirs:       req.PluginDirs,
		SystemPrompt:     req.SystemPrompt,
		ContainerSandbox: true,
		// SpawnProc nil → локальный subprocess адаптера внутри контейнера.
	})
	if err != nil {
		return "", err
	}
	// Постоянная привязка: sink журналит, resolver обслуживает permission. В отличие от
	// brigade↔фронт, здесь привязка одна на всю жизнь демона (не per-WS-сеанс).
	d.unbind = client.Bind(d.sink, d.resolve)
	d.client = client
	return client.SessionID(), nil
}

// setSSHKey загружает (или заменяет) ключ пользователя в ssh-agent демона, поднимая агента
// при первом вызове. Замена нужна на перевыпуск ключа: демон живёт дольше сессии.
func (d *Daemon) setSSHKey(privatePEM string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ssh != nil {
		return d.ssh.ReplaceKey(privatePEM)
	}
	// Сокет — в /tmp среды: демон один на среду, путь уходит потребителям в SSH_AUTH_SOCK.
	a, err := sshagent.Start(filepath.Join(os.TempDir(), "brigade-ssh-agent.sock"), privatePEM)
	if err != nil {
		return err
	}
	d.ssh = a
	writeSSHConfig(a.Path())
	return nil
}

// writeSSHConfig раскладывает ~/.ssh/config в среде агента: путь к сокету ssh-agent и
// приём хост-ключа при первом подключении. IdentityAgent дублирует SSH_AUTH_SOCK намеренно
// — переменную окружения наследуют только процессы, порождённые демоном, а `ssh` может
// запуститься из скрипта или сервиса с чистым окружением.
//
// Файл пишется здесь, а не кладётся brigade на хост: приватного ключа в нём нет, а среда
// агента может быть чужим образом, куда каталог .ssh не монтируется.
func writeSSHConfig(sockPath string) {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("acpdaemon: ssh config: home: %v", err)
		return
	}
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("acpdaemon: ssh config: mkdir: %v", err)
		return
	}
	cfg := "Host *\n" +
		"    IdentityAgent " + sockPath + "\n" +
		"\n" +
		"Host github.com\n" +
		"    HostName github.com\n" +
		"    User git\n" +
		"    StrictHostKeyChecking accept-new\n"
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(cfg), 0o600); err != nil {
		log.Printf("acpdaemon: ssh config: write: %v", err)
	}
}

// sshEnv — переменные окружения доступа к ssh-agent демона для порождаемых процессов
// (адаптер, терминалы). Пусто, пока ключ не загружен.
func (d *Daemon) sshEnv() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sshEnvLocked()
}

// sshEnvLocked — то же под уже взятым d.mu (configure держит его на всё время).
func (d *Daemon) sshEnvLocked() []string {
	if d.ssh == nil {
		return nil
	}
	return []string{"SSH_AUTH_SOCK=" + d.ssh.Path()}
}

// getClient возвращает настроенный клиент или ошибку FailedPrecondition (не сконфигурирован).
func (d *Daemon) getClient() (*acp.Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.client == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("daemon not configured"))
	}
	return d.client, nil
}

// Close сворачивает клиента и журнал.
func (d *Daemon) Close() {
	d.mu.Lock()
	c := d.client
	unbind := d.unbind
	d.mu.Unlock()
	if unbind != nil {
		unbind()
	}
	if c != nil {
		_ = c.Close()
	}
	d.mu.Lock()
	a := d.ssh
	d.ssh = nil
	d.mu.Unlock()
	if a != nil {
		a.Close()
	}
	_ = d.log.Close()
}

// v1ConfigureRequest — локальный алиас полей DaemonConfigureRequest (чтобы service.go не
// тащил gen-типы в сигнатуру configure). Заполняется в хендлере.
type v1ConfigureRequest struct {
	OauthToken      string
	ExtraEnv        []string
	AdapterCommand  string
	Cwd             string
	ResumeSessionId string
	PluginDirs      []string
	McpServersJson  []byte
	SystemPrompt    string
}

// --- entrypoint ---

// Main — точка входа субкоманды `brigade acp-agent`. Конфиг берётся из env (задаётся
// brigade при создании контейнера; секретов тут нет — только публичный ключ, порт, session id,
// путь журнала):
//
//	BRIGADE_SESSION_ID    — id сессии
//	BRIGADE_DAEMON_PORT   — порт Connect-сервера демона
//	BRIGADE_DAEMON_PUBKEY — публичный Ed25519-ключ brigade (проверка подписи вызовов)
//	BRIGADE_DAEMON_LOG    — путь журнала событий (дефолт ~/.brigade/acp-events.jsonl)
func Main(version string) int {
	sessionID := os.Getenv("BRIGADE_SESSION_ID")
	port := os.Getenv("BRIGADE_DAEMON_PORT")
	pubKey := os.Getenv("BRIGADE_DAEMON_PUBKEY")
	logPath := os.Getenv("BRIGADE_DAEMON_LOG")
	if logPath == "" {
		home, _ := os.UserHomeDir()
		logPath = filepath.Join(home, ".brigade", "acp-events.jsonl")
	}
	if port == "" {
		log.Printf("acpdaemon: BRIGADE_DAEMON_PORT not set")
		return 2
	}

	d, err := New(sessionID, logPath, version)
	if err != nil {
		log.Printf("acpdaemon: init: %v", err)
		return 1
	}
	defer d.Close()

	var verifier *agentauth.Verifier
	if pubKey != "" {
		v, err := agentauth.NewVerifier(pubKey)
		if err != nil {
			log.Printf("acpdaemon: bad daemon pubkey: %v", err)
			return 2
		}
		verifier = v
	}

	mux := http.NewServeMux()
	interceptors := connect.WithInterceptors(tokenInterceptor{verifier: verifier, sessionID: sessionID})
	mux.Handle(brigadev1connect.NewAgentDaemonServiceHandler(&service{d: d}, interceptors))

	addr := ":" + port
	log.Printf("acpdaemon: session=%s listening on %s log=%s", sessionID, addr, logPath)
	if err := http.ListenAndServe(addr, httpaccess.Wrap("acp-agent", mux)); err != nil {
		log.Printf("acpdaemon: server: %v", err)
		return 1
	}
	return 0
}

// tokenInterceptor проверяет подпись вызова публичным ключом brigade (asymmetric-auth) на
// unary и streaming вызовах. brigade подписывает короткоживущий токен приватным ключом, демон
// проверяет публичным (из env); токен адресован этой сессии (aud=sessionID) — отсекает и
// случайные обращения, и replay токена от другой сессии.
type tokenInterceptor struct {
	verifier  *agentauth.Verifier
	sessionID string
}

func (t tokenInterceptor) check(h http.Header) error {
	if t.verifier == nil {
		return nil // ключ не задан — проверка выключена (dev)
	}
	got, ok := strings.CutPrefix(h.Get("Authorization"), "Bearer ")
	if !ok {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("missing daemon token"))
	}
	if err := t.verifier.Verify(strings.TrimSpace(got), t.sessionID); err != nil {
		return connect.NewError(connect.CodeUnauthenticated, err)
	}
	return nil
}

func (t tokenInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := t.check(req.Header()); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (t tokenInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (t tokenInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := t.check(conn.RequestHeader()); err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

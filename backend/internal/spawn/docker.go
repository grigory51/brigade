package spawn

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

// labelSessionID — ключ label, помечающий контейнер сессии brigade. По нему
// контейнер отыскивается при Reattach после рестарта бэкенда.
const labelSessionID = "brigade.session.id"

// dockerStopTimeoutSeconds — бюджет штатной остановки контейнера (ContainerStop), после
// которого docker эскалирует до SIGKILL.
const dockerStopTimeoutSeconds = 5

// defaultImage — образ контейнера агента по умолчанию (если Spec.Image пуст).
const defaultImage = "brigade/claude-agent:latest"

// AgentUID/AgentGID — uid/gid пользователя agent в образе (зафиксированы в
// docker/claude-agent/Dockerfile). brigade chown'ит персональный ~/.claude на них,
// чтобы bind-mount был writable агентом (иначе root-owned mount → EACCES).
const (
	AgentUID = 1001
	AgentGID = 1001
)

// AgentHome — домашний каталог пользователя agent в контейнере. Bind-mount'ится
// целиком с хоста (per-user), чтобы состояние Claude (~/.claude, ~/.claude.json) и
// рабочие файлы (~/workspace) переживали сессии и были общими между контейнерами
// пользователя. Экспортирован — реестр строит cwd относительно него.
const AgentHome = "/home/agent"

// ContainerWorkdir — рабочая директория агента в docker-режиме: подпапка home
// (переживает контейнеры, не расшарена между пользователями, т.к. home per-user).
const ContainerWorkdir = AgentHome + "/workspace"
const containerWorkdir = ContainerWorkdir

// DockerSpawner запускает каждого агента в отдельном контейнере (контейнер на сессию).
type DockerSpawner struct {
	cli *client.Client
	// selfNetwork — docker-сеть, в которой работает сам brigade (если он в контейнере).
	// Контейнеры сессий подключаются к ней, чтобы агент достучался до API brigade по
	// его hostname. Пусто, если brigade — процесс на хосте (тогда агент ходит через
	// host.docker.internal / host-gateway).
	selfNetwork string
	// selfHost — hostname, по которому агент обращается к API brigade: имя контейнера
	// brigade в общей сети (self-container mode) либо "host.docker.internal" (host mode).
	selfHost string
}

// NewDockerSpawner создаёт DockerSpawner с клиентом Docker из окружения
// (DOCKER_HOST и т. п.). Определяет собственную сеть brigade: если процесс работает
// в docker-контейнере, контейнеры сессий будут спавниться в ту же сеть, чтобы агент
// мог обратиться к API brigade по имени сервиса. Клиент следует закрыть через Close.
func NewDockerSpawner() (*DockerSpawner, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("spawn: docker client: %w", err)
	}
	s := &DockerSpawner{cli: cli, selfHost: "host.docker.internal"}
	s.detectSelfNetwork(context.Background())
	return s, nil
}

// detectSelfNetwork определяет, работает ли brigade в docker-контейнере, и если да —
// запоминает его первую сеть и hostname для сетевого соединения с агентами. Любая
// ошибка (brigade не в контейнере, hostname не резолвится в контейнер) — не фатальна:
// остаётся режим host.docker.internal. Best-effort, вызывается один раз на старте.
func (s *DockerSpawner) detectSelfNetwork(ctx context.Context) {
	hostname, err := os.Hostname()
	if err != nil {
		return
	}
	// В контейнере hostname по умолчанию — short container id; docker принимает его
	// как идентификатор для inspect. Вне контейнера inspect вернёт ошибку — это ок.
	info, err := s.cli.ContainerInspect(ctx, hostname)
	if err != nil {
		log.Printf("spawn: not running in a container (%v) — agents reach API via host.docker.internal", err)
		return
	}
	if info.NetworkSettings == nil {
		return
	}
	for name := range info.NetworkSettings.Networks {
		// bridge/host/none — служебные сети без DNS по именам контейнеров; для связи
		// агент→brigade по hostname нужна user-defined сеть (её создаёт compose).
		if name == "bridge" || name == "host" || name == "none" {
			continue
		}
		s.selfNetwork = name
		// Имя контейнера brigade (без ведущего "/") — по нему агент к нему обратится.
		s.selfHost = trimLeadingSlash(info.Name)
		log.Printf("spawn: brigade in container %q on network %q — agents join it, API host=%q",
			s.selfHost, s.selfNetwork, s.selfHost)
		return
	}
	log.Printf("spawn: brigade container has no user-defined network — agents reach API via host.docker.internal")
}

// APIHost возвращает hostname, по которому агент в контейнере обращается к API
// brigade: имя контейнера brigade (если он сам в user-defined сети) либо
// host.docker.internal (host mode / только служебные сети).
func (s *DockerSpawner) APIHost() string { return s.selfHost }

func trimLeadingSlash(s string) string {
	if len(s) > 0 && s[0] == '/' {
		return s[1:]
	}
	return s
}

// Close освобождает ресурсы клиента Docker.
func (s *DockerSpawner) Close() error { return s.cli.Close() }

// networkingConfig подключает контейнер сессии к сети brigade (если она определена),
// чтобы агент достучался до API brigade по его hostname. nil — контейнер уходит в
// дефолтную bridge-сеть (host mode: агент обращается через host.docker.internal).
func (s *DockerSpawner) networkingConfig() *network.NetworkingConfig {
	if s.selfNetwork == "" {
		return nil
	}
	return &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			s.selfNetwork: {},
		},
	}
}

// netMode возвращает NetworkMode для HostConfig: имя сети brigade, если она
// определена (совпадает с networkingConfig), иначе пусто (дефолтный bridge).
func (s *DockerSpawner) netMode() container.NetworkMode {
	return container.NetworkMode(s.selfNetwork)
}

// Ping проверяет достижимость docker-демона. Используется при старте, чтобы решить,
// доступны ли docker-сессии на этом инстансе.
func (s *DockerSpawner) Ping(ctx context.Context) error {
	_, err := s.cli.Ping(ctx)
	return err
}

// homeBind добавляет bind-mount персонального home пользователя
// (spec.HomeHost → /home/agent) к hostCfg.Binds, если путь задан. Весь home общий
// между контейнерами пользователя (per-user): состояние Claude и рабочие файлы
// переживают сессии и видны во всех его контейнерах. Каталог на хосте создаётся
// заранее (см. registry.homeHost), здесь только монтируется.
func homeBind(hostCfg *container.HostConfig, spec Spec) {
	if spec.HomeHost == "" {
		return
	}
	hostCfg.Binds = append(hostCfg.Binds,
		fmt.Sprintf("%s:%s", spec.HomeHost, AgentHome))
}

// Spawn создаёт контейнер сессии, запускает его и подключается (attach) к его TTY.
//
// Контейнер помечается label brigade.session.id=<SessionID>. Персональный home
// пользователя (Spec.HomeHost) bind-mount'ится в /home/agent целиком — рабочая
// директория агента (~/workspace) и состояние Claude живут там, per-user.
func (s *DockerSpawner) Spawn(ctx context.Context, spec Spec) (Handle, error) {
	image := spec.Image
	if image == "" {
		image = defaultImage
	}

	cfg := &container.Config{
		Image:        image,
		Cmd:          []string{agentCommand},
		Env:          spec.Env,
		Hostname:     spec.Hostname,
		WorkingDir:   containerWorkdir,
		Labels:       map[string]string{labelSessionID: spec.SessionID},
		Tty:          true,
		OpenStdin:    true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	}

	// pid 1 — docker-init (tini), а не агент: он реапит осиротевшие процессы
	// (например, убитые шеллы /ws/shell и их детей), иначе в контейнере копились
	// бы зомби.
	initProcess := true
	hostCfg := &container.HostConfig{
		Init: &initProcess,
		// host.docker.internal резолвится в шлюз хоста и на Linux (host-gateway);
		// используется, когда brigade — процесс на хосте (нет своей docker-сети).
		ExtraHosts:  []string{"host.docker.internal:host-gateway"},
		NetworkMode: s.netMode(),
	}
	// Весь home пользователя монтируется per-user: рабочие файлы (~/workspace) и
	// состояние Claude (~/.claude, ~/.claude.json) переживают сессии и общие между
	// контейнерами пользователя. Отдельного bind рабочей директории нет.
	homeBind(hostCfg, spec)

	created, err := s.cli.ContainerCreate(ctx, cfg, hostCfg, s.networkingConfig(), nil, "brigade-"+spec.SessionID)
	if err != nil {
		return nil, fmt.Errorf("spawn: container create: %w", err)
	}

	// Сначала attach, затем start: иначе ранний вывод агента до подключения теряется.
	hijacked, err := s.attach(ctx, created.ID)
	if err != nil {
		return nil, err
	}

	if err := s.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		hijacked.Close()
		return nil, fmt.Errorf("spawn: container start: %w", err)
	}

	return s.newHandle(created.ID, spec.SessionID, hijacked), nil
}

// Reattach находит контейнер сессии по label и заново подключается к его TTY.
func (s *DockerSpawner) Reattach(ctx context.Context, p Persisted) (Handle, error) {
	label := p.ContainerLabel
	if label == "" {
		label = p.SessionID
	}
	if label == "" {
		return nil, errors.New("spawn: docker reattach requires container label or session id")
	}

	id, err := s.findBySessionLabel(ctx, label)
	if err != nil {
		return nil, err
	}

	hijacked, err := s.attach(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.newHandle(id, p.SessionID, hijacked), nil
}

// ContainerIP возвращает IP контейнера сессии в его сети (первый непустой адрес
// среди подключённых сетей). Используется preview-прокси: порты контейнеров не
// публикуются, upstream доступен по адресу bridge-сети с хоста docker-демона.
func (s *DockerSpawner) ContainerIP(ctx context.Context, sessionID string) (string, error) {
	id, err := s.findBySessionLabel(ctx, sessionID)
	if err != nil {
		return "", err
	}
	info, err := s.cli.ContainerInspect(ctx, id)
	if err != nil {
		return "", fmt.Errorf("spawn: container inspect: %w", err)
	}
	if info.NetworkSettings != nil {
		for _, nw := range info.NetworkSettings.Networks {
			if nw.IPAddress != "" {
				return nw.IPAddress, nil
			}
		}
	}
	return "", fmt.Errorf("spawn: container %s has no network address", id)
}

// findBySessionLabel ищет контейнер по label brigade.session.id=<label>.
func (s *DockerSpawner) findBySessionLabel(ctx context.Context, label string) (string, error) {
	args := filters.NewArgs()
	args.Add("label", labelSessionID+"="+label)

	list, err := s.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return "", fmt.Errorf("spawn: container list: %w", err)
	}
	if len(list) == 0 {
		return "", fmt.Errorf("spawn: no container with %s=%s", labelSessionID, label)
	}
	return list[0].ID, nil
}

// attach подключается к stdin/stdout/stderr контейнера в потоковом режиме.
func (s *DockerSpawner) attach(ctx context.Context, id string) (types.HijackedResponse, error) {
	hijacked, err := s.cli.ContainerAttach(ctx, id, container.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return types.HijackedResponse{}, fmt.Errorf("spawn: container attach: %w", err)
	}
	return hijacked, nil
}

func (s *DockerSpawner) newHandle(id, sessionID string, hijacked types.HijackedResponse) *dockerHandle {
	return &dockerHandle{
		cli:       s.cli,
		id:        id,
		sessionID: sessionID,
		hijacked:  hijacked,
		exitCode:  -1,
	}
}

// dockerHandle — Handle над attach-потоком контейнера.
//
// При TTY:true вывод не мультиплексируется (без stdcopy-заголовков), поэтому Reader
// отдаёт raw-байты терминала напрямую. Запись в stdin идёт в Conn соединения.
type dockerHandle struct {
	cli       *client.Client
	id        string
	sessionID string
	hijacked  types.HijackedResponse

	// hist хранит хвост вывода терминала для восстановления экрана при reconnect.
	hist history

	waitOnce sync.Once
	waitErr  error
	exitCode int
}

// Read читает вывод контейнера и попутно накапливает его хвост в hist, чтобы новое
// WebSocket-подключение могло восстановить содержимое терминала.
func (h *dockerHandle) Read(p []byte) (int, error) {
	n, err := h.hijacked.Reader.Read(p)
	if n > 0 {
		h.hist.append(p[:n])
	}
	return n, err
}

func (h *dockerHandle) Write(p []byte) (int, error) { return h.hijacked.Conn.Write(p) }

// History возвращает копию накопленного хвоста вывода терминала.
func (h *dockerHandle) History() []byte { return h.hist.snapshot() }

// Close закрывает attach-соединение. Контейнер при этом продолжает работать —
// это нужно для resume через повторный attach; остановка контейнера выполняется
// отдельно (Terminate при Stop/Delete сессии).
func (h *dockerHandle) Close() error {
	h.hijacked.Close()
	return nil
}

// Terminate останавливает и удаляет контейнер сессии. Сначала закрывается attach-поток,
// затем контейнер останавливается штатно (ContainerStop с таймаутом — docker сам
// эскалирует до SIGKILL) и удаляется. Без этого Stop/Delete оставляли бы контейнер
// работать вечно (утечка). Идемпотентна: отсутствующий/уже остановленный контейнер не
// считается ошибкой.
func (h *dockerHandle) Terminate(ctx context.Context) error {
	h.hijacked.Close()

	stopTimeout := dockerStopTimeoutSeconds
	if err := h.cli.ContainerStop(ctx, h.id, container.StopOptions{Timeout: &stopTimeout}); err != nil {
		// Контейнер мог уже не существовать/быть остановленным — это не ошибка teardown.
		log.Printf("spawn: container stop %s: %v", h.id, err)
	}
	if err := h.cli.ContainerRemove(ctx, h.id, container.RemoveOptions{Force: true}); err != nil {
		log.Printf("spawn: container remove %s: %v", h.id, err)
	}
	return nil
}

// Resize меняет размер TTY контейнера.
func (h *dockerHandle) Resize(cols, rows uint16) error {
	return h.cli.ContainerResize(context.Background(), h.id, container.ResizeOptions{
		Height: uint(rows),
		Width:  uint(cols),
	})
}

// Wait дожидается завершения контейнера и фиксирует код выхода. Идемпотентна.
func (h *dockerHandle) Wait() error {
	h.waitOnce.Do(func() {
		ctx := context.Background()
		statusCh, errCh := h.cli.ContainerWait(ctx, h.id, container.WaitConditionNotRunning)
		select {
		case err := <-errCh:
			h.waitErr = err
			h.exitCode = -1
		case st := <-statusCh:
			h.exitCode = int(st.StatusCode)
			if st.Error != nil {
				h.waitErr = errors.New(st.Error.Message)
			}
		}
	})
	return h.waitErr
}

func (h *dockerHandle) ExitCode() int          { return h.exitCode }
func (h *dockerHandle) AgentSessionID() string { return "" }

// ContainerLabel возвращает значение label brigade.session.id контейнера сессии.
func (h *dockerHandle) ContainerLabel() string { return h.sessionID }

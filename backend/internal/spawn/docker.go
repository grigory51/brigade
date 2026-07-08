package spawn

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// labelSessionID — ключ label, помечающий контейнер сессии brigade. По нему
// контейнер отыскивается при Reattach после рестарта бэкенда.
const labelSessionID = "brigade.session.id"

// labelUserID/labelKind — метки общего per-user контейнера CLI-сессий (shared-схема):
// один долгоживущий контейнер brigade-user-<userID> на пользователя, сессии — exec'и
// внутри него. Label brigade.session.id на такой контейнер намеренно НЕ вешается: он
// остаётся контрактом legacy-CLI и ACP (Reattach, preview, /ws/shell по сессии).
const (
	labelUserID       = "brigade.user.id"
	labelKind         = "brigade.kind"
	labelKindCLIShare = "cli-shared"
)

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

// ContainerWorkdir — базовая рабочая директория агентов в docker-режиме: подпапка home
// (переживает контейнеры, не расшарена между пользователями, т.к. home per-user).
// Фактический cwd сессии — её per-session подкаталог ~/workspace/<id> (см. specWorkdir).
const ContainerWorkdir = AgentHome + "/workspace"
const containerWorkdir = ContainerWorkdir

// specWorkdir возвращает рабочую директорию контейнера/exec'а для сессии: per-session
// cwd из Spec (~/workspace/<id>), либо базовый workspace, если cwd не задан (старые
// сессии до перехода на per-session, эфемерный fallback).
func specWorkdir(spec Spec) string {
	if spec.Cwd != "" {
		return spec.Cwd
	}
	return containerWorkdir
}

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

// Spawn запускает CLI-сессию в docker.
//
// Непустой Spec.UserID включает shared-схему: сессия — это docker exec с TTY в общем
// per-user контейнере (см. ensureUserContainer). Пустой — legacy-схема: отдельный
// контейнер на сессию с label brigade.session.id=<SessionID> (сохранена для
// восстановления старых сессий; новые так не создаются).
// Spawn/Reattach для docker больше не используются: CLI-сессии docker поднимает реестр через
// per-user агент-демон (registry.spawnCLIDaemon → EnsureUserDaemon + cliremote), ACP —
// registry.spawnACPDaemon. Методы оставлены для удовлетворения интерфейса Spawner (его
// использует и local-спавнер); в docker-режиме не вызываются.
func (s *DockerSpawner) Spawn(context.Context, Spec) (Handle, error) {
	return nil, errors.New("spawn: docker-сессии поднимаются реестром через демон, не Spawn")
}

// Reattach восстанавливает CLI-сессию после рестарта бэкенда.
//
// Legacy-схема (непустой ContainerLabel): контейнер сессии ищется по label
// brigade.session.id, attach к его главному TTY — тому же процессу claude.
//
// Shared-схема (пустой ContainerLabel): re-attach к запущенному exec невозможен по
// Docker API, поэтому семантика — «перезапуск с resume»: общий контейнер поднимается
// при необходимости (hostname/home сохраняют авторизацию Claude), осиротевший процесс
// прежнего exec'а убивается по маркеру, затем свежий exec `claude --resume
// <AgentSessionID>` продолжает тот же диалог.
func (s *DockerSpawner) Reattach(context.Context, Persisted) (Handle, error) {
	return nil, errors.New("spawn: docker-сессии восстанавливаются реестром через демон, не Reattach")
}

// ensureUserContainer возвращает работающий общий контейнер пользователя, создавая
// или запуская его при необходимости. Контейнер долгоживущий: PID 1 — docker-init,
// главный процесс — idle-якорь (sleep infinity), сессии живут exec'ами. Hostname и
// bind home стабильны per-user — авторизация Claude переживает и сессии, и
// пересоздание контейнера настолько, насколько её fingerprint опирается на них.
func (s *DockerSpawner) ensureUserContainer(ctx context.Context, userID, image, homeHost, hostname, pubKey string) (string, error) {
	if id, state, err := s.findUserContainer(ctx, userID); err != nil {
		return "", err
	} else if id != "" {
		if err := s.ensureRunning(ctx, id, state); err != nil {
			return "", err
		}
		return id, nil
	}

	if image == "" {
		image = defaultImage
	}
	initProcess := true
	daemonNatPort := nat.Port(fmt.Sprintf("%d/tcp", daemonPort))
	cfg := &container.Config{
		Image: image,
		// pid1 — per-user агент-демон brigade: CLI-сессии и вспом. шеллы он спавнит сам в pty
		// (Terminal RPC), brigade ходит по Connect, а не docker-exec'ом. Контейнер общий на
		// пользователя — логин claude (привязан к контейнеру) переживает несколько сессий.
		Cmd:      []string{brigadeBinPath, "acp-agent"},
		Hostname: hostname,
		// Идентификатор демона (aud подписи) = userID: per-user демон обслуживает все CLI-сессии
		// пользователя. Секретов в env нет — только публичный ключ.
		Env: []string{
			"BRIGADE_SESSION_ID=" + userID,
			fmt.Sprintf("BRIGADE_DAEMON_PORT=%d", daemonPort),
			"BRIGADE_DAEMON_PUBKEY=" + pubKey,
			"BRIGADE_DAEMON_LOG=" + AgentHome + daemonLogDir + "/user-" + userID + "/events.jsonl",
		},
		Labels: map[string]string{
			labelUserID: userID,
			labelKind:   labelKindCLIShare,
		},
		ExposedPorts: nat.PortSet{daemonNatPort: struct{}{}},
	}
	hostCfg := &container.HostConfig{
		Init:        &initProcess,
		ExtraHosts:  []string{"host.docker.internal:host-gateway"},
		NetworkMode: s.netMode(),
		// Публикуем порт демона на 127.0.0.1:<эфемерный> для host-режима brigade (см. daemonAddrByID).
		PortBindings: nat.PortMap{daemonNatPort: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: ""}}},
	}
	homeBind(hostCfg, Spec{HomeHost: homeHost})

	created, err := s.cli.ContainerCreate(ctx, cfg, hostCfg, s.networkingConfig(), nil, "brigade-user-"+userID)
	if err != nil {
		// Гонка одновременного создания двух сессий: проигравший конфликт имени
		// переиспользует контейнер победителя.
		if id, state, ferr := s.findUserContainer(ctx, userID); ferr == nil && id != "" {
			if serr := s.ensureRunning(ctx, id, state); serr != nil {
				return "", fmt.Errorf("spawn: user container after race: %w", serr)
			}
			return id, nil
		}
		return "", fmt.Errorf("spawn: user container create: %w", err)
	}
	if err := s.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("spawn: user container start: %w", err)
	}
	return created.ID, nil
}

// EnsureUserDaemon поднимает per-user контейнер с агентом-демоном (или переиспользует живой) и
// возвращает адрес его Connect-сервера, дождавшись готовности. Общий контейнер на пользователя
// сохраняет логин claude; CLI-сессии — durable-терминалы демона. pubKey — публичный ключ
// brigade (asymmetric-auth). Вызывающий держит per-user лок (как прежняя shared-схема).
func (s *DockerSpawner) EnsureUserDaemon(ctx context.Context, spec Spec, pubKey string) (string, error) {
	id, err := s.ensureUserContainer(ctx, spec.UserID, spec.Image, spec.HomeHost, spec.Hostname, pubKey)
	if err != nil {
		return "", err
	}
	addr, err := s.daemonAddrByID(ctx, id)
	if err != nil {
		return "", err
	}
	if err := waitDaemonReady(ctx, addr); err != nil {
		return "", err
	}
	return addr, nil
}

// UserDaemonAddr возвращает адрес per-user демона для reconnect после рестарта brigade.
// ok=false — контейнера нет или он не поднимается.
func (s *DockerSpawner) UserDaemonAddr(ctx context.Context, userID string) (string, bool) {
	id, state, err := s.findUserContainer(ctx, userID)
	if err != nil || id == "" {
		return "", false
	}
	if err := s.ensureRunning(ctx, id, state); err != nil {
		return "", false
	}
	addr, err := s.daemonAddrByID(ctx, id)
	if err != nil {
		return "", false
	}
	return addr, true
}

// findUserContainer ищет общий контейнер пользователя по label brigade.user.id.
// Возвращает пустой id, если контейнера нет; state — docker-состояние ("running",
// "exited", "paused", …).
func (s *DockerSpawner) findUserContainer(ctx context.Context, userID string) (id, state string, err error) {
	args := filters.NewArgs()
	args.Add("label", labelUserID+"="+userID)
	args.Add("label", labelKind+"="+labelKindCLIShare)
	list, err := s.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return "", "", fmt.Errorf("spawn: user container list: %w", err)
	}
	if len(list) == 0 {
		return "", "", nil
	}
	return list[0].ID, list[0].State, nil
}

// ensureRunning приводит контейнер в состояние running по его текущему state: paused —
// unpause (ContainerStart для paused падает), running — no-op, прочее (exited/created) —
// start.
func (s *DockerSpawner) ensureRunning(ctx context.Context, id, state string) error {
	switch state {
	case "running":
		return nil
	case "paused":
		if err := s.cli.ContainerUnpause(ctx, id); err != nil {
			return fmt.Errorf("spawn: user container unpause: %w", err)
		}
		return nil
	default:
		if err := s.cli.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
			return fmt.Errorf("spawn: user container start: %w", err)
		}
		return nil
	}
}

// RemoveUserContainer останавливает и удаляет общий контейнер пользователя.
// Вызывается реестром, когда закрыта последняя CLI-сессия пользователя. Идемпотентна:
// отсутствие контейнера — не ошибка.
func (s *DockerSpawner) RemoveUserContainer(ctx context.Context, userID string) error {
	id, _, err := s.findUserContainer(ctx, userID)
	if err != nil || id == "" {
		return err
	}
	stopTimeout := dockerStopTimeoutSeconds
	if err := s.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &stopTimeout}); err != nil {
		log.Printf("spawn: user container stop %s: %v", id, err)
	}
	if err := s.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil {
		log.Printf("spawn: user container remove %s: %v", id, err)
	}
	return nil
}

// ContainerIP возвращает IP контейнера сессии в его сети (первый непустой адрес
// среди подключённых сетей). Используется preview-прокси: порты контейнеров не
// публикуются, upstream доступен по адресу bridge-сети с хоста docker-демона.
// Сначала ищется контейнер сессии (legacy CLI и ACP, label brigade.session.id);
// не найден — общий контейнер пользователя (shared CLI).
func (s *DockerSpawner) ContainerIP(ctx context.Context, sessionID, userID string) (string, error) {
	id, err := s.findSessionOrUserContainer(ctx, sessionID, userID)
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

// findSessionOrUserContainer находит контейнер, обслуживающий сессию: сперва по
// label сессии (legacy CLI, ACP), затем — общий контейнер пользователя (shared CLI).
func (s *DockerSpawner) findSessionOrUserContainer(ctx context.Context, sessionID, userID string) (string, error) {
	if id, err := s.findBySessionLabel(ctx, sessionID); err == nil {
		return id, nil
	}
	if userID != "" {
		if id, _, err := s.findUserContainer(ctx, userID); err == nil && id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf("spawn: no container for session %s", sessionID)
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

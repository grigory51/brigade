package spawn

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"

	"github.com/grigory51/brigade/backend/internal/agent"
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

// DefaultImage — образ контейнера агента по умолчанию: собирается локально из
// packaging/docker/agent/Dockerfile. Серверная инсталляция обычно переопределяет его
// опубликованным образом (config: agent_image), десктопная — тянет его же из ghcr.
const DefaultImage = "brigade/agent:latest"

// AgentUID/AgentGID — uid/gid пользователя agent в образе (зафиксированы в
// packaging/docker/agent/Dockerfile). brigade chown'ит персональный ~/.claude на них,
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
	// agentNetwork — отдельная bridge-сеть только для brigade и контейнеров агентов.
	// Они не подключаются к compose-сети сервиса и не занимают её статические адреса.
	agentNetwork string
	// selfNetwork непуст, когда brigade работает в контейнере и подключён к agentNetwork.
	// В host-режиме агент ходит через host.docker.internal / host-gateway.
	selfNetwork string
	// selfHost — hostname, по которому агент обращается к API brigade: имя контейнера
	// brigade в изолированной сети (self-container mode) либо "host.docker.internal" (host mode).
	selfHost string
	// baseImage — образ агента по умолчанию и донор runtime-слоёв (см. runtime.go).
	baseImage string
	// runtimeState — кеш подготовленных runtime-volume'ов (см. runtime.go).
	runtimeState
}

// BaseImage — образ агента по умолчанию этого инстанса.
func (s *DockerSpawner) BaseImage() string { return s.baseImage }

// SelfStartedAt возвращает время старта контейнера brigade. В host-режиме inspect
// недоступен, поэтому вызывающий код трактует ошибку как отсутствие значения.
func (s *DockerSpawner) SelfStartedAt(ctx context.Context) (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", err
	}
	info, err := s.cli.ContainerInspect(ctx, hostname)
	if err != nil {
		return "", err
	}
	if info.State == nil {
		return "", errors.New("spawn: self container has no state")
	}
	return info.State.StartedAt, nil
}

// NewDockerSpawner создаёт DockerSpawner с клиентом Docker из окружения
// (DOCKER_HOST и т. п.) и готовит отдельную сеть контейнеров агентов. Клиент следует
// закрыть через Close.
func NewDockerSpawner(baseImage string) (*DockerSpawner, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("spawn: docker client: %w", err)
	}
	if baseImage == "" {
		baseImage = DefaultImage
	}
	s := &DockerSpawner{cli: cli, baseImage: baseImage, selfHost: "host.docker.internal"}
	if err := s.configureAgentNetwork(context.Background()); err != nil {
		_ = cli.Close()
		return nil, err
	}
	return s, nil
}

// configureAgentNetwork создаёт отдельную сеть агентов. Если brigade сам работает в
// контейнере, он подключается к ней, оставаясь также в сервисной сети для входящего API.
func (s *DockerSpawner) configureAgentNetwork(ctx context.Context) error {
	name := "brigade-agents"
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("spawn: hostname: %w", err)
	}
	info, err := s.cli.ContainerInspect(ctx, hostname)
	containerized := err == nil
	if containerized {
		s.selfHost = trimLeadingSlash(info.Name)
		name = s.selfHost + "-agents"
	} else {
		log.Printf("spawn: not running in a container (%v) — agents reach API via host.docker.internal", err)
	}

	if _, err := s.cli.NetworkInspect(ctx, name, network.InspectOptions{}); client.IsErrNotFound(err) {
		if _, err := s.cli.NetworkCreate(ctx, name, network.CreateOptions{
			Driver: "bridge",
			Labels: map[string]string{labelKind: "agents"},
		}); err != nil {
			return fmt.Errorf("spawn: create agent network %q: %w", name, err)
		}
	} else if err != nil {
		return fmt.Errorf("spawn: inspect agent network %q: %w", name, err)
	}
	s.agentNetwork = name

	if containerized {
		if info.NetworkSettings == nil || info.NetworkSettings.Networks[name] == nil {
			if err := s.cli.NetworkConnect(ctx, name, info.ID, &network.EndpointSettings{Aliases: []string{s.selfHost}}); err != nil {
				return fmt.Errorf("spawn: connect brigade to agent network %q: %w", name, err)
			}
		}
		s.selfNetwork = name
		log.Printf("spawn: brigade in container %q — agents use isolated network %q, API host=%q",
			s.selfHost, s.agentNetwork, s.selfHost)
	} else {
		log.Printf("spawn: agents use isolated network %q", s.agentNetwork)
	}
	s.migrateAgentContainers(ctx)
	return nil
}

// migrateAgentContainers освобождает адреса прежней сервисной сети после обновления.
// Ошибка одного старого контейнера не должна препятствовать восстановлению остальных.
func (s *DockerSpawner) migrateAgentContainers(ctx context.Context) {
	containers, err := s.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		log.Printf("spawn: list agent containers for network migration: %v", err)
		return
	}
	for _, c := range containers {
		_, session := c.Labels[labelSessionID]
		if !session && c.Labels[labelKind] != labelKindCLIShare {
			continue
		}
		if err := s.isolateContainerNetwork(ctx, c.ID); err != nil {
			log.Printf("spawn: isolate agent container %s: %v", c.ID, err)
		}
	}
}

func (s *DockerSpawner) isolateContainerNetwork(ctx context.Context, id string) error {
	info, err := s.cli.ContainerInspect(ctx, id)
	if err != nil {
		return err
	}
	if info.NetworkSettings == nil {
		return errors.New("container has no network settings")
	}
	networks := info.NetworkSettings.Networks
	if networks[s.agentNetwork] == nil {
		if err := s.cli.NetworkConnect(ctx, s.agentNetwork, id, nil); err != nil {
			return fmt.Errorf("connect to %s: %w", s.agentNetwork, err)
		}
	}
	for name := range networks {
		if name != s.agentNetwork {
			if err := s.cli.NetworkDisconnect(ctx, name, id, true); err != nil {
				return fmt.Errorf("disconnect from %s: %w", name, err)
			}
		}
	}
	return nil
}

// APIHost возвращает hostname, по которому агент в контейнере обращается к API
// brigade: имя контейнера brigade (если он сам в agent network) либо
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

// networkingConfig подключает контейнер только к изолированной сети агентов.
func (s *DockerSpawner) networkingConfig() *network.NetworkingConfig {
	if s.agentNetwork == "" {
		return nil
	}
	return &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			s.agentNetwork: {},
		},
	}
}

// netMode возвращает NetworkMode изолированной сети агентов.
func (s *DockerSpawner) netMode() container.NetworkMode {
	return container.NetworkMode(s.agentNetwork)
}

// Ping проверяет достижимость docker-демона. Используется при старте, чтобы решить,
// доступны ли docker-сессии на этом инстансе.
func (s *DockerSpawner) Ping(ctx context.Context) error {
	_, err := s.cli.Ping(ctx)
	return err
}

func hardenAgentContainer(hostCfg *container.HostConfig) {
	hostCfg.CapDrop = strslice.StrSlice{"ALL"}
	hostCfg.SecurityOpt = append(hostCfg.SecurityOpt, "no-new-privileges=true")
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
func (s *DockerSpawner) ensureUserContainer(ctx context.Context, userID, image, homeHost, hostname, pubKey string, layers []agent.Layer) (string, error) {
	if image == "" {
		image = s.baseImage
	}
	// Runtime-mount'ы считаются до reuse: долгоживущий per-user контейнер мог быть создан
	// прошлой версией brigade без нового агента или со старым runtime-volume.
	runtimeMounts, runtimePath, err := s.runtimeMounts(ctx, layers)
	if err != nil {
		return "", err
	}
	if id, state, err := s.findUserContainer(ctx, userID); err != nil {
		return "", err
	} else if id != "" {
		info, err := s.cli.ContainerInspect(ctx, id)
		if err != nil {
			return "", fmt.Errorf("spawn: user container inspect: %w", err)
		}
		if runtimeMountsCurrent(info.Mounts, runtimeMounts) {
			if err := s.ensureRunning(ctx, id, state); err != nil {
				return "", err
			}
			return id, nil
		}
		log.Printf("spawn: user container %s has stale runtime mounts, recreating", id)
		if err := s.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil {
			return "", fmt.Errorf("spawn: remove stale user container: %w", err)
		}
	}

	// Компоненты brigade приезжают read-only volume'ами, а не берутся из образа (см.
	// runtime.go): контейнер может быть построен на образе пользователя.
	initProcess := true
	daemonNatPort := nat.Port(fmt.Sprintf("%d/tcp", daemonPort))
	cfg := &container.Config{
		Image: image,
		// pid1 — per-user агент-демон brigade: CLI-сессии и вспом. шеллы он спавнит сам в pty
		// (Terminal RPC), brigade ходит по Connect, а не docker-exec'ом. Контейнер общий на
		// пользователя — логин claude (привязан к контейнеру) переживает несколько сессий.
		Cmd:      []string{daemonBinPath(), "acp-agent"},
		User:     agentUser,
		Hostname: hostname,
		// Идентификатор демона (aud подписи) = userID: per-user демон обслуживает все CLI-сессии
		// пользователя. Секретов в env нет — только публичный ключ.
		Env: sanitizedEnv([]string{
			"BRIGADE_SESSION_ID=" + userID,
			fmt.Sprintf("BRIGADE_DAEMON_PORT=%d", daemonPort),
			"BRIGADE_DAEMON_PUBKEY=" + pubKey,
			"BRIGADE_DAEMON_LOG=" + AgentHome + daemonLogDir + "/user-" + userID + "/events.jsonl",
			"HOME=" + AgentHome,
			"PATH=" + runtimePath + ":" + basePath,
		}),
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
		Mounts:      runtimeMounts,
		// Публикуем порт демона на 127.0.0.1:<эфемерный> для host-режима brigade (см. daemonAddrByID).
		PortBindings: nat.PortMap{daemonNatPort: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: ""}}},
	}
	hardenAgentContainer(hostCfg)
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

func runtimeMountsCurrent(current []container.MountPoint, expected []mount.Mount) bool {
	for _, want := range expected {
		found := false
		for _, got := range current {
			if got.Type == want.Type && got.Name == want.Source && got.Destination == want.Target {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// EnsureUserDaemon поднимает per-user контейнер с агентом-демоном (или переиспользует живой) и
// возвращает адрес его Connect-сервера, дождавшись готовности. Общий контейнер на пользователя
// сохраняет логин claude; CLI-сессии — durable-терминалы демона. pubKey — публичный ключ
// brigade (asymmetric-auth). Вызывающий держит per-user лок (как прежняя shared-схема).
func (s *DockerSpawner) EnsureUserDaemon(ctx context.Context, spec Spec, pubKey string) (string, error) {
	id, err := s.ensureUserContainer(ctx, spec.UserID, spec.Image, spec.HomeHost, spec.Hostname, pubKey, spec.Layers)
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

// ContainerIP возвращает IP контейнера сессии в изолированной сети агентов.
// Используется preview-прокси: порты контейнеров не публикуются, upstream доступен
// по адресу bridge-сети с хоста docker-демона.
// Сначала ищется контейнер сессии (legacy CLI и ACP, label brigade.session.id);
// не найден — общий контейнер пользователя (shared CLI).
func (s *DockerSpawner) ContainerIP(ctx context.Context, sessionID, userID string) (string, error) {
	id, err := s.findSessionOrUserContainer(ctx, sessionID, userID)
	if err != nil {
		return "", err
	}
	if err := s.isolateContainerNetwork(ctx, id); err != nil {
		return "", fmt.Errorf("spawn: isolate container network: %w", err)
	}
	info, err := s.cli.ContainerInspect(ctx, id)
	if err != nil {
		return "", fmt.Errorf("spawn: container inspect: %w", err)
	}
	if info.NetworkSettings != nil {
		if nw := info.NetworkSettings.Networks[s.agentNetwork]; nw != nil && nw.IPAddress != "" {
			return nw.IPAddress, nil
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

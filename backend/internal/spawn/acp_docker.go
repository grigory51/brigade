package spawn

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/go-connections/nat"
)

const (
	// daemonPort — фиксированный порт Connect-сервера демона внутри контейнера сессии.
	daemonPort = 8787
	// brigadeBinPath — путь бинаря brigade в образе агента (COPY в Dockerfile); демон —
	// это `brigade acp-agent`.
	brigadeBinPath = "/usr/local/bin/brigade"
	// daemonLogDir — базовый каталог durable-журналов демона в home (относительно AgentHome).
	// Журнал кладётся в per-session подкаталог: home может быть per-user bind-mount
	// (<claudeHomeDir>/<userID>, общий для сессий юзера), и без session id журналы
	// одновременных docker-ACP-сессий одного юзера легли бы в один файл.
	daemonLogDir = "/.brigade"
)

// ACPVolumeName возвращает имя named volume home агента для сессии (fallback, когда
// персональный home пользователя не задан — claude_home_dir пуст).
func ACPVolumeName(sessionID string) string { return "brigade-claude-" + sessionID }

// DockerACPSpawner порождает контейнерные процессы ACP-adapter'а (docker-режим).
// Реализует acp.ProcSpawner через SpawnProc.
type DockerACPSpawner struct {
	spawner *DockerSpawner
}

// ACP создаёт фабрику контейнерных ACP-процессов поверх docker-клиента спавнера.
func (s *DockerSpawner) ACP() *DockerACPSpawner { return &DockerACPSpawner{spawner: s} }

// StartDaemon создаёт контейнер сессии с durable ACP-демоном (`brigade acp-agent`, pid1) и
// возвращает адрес его Connect-сервера для дозвона. Секретов в env контейнера НЕТ (они
// придут в Configure и лягут только в env дочернего адаптера — `/ws/shell` их не видит):
// в env только несекретная конфигурация демона. Контейнер БЕЗ AutoRemove — переживает
// рестарт brigade; удаляется явно при teardown. pubKey — публичный Ed25519-ключ brigade
// (asymmetric-auth: демон проверяет им подпись вызовов; утечка env импрсонацию не даёт).
func (d *DockerACPSpawner) StartDaemon(ctx context.Context, spec Spec, stateID, pubKey string) (string, error) {
	cli := d.spawner.cli
	image := spec.Image
	if image == "" {
		image = defaultImage
	}

	var homeMount mount.Mount
	if spec.HomeHost != "" {
		homeMount = mount.Mount{Type: mount.TypeBind, Source: spec.HomeHost, Target: AgentHome}
	} else {
		volName := ACPVolumeName(stateID)
		if _, err := cli.VolumeCreate(ctx, volume.CreateOptions{Name: volName}); err != nil {
			return "", fmt.Errorf("spawn: acp volume create: %w", err)
		}
		homeMount = mount.Mount{Type: mount.TypeVolume, Source: volName, Target: AgentHome}
	}

	daemonNatPort := nat.Port(fmt.Sprintf("%d/tcp", daemonPort))
	cfg := &container.Config{
		Image: image,
		// pid1 контейнера — демон brigade; адаптер он спавнит сам (Configure).
		Cmd: []string{brigadeBinPath, "acp-agent"},
		// Только несекретная конфигурация демона. Секреты (OAuth, preview-env) — в Configure.
		Env: []string{
			"BRIGADE_SESSION_ID=" + spec.SessionID,
			fmt.Sprintf("BRIGADE_DAEMON_PORT=%d", daemonPort),
			"BRIGADE_DAEMON_PUBKEY=" + pubKey,
			"BRIGADE_DAEMON_LOG=" + AgentHome + daemonLogDir + "/" + spec.SessionID + "/acp-events.jsonl",
		},
		Hostname:     spec.Hostname,
		WorkingDir:   specWorkdir(spec),
		Labels:       map[string]string{labelSessionID: spec.SessionID},
		ExposedPorts: nat.PortSet{daemonNatPort: struct{}{}},
	}
	initProcess := true
	hostCfg := &container.HostConfig{
		AutoRemove:  false, // durable: контейнер переживает brigade, удаляется явным teardown
		Init:        &initProcess,
		ExtraHosts:  []string{"host.docker.internal:host-gateway"},
		NetworkMode: d.spawner.netMode(),
		Mounts:      []mount.Mount{homeMount},
		// Публикуем порт демона на 127.0.0.1:<эфемерный> — для host-режима brigade (процесс на
		// хосте не достаёт bridge-IP контейнера). В container-режиме (brigade на общей сети)
		// используется прямой IP:port (см. daemonAddr).
		PortBindings: nat.PortMap{daemonNatPort: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: ""}}},
	}

	name := "brigade-acp-" + spec.SessionID
	if err := cli.ContainerRemove(ctx, name, container.RemoveOptions{Force: true}); err != nil && !strings.Contains(err.Error(), "No such container") {
		return "", fmt.Errorf("spawn: acp remove stale container: %w", err)
	}
	created, err := cli.ContainerCreate(ctx, cfg, hostCfg, d.spawner.networkingConfig(), nil, name)
	if err != nil {
		return "", fmt.Errorf("spawn: acp container create: %w", err)
	}
	if err := cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		_ = cli.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("spawn: acp container start: %w", err)
	}

	addr, err := d.daemonAddr(ctx, spec.SessionID)
	if err != nil {
		return "", err
	}
	// Демон (Go-бинарь) поднимает листенер не мгновенно после старта контейнера — ждём
	// готовности порта, иначе первый Configure упрётся в connection refused.
	if err := waitDaemonReady(ctx, addr); err != nil {
		return "", err
	}
	return addr, nil
}

// waitDaemonReady опрашивает HTTP-эндпоинт демона, пока тот не начнёт отвечать (любой
// HTTP-статус), или до дедлайна ~15s / отмены ctx. Проба именно на уровне HTTP, а не TCP:
// опубликованный порт принимает соединение через docker-proxy ещё до того, как процесс демона
// внутри контейнера поднимет листенер, — сырой dial прошёл бы преждевременно, и первый
// Configure упёрся бы в reset (unexpected EOF).
func waitDaemonReady(ctx context.Context, addr string) error {
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, addr, nil)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			return nil // демон ответил по HTTP — листенер поднят
		}
		lastErr = err
		if time.Now().After(deadline) {
			return fmt.Errorf("spawn: daemon %s not ready: %w", addr, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// DaemonAddr возвращает адрес демона живого контейнера сессии (для reconnect после рестарта
// brigade). ok=false, если контейнера нет или он не запущен (нужен respawn через StartDaemon).
func (d *DockerACPSpawner) DaemonAddr(ctx context.Context, sessionID string) (string, bool) {
	addr, err := d.daemonAddr(ctx, sessionID)
	if err != nil {
		return "", false
	}
	return addr, true
}

// daemonAddr резолвит http-адрес демона сессии в зависимости от режима brigade:
//   - host-режим (brigade — процесс на хосте, selfNetwork пуст): опубликованный порт на
//     127.0.0.1 (bridge-IP контейнера с хоста не роутится);
//   - container-режим (brigade в контейнере на общей сети): прямой IP контейнера:daemonPort.
func (d *DockerACPSpawner) daemonAddr(ctx context.Context, sessionID string) (string, error) {
	id, err := d.spawner.findBySessionLabel(ctx, sessionID)
	if err != nil {
		return "", err
	}
	return d.spawner.daemonAddrByID(ctx, id)
}

// daemonAddrByID резолвит http-адрес демона контейнера по его id в зависимости от режима
// brigade (общий резолвер для per-session ACP и per-user CLI демонов):
//   - host-режим (selfNetwork пуст): опубликованный порт на 127.0.0.1 (bridge-IP контейнера
//     с хоста не роутится);
//   - container-режим (brigade на общей сети): прямой IP контейнера:daemonPort.
func (s *DockerSpawner) daemonAddrByID(ctx context.Context, id string) (string, error) {
	info, err := s.cli.ContainerInspect(ctx, id)
	if err != nil {
		return "", fmt.Errorf("spawn: container inspect: %w", err)
	}
	if s.selfNetwork == "" {
		if info.NetworkSettings != nil {
			if binds := info.NetworkSettings.Ports[nat.Port(fmt.Sprintf("%d/tcp", daemonPort))]; len(binds) > 0 && binds[0].HostPort != "" {
				return fmt.Sprintf("http://127.0.0.1:%s", binds[0].HostPort), nil
			}
		}
		return "", fmt.Errorf("spawn: daemon port for container %s not published", id)
	}
	if info.NetworkSettings != nil {
		for _, nw := range info.NetworkSettings.Networks {
			if nw.IPAddress != "" {
				return fmt.Sprintf("http://%s:%d", nw.IPAddress, daemonPort), nil
			}
		}
	}
	return "", fmt.Errorf("spawn: container %s has no network address", id)
}

// RemoveContainer удаляет контейнер сессии (явный teardown durable-демона).
func (d *DockerACPSpawner) RemoveContainer(ctx context.Context, sessionID string) error {
	id, err := d.spawner.findBySessionLabel(ctx, sessionID)
	if err != nil {
		return nil // контейнера нет — нечего удалять
	}
	return d.spawner.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
}

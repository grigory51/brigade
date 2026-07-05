package spawn

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/grigory51/brigade/backend/internal/acp"
)

// acpAdapterCommand — команда ACP-adapter'а внутри контейнера агента.
const acpAdapterCommand = "claude-agent-acp"

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

// SpawnProc возвращает acp.ProcSpawner для сессии: контейнер с claude-agent-acp на
// stdio (без TTY), рабочая директория сессии — bind-mount, состояние агента — named
// volume по stateID. Контейнер удаляется по завершении (AutoRemove); volume переживает
// его. stateID — идентификатор корневой сессии дерева: ветки (Fork) монтируют volume
// родителя, иначе форкнутый агент не увидел бы исходную сессию.
func (d *DockerACPSpawner) SpawnProc(spec Spec, stateID string) acp.ProcSpawner {
	return func(ctx context.Context) (acp.AgentProc, error) {
		return d.start(ctx, spec, stateID)
	}
}

func (d *DockerACPSpawner) start(ctx context.Context, spec Spec, stateID string) (acp.AgentProc, error) {
	cli := d.spawner.cli

	image := spec.Image
	if image == "" {
		image = defaultImage
	}

	// Home агента (/home/agent) целиком:
	//  - если задан персональный home пользователя (spec.HomeHost) — bind-mount с
	//    хоста, общий для всех его сессий (CLI и ACP): авторизация Claude и рабочие
	//    файлы (~/workspace) переживают сессии и видны везде;
	//  - иначе fallback на named volume по дереву сессий (stateID).
	var homeMount mount.Mount
	if spec.HomeHost != "" {
		homeMount = mount.Mount{Type: mount.TypeBind, Source: spec.HomeHost, Target: AgentHome}
	} else {
		volName := ACPVolumeName(stateID)
		if _, err := cli.VolumeCreate(ctx, volume.CreateOptions{Name: volName}); err != nil {
			return nil, fmt.Errorf("spawn: acp volume create: %w", err)
		}
		homeMount = mount.Mount{Type: mount.TypeVolume, Source: volName, Target: AgentHome}
	}

	cfg := &container.Config{
		Image:    image,
		Cmd:      []string{acpAdapterCommand},
		Env:      spec.Env,
		Hostname: spec.Hostname,
		// Adapter говорит JSON-RPC по stdio: TTY недопустим (исказил бы поток),
		// stdin держится открытым на всё время жизни.
		Tty:          false,
		OpenStdin:    true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		WorkingDir:   specWorkdir(spec),
		Labels:       map[string]string{labelSessionID: spec.SessionID},
	}
	initProcess := true
	hostCfg := &container.HostConfig{
		// Контейнер одноразов: состояние в volume, teardown не оставляет мусора.
		AutoRemove: true,
		// pid 1 — docker-init (tini), а не adapter: он реапит осиротевшие процессы
		// (например, убитые шеллы /ws/shell и их детей), иначе в контейнере копились
		// бы зомби.
		Init: &initProcess,
		// host.docker.internal резолвится в шлюз хоста и на Linux (host-gateway);
		// используется, когда brigade — процесс на хосте (нет своей docker-сети).
		ExtraHosts:  []string{"host.docker.internal:host-gateway"},
		NetworkMode: d.spawner.netMode(),
		Mounts:      []mount.Mount{homeMount},
	}

	// Осиротевший контейнер сессии (нечистая смерть brigade — Close не отработал)
	// держит имя и блокирует пересоздание. Убираем его: состояние живёт в volume,
	// контейнер одноразов по контракту.
	name := "brigade-acp-" + spec.SessionID
	if err := cli.ContainerRemove(ctx, name, container.RemoveOptions{Force: true}); err != nil && !strings.Contains(err.Error(), "No such container") {
		return nil, fmt.Errorf("spawn: acp remove stale container: %w", err)
	}

	created, err := cli.ContainerCreate(ctx, cfg, hostCfg, d.spawner.networkingConfig(), nil, name)
	if err != nil {
		return nil, fmt.Errorf("spawn: acp container create: %w", err)
	}

	hijacked, err := cli.ContainerAttach(ctx, created.ID, container.AttachOptions{
		Stream: true, Stdin: true, Stdout: true, Stderr: true,
	})
	if err != nil {
		_ = cli.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("spawn: acp container attach: %w", err)
	}

	if err := cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		hijacked.Close()
		_ = cli.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("spawn: acp container start: %w", err)
	}

	p := &dockerACPProc{cli: cli, id: created.ID, hijacked: hijacked}
	// Tty:false мультиплексирует stdout/stderr в один поток (stdcopy): демультиплексор
	// раскладывает stdout adapter'а в pipe для JSON-RPC, stderr — в лог процесса brigade.
	pr, pw := io.Pipe()
	p.stdout = pr
	go func() {
		_, err := stdcopy.StdCopy(pw, os.Stderr, hijacked.Reader)
		_ = pw.CloseWithError(err)
	}()
	return p, nil
}

// dockerACPProc — acp.AgentProc над контейнером adapter'а.
type dockerACPProc struct {
	cli      dockerAPI
	id       string
	hijacked types.HijackedResponse
	stdout   io.Reader

	waitOnce sync.Once
	waitErr  error
}

// dockerAPI — подмножество docker-клиента, используемое процессом (сужение для тестов).
type dockerAPI interface {
	ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error
	ContainerKill(ctx context.Context, containerID, signal string) error
	ContainerWait(ctx context.Context, containerID string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error)
}

func (p *dockerACPProc) Stdin() io.WriteCloser { return stdinHalfCloser{p.hijacked} }
func (p *dockerACPProc) Stdout() io.Reader     { return p.stdout }

func (p *dockerACPProc) Signal() error {
	timeout := dockerStopTimeoutSeconds
	return p.cli.ContainerStop(context.Background(), p.id, container.StopOptions{Timeout: &timeout})
}

func (p *dockerACPProc) Kill() error {
	return p.cli.ContainerKill(context.Background(), p.id, "KILL")
}

func (p *dockerACPProc) Wait() error {
	p.waitOnce.Do(func() {
		statusCh, errCh := p.cli.ContainerWait(context.Background(), p.id, container.WaitConditionNotRunning)
		select {
		case err := <-errCh:
			// «No such container» после AutoRemove — штатный исход, не ошибка teardown.
			if err != nil && !strings.Contains(err.Error(), "No such container") {
				p.waitErr = err
			}
		case <-statusCh:
		}
		p.hijacked.Close()
	})
	return p.waitErr
}

// stdinHalfCloser закрывает только пишущую половину attach-соединения (CloseWrite):
// adapter получает EOF на stdin, а stdout продолжает читаться до завершения процесса.
type stdinHalfCloser struct {
	hijacked types.HijackedResponse
}

func (s stdinHalfCloser) Write(b []byte) (int, error) { return s.hijacked.Conn.Write(b) }

func (s stdinHalfCloser) Close() error { return s.hijacked.CloseWrite() }

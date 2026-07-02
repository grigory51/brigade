package spawn

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
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

// ContainerWorkdir — точка монтирования рабочей директории сессии внутри контейнера.
// Подпапка из WorkDir хоста (Spec.Cwd) bind-mount'ится сюда. Экспортирована: в
// docker-режиме ACP-агенту передаётся именно этот путь (хостовый живёт только в bind).
const ContainerWorkdir = "/workspace"
const containerWorkdir = ContainerWorkdir

// DockerSpawner запускает каждого агента в отдельном контейнере (контейнер на сессию).
type DockerSpawner struct {
	cli *client.Client
}

// NewDockerSpawner создаёт DockerSpawner с клиентом Docker из окружения
// (DOCKER_HOST и т. п.). Клиент следует закрыть через Close при остановке сервиса.
func NewDockerSpawner() (*DockerSpawner, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("spawn: docker client: %w", err)
	}
	return &DockerSpawner{cli: cli}, nil
}

// Close освобождает ресурсы клиента Docker.
func (s *DockerSpawner) Close() error { return s.cli.Close() }

// Spawn создаёт контейнер сессии, запускает его и подключается (attach) к его TTY.
//
// Контейнер помечается label brigade.session.id=<SessionID>. Рабочая директория
// сессии (Spec.Cwd, подпапка WorkDir хоста) bind-mount'ится в containerWorkdir —
// именно bind-mount подпапки, а не named volume, как требует контракт изоляции.
func (s *DockerSpawner) Spawn(ctx context.Context, spec Spec) (Handle, error) {
	image := spec.Image
	if image == "" {
		image = defaultImage
	}

	cfg := &container.Config{
		Image:        image,
		Cmd:          []string{agentCommand},
		Env:          spec.Env,
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
	hostCfg := &container.HostConfig{Init: &initProcess}
	if spec.Cwd != "" {
		// bind-mount подпапки рабочей директории внутрь контейнера.
		hostCfg.Binds = []string{fmt.Sprintf("%s:%s", spec.Cwd, containerWorkdir)}
	}

	created, err := s.cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "brigade-"+spec.SessionID)
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

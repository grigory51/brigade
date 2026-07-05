package spawn

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// execPollInterval — период опроса состояния exec-процесса в Wait. Docker API не
// даёт блокирующего ожидания exec'а (в отличие от ContainerWait) — только inspect.
const execPollInterval = time.Second

// spawnExecCLI запускает CLI-сессию exec'ом в общем контейнере пользователя: TTY,
// рабочая директория агента, окружение сессии + маркер sessionMarkEnv (по нему
// процесс находят teardown и зачистка орфанов — Docker API не умеет завершать exec).
func (s *DockerSpawner) spawnExecCLI(ctx context.Context, containerID, sessionID string, env []string, cmd []string) (Handle, error) {
	execResp, err := s.cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Tty:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		WorkingDir:   containerWorkdir,
		Env:          append(append([]string{}, env...), sessionMarkEnv+"="+sessionID),
		Cmd:          cmd,
	})
	if err != nil {
		return nil, fmt.Errorf("spawn: cli exec create: %w", err)
	}

	hijacked, err := s.cli.ContainerExecAttach(ctx, execResp.ID, container.ExecStartOptions{Tty: true})
	if err != nil {
		return nil, fmt.Errorf("spawn: cli exec attach: %w", err)
	}

	return &execCLIHandle{
		cli:         s.cli,
		containerID: containerID,
		execID:      execResp.ID,
		sessionID:   sessionID,
		hijacked:    hijacked,
		exitCode:    -1,
	}, nil
}

// execCLIHandle — Handle CLI-сессии над docker exec в общем per-user контейнере.
//
// При TTY вывод не мультиплексируется (без stdcopy-заголовков) — Reader отдаёт
// raw-байты. В отличие от dockerHandle жизненный цикл контейнера этому Handle не
// принадлежит: Terminate убивает только процесс сессии (по маркеру), контейнером
// распоряжается реестр (см. releaseUserContainerIfIdle).
type execCLIHandle struct {
	cli         *client.Client
	containerID string
	// execID — идентификатор exec-инстанса (resize, inspect в Wait).
	execID string
	// sessionID — идентификатор сессии brigade; он же значение маркера sessionMarkEnv
	// и идентификатор сессии агента (claude запущен с --session-id <sessionID>).
	sessionID string
	hijacked  types.HijackedResponse

	// hist хранит хвост вывода терминала для восстановления экрана при reconnect.
	hist history

	waitOnce sync.Once
	waitErr  error
	exitCode int
}

// Read читает вывод exec'а и накапливает хвост для восстановления экрана.
func (h *execCLIHandle) Read(p []byte) (int, error) {
	n, err := h.hijacked.Reader.Read(p)
	if n > 0 {
		h.hist.append(p[:n])
	}
	return n, err
}

func (h *execCLIHandle) Write(p []byte) (int, error) { return h.hijacked.Conn.Write(p) }

// History возвращает копию накопленного хвоста вывода терминала.
func (h *execCLIHandle) History() []byte { return h.hist.snapshot() }

// Close закрывает attach-соединение. Процесс сессии продолжает работать (pty exec'а
// держит dockerd); окончательное завершение — Terminate при Stop/Delete.
func (h *execCLIHandle) Close() error {
	h.hijacked.Close()
	return nil
}

// Terminate завершает процесс сессии внутри общего контейнера: kill по маркеру
// sessionMarkEnv (сам claude и его дети, унаследовавшие окружение). Контейнер не
// трогается — им распоряжается реестр, когда закрыта последняя сессия пользователя.
// Идемпотентна: отсутствие процессов под маркером — штатный исход.
func (h *execCLIHandle) Terminate(ctx context.Context) error {
	h.hijacked.Close()
	return killByEnvMark(ctx, h.cli, h.containerID, sessionMarkEnv, h.sessionID)
}

// Resize меняет размер TTY exec-процесса.
func (h *execCLIHandle) Resize(cols, rows uint16) error {
	return h.cli.ContainerExecResize(context.Background(), h.execID, container.ResizeOptions{
		Height: uint(rows),
		Width:  uint(cols),
	})
}

// Wait блокируется до завершения exec-процесса, опрашивая его состояние: блокирующего
// ожидания exec'а в Docker API нет. Ошибка inspect трактуется как завершение (демон
// недоступен — судьбу процесса всё равно не узнать), код выхода фиксируется из
// последнего inspect. Идемпотентна.
func (h *execCLIHandle) Wait() error {
	h.waitOnce.Do(func() {
		ticker := time.NewTicker(execPollInterval)
		defer ticker.Stop()
		for range ticker.C {
			info, err := h.cli.ContainerExecInspect(context.Background(), h.execID)
			if err != nil {
				log.Printf("spawn: cli exec inspect %s: %v", h.execID, err)
				h.waitErr = err
				return
			}
			if !info.Running {
				h.exitCode = info.ExitCode
				return
			}
		}
	})
	return h.waitErr
}

func (h *execCLIHandle) ExitCode() int { return h.exitCode }

// AgentSessionID — идентификатор сессии агента. Задан при спавне (--session-id), по
// нему Reattach делает `claude --resume` после рестарта бэкенда.
func (h *execCLIHandle) AgentSessionID() string { return h.sessionID }

// ContainerLabel пуст: у shared-схемы нет контейнера с label brigade.session.id;
// пустое значение в store — признак схемы при Reattach.
func (h *execCLIHandle) ContainerLabel() string { return "" }

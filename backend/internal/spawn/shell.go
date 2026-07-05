package spawn

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/creack/pty"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
)

// ShellHandle — управление вспомогательным шеллом сессии: поток псевдотерминала,
// изменение размера и завершение. Подмножество Handle, достаточное транспорту
// (termws.Shell удовлетворяется структурно). Хвост вывода (History) для шелла пуст:
// его жизнь равна жизни одного WS-подключения, восстанавливать экран не для кого.
type ShellHandle interface {
	io.ReadWriter
	Resize(cols, rows uint16) error
	History() []byte
	Terminate(ctx context.Context) error
}

// StartLocalShell запускает интерактивный шелл хост-машины в pty с рабочей
// директорией cwd. Команда берётся из $SHELL хоста (fallback bash) — шелл
// предназначен пользователю brigade для ручного осмотра рабочей директории сессии.
func StartLocalShell(ctx context.Context, cwd string) (ShellHandle, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "bash"
	}

	cmd := exec.CommandContext(ctx, shell)
	cmd.Dir = cwd
	cmd.Env = os.Environ()

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("spawn: local shell: %w", err)
	}
	// Переиспользуем localHandle: его Terminate реализует полный teardown
	// (EOF на pty → SIGTERM → SIGKILL → reap без зомби).
	return &localHandle{ptmx: ptmx, cmd: cmd, exitCode: -1}, nil
}

// shellMarkEnv — переменная окружения, помечающая процессы exec-шелла для teardown.
// Docker API не умеет завершать exec-процесс, поэтому Terminate находит процессы по
// этому маркеру в /proc/*/environ и убивает их (дети шелла наследуют окружение и
// попадают под тот же маркер).
const shellMarkEnv = "BRIGADE_SHELL_MARK"

// SpawnShell запускает интерактивный шелл внутри контейнера сессии через docker exec.
// Контейнер отыскивается по label brigade.session.id (legacy CLI, ACP) либо как общий
// контейнер пользователя (shared CLI); он должен быть запущен — exec в остановленный
// контейнер невозможен.
func (s *DockerSpawner) SpawnShell(ctx context.Context, sessionLabel, userID string) (ShellHandle, error) {
	id, err := s.findSessionOrUserContainer(ctx, sessionLabel, userID)
	if err != nil {
		return nil, err
	}

	mark := uuid.NewString()
	execResp, err := s.cli.ContainerExecCreate(ctx, id, container.ExecOptions{
		Tty:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		WorkingDir:   containerWorkdir,
		Env:          []string{shellMarkEnv + "=" + mark},
		Cmd:          []string{"/bin/bash"},
	})
	if err != nil {
		return nil, fmt.Errorf("spawn: shell exec create: %w", err)
	}

	hijacked, err := s.cli.ContainerExecAttach(ctx, execResp.ID, container.ExecStartOptions{Tty: true})
	if err != nil {
		return nil, fmt.Errorf("spawn: shell exec attach: %w", err)
	}

	return &execShellHandle{
		cli:         s.cli,
		containerID: id,
		execID:      execResp.ID,
		mark:        mark,
		hijacked:    hijacked,
	}, nil
}

// execShellHandle — ShellHandle над docker exec с TTY. При TTY вывод не
// мультиплексируется (без stdcopy-заголовков), поэтому Reader отдаёт raw-байты
// терминала напрямую.
type execShellHandle struct {
	cli         *client.Client
	containerID string
	// execID — идентификатор exec-инстанса (нужен для resize).
	execID string
	// mark — значение shellMarkEnv в окружении шелла; по нему Terminate находит и
	// убивает процессы шелла внутри контейнера.
	mark     string
	hijacked types.HijackedResponse
}

func (h *execShellHandle) Read(p []byte) (int, error)  { return h.hijacked.Reader.Read(p) }
func (h *execShellHandle) Write(p []byte) (int, error) { return h.hijacked.Conn.Write(p) }

// History для exec-шелла всегда пуст: жизнь шелла равна жизни WS-подключения.
func (h *execShellHandle) History() []byte { return nil }

// Resize меняет размер TTY exec-процесса.
func (h *execShellHandle) Resize(cols, rows uint16) error {
	return h.cli.ContainerExecResize(context.Background(), h.execID, container.ResizeOptions{
		Height: uint(rows),
		Width:  uint(cols),
	})
}

// Terminate закрывает attach-соединение и убивает процессы шелла внутри контейнера.
// Docker API не умеет завершать exec-процесс, а разрыв attach его не трогает (pty
// exec-инстанса держит демон) — без явного kill осиротевшие шеллы копились бы в
// контейнере. Контейнер сессии продолжает работать.
func (h *execShellHandle) Terminate(ctx context.Context) error {
	h.hijacked.Close()
	return killByEnvMark(ctx, h.cli, h.containerID, shellMarkEnv, h.mark)
}

// killByEnvMark убивает внутри контейнера все процессы, в окружении которых есть
// envKey=value: сам целевой процесс и его дети (окружение наследуется). Это обходной
// путь завершения exec-процессов — Docker API их убивать не умеет. Запускается
// detach-exec'ом со скриптом по /proc/*/environ; отсутствие совпадений — штатный
// исход (процесс уже завершился).
func killByEnvMark(ctx context.Context, cli *client.Client, containerID, envKey, value string) error {
	script := fmt.Sprintf(
		`for p in /proc/[0-9]*; do grep -qa %s=%s "$p/environ" 2>/dev/null && kill -9 "${p#/proc/}" 2>/dev/null; done; true`,
		envKey, value)
	execResp, err := cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Detach: true,
		Cmd:    []string{"/bin/sh", "-c", script},
	})
	if err != nil {
		return fmt.Errorf("spawn: mark cleanup create: %w", err)
	}
	if err := cli.ContainerExecStart(ctx, execResp.ID, container.ExecStartOptions{Detach: true}); err != nil {
		return fmt.Errorf("spawn: mark cleanup start: %w", err)
	}
	return nil
}

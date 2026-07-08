package spawn

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// ShellHandle — управление вспомогательным шеллом сессии: поток псевдотерминала,
// изменение размера и завершение. Подмножество Handle, достаточное транспорту
// (termws.Shell удовлетворяется структурно). Хвост вывода (History) для шелла пуст:
// его жизнь равна жизни одного WS-подключения, восстанавливать экран не для кого.
//
// Docker-шелл теперь идёт через фасад-демон (acpremote.OpenShell / cliremote.OpenShell —
// эфемерный терминал в контейнере), а не docker-exec. Здесь остаётся только local-шелл
// (процесс хоста в pty).
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

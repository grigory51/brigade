package spawn

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// agentCommand — исполняемый файл агента. Claude Code предоставляет TTY-режим из
// коробки, при resume принимает `--resume <agent_session_id>`.
const agentCommand = "claude"

// LocalSpawner запускает агентов как процессы хост-машины внутри pty.
type LocalSpawner struct{}

// NewLocalSpawner создаёт LocalSpawner.
func NewLocalSpawner() *LocalSpawner { return &LocalSpawner{} }

// Spawn запускает `claude` в новом pty.
func (s *LocalSpawner) Spawn(ctx context.Context, spec Spec) (Handle, error) {
	return startLocal(ctx, []string{}, spec.Cwd, spec.Env, spec.SessionID)
}

// Reattach в local-режиме восстанавливает сессию повторным запуском агента с
// `--resume <agent_session_id>` (отдельного работающего процесса нет: при рестарте
// бэкенда дочерние процессы уже завершены, состояние агента хранит он сам).
func (s *LocalSpawner) Reattach(ctx context.Context, p Persisted) (Handle, error) {
	if p.AgentSessionID == "" {
		return nil, errors.New("spawn: local reattach requires agent session id")
	}
	return startLocal(ctx, []string{"--resume", p.AgentSessionID}, p.Cwd, p.Env, p.SessionID)
}

// startLocal формирует команду агента, запускает её в pty и оборачивает в localHandle.
func startLocal(ctx context.Context, args []string, cwd string, env []string, sessionID string) (Handle, error) {
	cmd := exec.CommandContext(ctx, agentCommand, args...)
	cmd.Dir = cwd
	// Наследуем окружение хоста и добавляем переданные переменные (CLAUDE_CODE_OAUTH_TOKEN
	// и т. п.) поверх него.
	cmd.Env = append(os.Environ(), env...)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}

	h := &localHandle{
		ptmx:           ptmx,
		cmd:            cmd,
		sessionID:      sessionID,
		exitCode:       -1,
		agentSessionID: agentSessionIDFromArgs(args),
	}
	return h, nil
}

// agentSessionIDFromArgs извлекает agent_session_id из аргументов resume, если он там
// есть. При первичном запуске идентификатор сообщает сам агент (вне рамок этого
// модуля); здесь он остаётся пустым до момента, когда вызывающий код его установит.
func agentSessionIDFromArgs(args []string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--resume" {
			return args[i+1]
		}
	}
	return ""
}

// localHandle — Handle над pty локального процесса агента.
type localHandle struct {
	ptmx      *os.File
	cmd       *exec.Cmd
	sessionID string

	agentSessionID string

	// hist хранит хвост вывода pty для восстановления экрана при переподключении.
	hist history

	waitOnce sync.Once
	waitErr  error
	exitCode int
}

// Read читает вывод pty и попутно накапливает его хвост в hist, чтобы новое
// WebSocket-подключение могло восстановить содержимое терминала.
func (h *localHandle) Read(p []byte) (int, error) {
	n, err := h.ptmx.Read(p)
	if n > 0 {
		h.hist.append(p[:n])
	}
	return n, err
}

func (h *localHandle) Write(p []byte) (int, error) { return h.ptmx.Write(p) }

// History возвращает копию накопленного хвоста вывода терминала.
func (h *localHandle) History() []byte { return h.hist.snapshot() }

// Close закрывает pty. Процесс завершится по EOF на своём управляющем терминале;
// ожидание его кода завершения выполняется в Wait.
func (h *localHandle) Close() error { return h.ptmx.Close() }

// Terminate завершает процесс агента и реапит его. Закрытие pty даёт процессу EOF на
// управляющем терминале (он выходит сам); затем ждём завершения в пределах бюджета, при
// необходимости эскалируя до SIGTERM и SIGKILL. Wait в конце реапит процесс (без зомби).
func (h *localHandle) Terminate(ctx context.Context) error {
	// EOF на pty — мягкий сигнал агенту завершиться.
	_ = h.ptmx.Close()

	// Реапим в фоне: Wait идемпотентна (waitOnce), поэтому безопасна и при гонке с
	// внешним наблюдателем (watchExit).
	exited := make(chan struct{})
	go func() {
		_ = h.Wait()
		close(exited)
	}()

	// Бюджет ожидания мягкого выхода: либо ctx вызывающего, либо короткий внутренний.
	wait := func(d time.Duration) bool {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-exited:
			return true
		case <-ctx.Done():
			return false
		case <-timer.C:
			return false
		}
	}

	if wait(3 * time.Second) {
		return nil
	}
	if h.cmd.Process != nil {
		_ = h.cmd.Process.Signal(syscall.SIGTERM)
	}
	if wait(2 * time.Second) {
		return nil
	}
	if h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
	}
	<-exited
	return nil
}

// Resize задаёт размер pty.
func (h *localHandle) Resize(cols, rows uint16) error {
	return pty.Setsize(h.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
}

// Wait дожидается завершения процесса и фиксирует код выхода. Идемпотентна.
func (h *localHandle) Wait() error {
	h.waitOnce.Do(func() {
		err := h.cmd.Wait()
		h.waitErr = err
		var exitErr *exec.ExitError
		switch {
		case err == nil:
			h.exitCode = 0
		case errors.As(err, &exitErr):
			h.exitCode = exitErr.ExitCode()
		default:
			h.exitCode = -1
		}
	})
	return h.waitErr
}

func (h *localHandle) ExitCode() int          { return h.exitCode }
func (h *localHandle) AgentSessionID() string { return h.agentSessionID }

// ContainerLabel для local-режима всегда пуст: контейнера нет.
func (h *localHandle) ContainerLabel() string { return "" }

package acp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// AgentProc — запущенный процесс ACP-adapter'а, абстрагированный от способа запуска:
// локальный subprocess (localProc) или Docker-контейнер (spawn.DockerACPProc).
// Client общается с adapter'ом только через эти каналы и сигналы.
type AgentProc interface {
	// Stdin — канал запросов adapter'у. Закрытие — мягкий сигнал завершения (EOF).
	Stdin() io.WriteCloser
	// Stdout — канал ответов и нотификаций adapter'а.
	Stdout() io.Reader
	// Signal просит процесс завершиться (SIGTERM / graceful stop контейнера).
	Signal() error
	// Kill завершает процесс немедленно.
	Kill() error
	// Wait блокируется до завершения процесса и освобождает его ресурсы (reap).
	// Идемпотентен: повторные вызовы возвращают тот же результат.
	Wait() error
}

// ProcSpawner порождает процесс adapter'а. Передаётся в Options.SpawnProc; nil означает
// локальный subprocess claude-agent-acp (см. spawnLocalProc).
type ProcSpawner func(ctx context.Context) (AgentProc, error)

// spawnLocalProc запускает claude-agent-acp локальным subprocess'ом (режим local).
func spawnLocalProc(opts Options) (AgentProc, error) {
	// Субпроцесс не привязываем к ctx вызова: его жизнь равна жизни сессии, а не
	// вызову конструктора. Остановка — через Signal/Kill.
	cmd := exec.Command(adapterBinary)
	cmd.Dir = opts.Cwd
	cmd.Stderr = os.Stderr
	cmd.Env = append(append(os.Environ(), opts.ExtraEnv...), "CLAUDE_CODE_OAUTH_TOKEN="+opts.OAuthToken)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("acp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("acp: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("acp: запуск %q: %w", adapterBinary, err)
	}
	return &localProc{cmd: cmd, stdin: stdin, stdout: stdout}, nil
}

// localProc — AgentProc над локальным exec.Cmd.
type localProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader

	waitOnce sync.Once
	waitErr  error
}

func (p *localProc) Stdin() io.WriteCloser { return p.stdin }
func (p *localProc) Stdout() io.Reader     { return p.stdout }

func (p *localProc) Signal() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Signal(syscall.SIGTERM)
}

func (p *localProc) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

func (p *localProc) Wait() error {
	p.waitOnce.Do(func() { p.waitErr = p.cmd.Wait() })
	return p.waitErr
}

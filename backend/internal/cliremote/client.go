// Package cliremote — brigade-сторона CLI-агента поверх durable-терминала демона.
//
// CLI-режим: `claude` живёт в pty per-user демона (агента brigade, pid1 общего контейнера
// пользователя). Общий контейнер сохраняет логин claude (авторизация привязана к контейнеру),
// а каждая CLI-сессия — отдельный durable-терминал по своему id. brigade ходит к демону по
// Connect-RPC (Terminal API), а не docker-exec'ом — не завязывается на способ спавна.
//
// Client реализует spawn.Handle: Read из потока вывода (через io.Pipe), Write/Resize — unary
// RPC, Wait — до exited-сигнала демона, Close — детач (процесс durable, переживает рестарт
// brigade). Авторизация — подпись brigade (aud = per-user идентификатор демона), заголовок на
// каждый вызов; terminal id = brigade session id.
package cliremote

import (
	"context"
	"io"
	"log"
	"net/http"
	"sync"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/gen/go/brigade/v1/brigadev1connect"
)

// Client — brigade-сторона одной CLI-сессии (durable-терминал демона). Реализует spawn.Handle.
type Client struct {
	rpc       brigadev1connect.AgentDaemonServiceClient
	signToken func() (string, error)
	termID    string // id терминала = brigade session id (== agent session id для claude)

	cancel context.CancelFunc // отменяет OpenTerminal-стрим (детач на Close)
	pr     *io.PipeReader
	pw     *io.PipeWriter

	mu       sync.Mutex
	exitCode int
	waitCh   chan struct{} // закрыт при завершении процесса (получен exited-сигнал)
}

// New создаёт клиент к per-user демону по baseURL. termID — id CLI-сессии (терминала);
// signToken подписывает вызовы (aud = идентификатор per-user демона).
func New(baseURL, termID string, signToken func() (string, error)) *Client {
	return &Client{
		rpc:       brigadev1connect.NewAgentDaemonServiceClient(http.DefaultClient, baseURL),
		signToken: signToken,
		termID:    termID,
		waitCh:    make(chan struct{}),
	}
}

func (c *Client) sign() string {
	t, err := c.signToken()
	if err != nil {
		log.Printf("cliremote: sign daemon token: %v", err)
		return ""
	}
	return t
}

func authReq[T any](token string, msg *T) *connect.Request[T] {
	r := connect.NewRequest(msg)
	if token != "" {
		r.Header().Set("Authorization", "Bearer "+token)
	}
	return r
}

// Start открывает durable-терминал агента (cmd = claude ...). При reconnect после рестарта
// brigade демон переотдаёт scrollback (восстановление экрана через Read) и продолжает живой
// процесс; при мёртвом контейнере (respawn) cmd поднимает claude заново (--resume).
func (c *Client) Start(cmd []string, cwd string, env []string, cols, rows uint16) error {
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := c.rpc.OpenTerminal(ctx, authReq(c.sign(), &v1.DaemonOpenTerminalRequest{
		Id:      c.termID,
		Cmd:     cmd,
		Cwd:     cwd,
		Env:     env,
		Cols:    uint32(cols),
		Rows:    uint32(rows),
		Durable: true,
	}))
	if err != nil {
		cancel()
		return err
	}
	c.cancel = cancel
	c.pr, c.pw = io.Pipe()
	go c.pump(stream)
	return nil
}

// pump перекладывает поток вывода в pipe (его читает Read); exited-сигнал закрывает waitCh
// (завершение процесса), конец потока без него — детач (процесс жив).
func (c *Client) pump(stream *connect.ServerStreamForClient[v1.DaemonTerminalOutput]) {
	for stream.Receive() {
		msg := stream.Msg()
		if msg.Exited {
			c.mu.Lock()
			c.exitCode = int(msg.ExitCode)
			select {
			case <-c.waitCh:
			default:
				close(c.waitCh)
			}
			c.mu.Unlock()
			continue
		}
		if _, err := c.pw.Write(msg.Data); err != nil {
			break
		}
	}
	_ = c.pw.CloseWithError(io.EOF)
}

// --- spawn.Handle ---

func (c *Client) Read(p []byte) (int, error) { return c.pr.Read(p) }

func (c *Client) Write(p []byte) (int, error) {
	_, err := c.rpc.TerminalInput(context.Background(), authReq(c.sign(),
		&v1.DaemonTerminalInputRequest{Id: c.termID, Data: p}))
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *Client) Resize(cols, rows uint16) error {
	_, err := c.rpc.TerminalResize(context.Background(), authReq(c.sign(),
		&v1.DaemonTerminalResizeRequest{Id: c.termID, Cols: uint32(cols), Rows: uint32(rows)}))
	return err
}

// Close отцепляет клиента (детач стрима), НЕ гася процесс: демон и его контейнер переживают
// рестарт brigade, reconnect создаёт новый Client. Wait при этом не разблокируется.
func (c *Client) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	if c.pw != nil {
		_ = c.pw.CloseWithError(io.EOF)
	}
	return nil
}

// Terminate — teardown агента. Per-user контейнер демона удаляется реестром отдельно
// (releaseUserContainerIfIdle), что гасит процесс; здесь только детач потока.
func (c *Client) Terminate(context.Context) error { return c.Close() }

// Wait блокируется до завершения процесса агента (exited-сигнал демона). Детач (Close) его
// НЕ разблокирует — процесс durable, живёт для reconnect.
func (c *Client) Wait() error {
	<-c.waitCh
	return nil
}

func (c *Client) ExitCode() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exitCode
}

// History пуст: scrollback приходит первым сообщением durable-стрима (через Read) на attach.
func (c *Client) History() []byte { return nil }

// AgentSessionID — id ACP/agent-сессии для resume (для CLI == id терминала == session id,
// claude запускается с --session-id/--resume по нему).
func (c *Client) AgentSessionID() string { return c.termID }

// ContainerLabel пуст: CLI живёт в общем per-user контейнере (нет метки brigade.session.id).
// По пустому значению restore выбирает per-user-daemon-ветку (как и прежняя shared-схема).
func (c *Client) ContainerLabel() string { return "" }

// --- вспомогательный шелл на per-user демоне ---

// OpenShell поднимает вспомогательный шелл (bash в pty) на per-user демоне по baseURL и
// возвращает ShellSession (реализует termws.Shell). Эфемерный: Terminate гасит pty в демоне.
func OpenShell(baseURL, cwd string, signToken func() (string, error)) (*ShellSession, error) {
	rpc := brigadev1connect.NewAgentDaemonServiceClient(http.DefaultClient, baseURL)
	sh := &ShellSession{rpc: rpc, signToken: signToken, id: uuid.NewString()}
	ctx, cancel := context.WithCancel(context.Background())
	sh.cancel = cancel
	stream, err := rpc.OpenTerminal(ctx, authReq(sh.sign(), &v1.DaemonOpenTerminalRequest{
		Id:      sh.id,
		Cmd:     []string{"/bin/bash"},
		Cwd:     cwd,
		Durable: false,
	}))
	if err != nil {
		cancel()
		return nil, err
	}
	sh.pr, sh.pw = io.Pipe()
	go sh.pump(stream)
	return sh, nil
}

// ShellSession — brigade-сторона вспомогательного шелла CLI-сессии поверх Terminal RPC
// per-user демона. Реализует termws.Shell.
type ShellSession struct {
	rpc       brigadev1connect.AgentDaemonServiceClient
	signToken func() (string, error)
	id        string
	cancel    context.CancelFunc
	pr        *io.PipeReader
	pw        *io.PipeWriter
}

func (sh *ShellSession) sign() string {
	t, err := sh.signToken()
	if err != nil {
		log.Printf("cliremote: sign daemon token: %v", err)
		return ""
	}
	return t
}

func (sh *ShellSession) pump(stream *connect.ServerStreamForClient[v1.DaemonTerminalOutput]) {
	for stream.Receive() {
		if _, err := sh.pw.Write(stream.Msg().Data); err != nil {
			break
		}
	}
	_ = sh.pw.CloseWithError(io.EOF)
}

func (sh *ShellSession) Read(p []byte) (int, error) { return sh.pr.Read(p) }

func (sh *ShellSession) Write(p []byte) (int, error) {
	_, err := sh.rpc.TerminalInput(context.Background(), authReq(sh.sign(),
		&v1.DaemonTerminalInputRequest{Id: sh.id, Data: p}))
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (sh *ShellSession) Resize(cols, rows uint16) error {
	_, err := sh.rpc.TerminalResize(context.Background(), authReq(sh.sign(),
		&v1.DaemonTerminalResizeRequest{Id: sh.id, Cols: uint32(cols), Rows: uint32(rows)}))
	return err
}

func (sh *ShellSession) History() []byte { return nil }

func (sh *ShellSession) Terminate(context.Context) error {
	sh.cancel()
	if sh.pw != nil {
		_ = sh.pw.CloseWithError(io.EOF)
	}
	return nil
}

package acpdaemon

import (
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
)

// termHistoryCap — размер scrollback на терминал (хвост вывода для восстановления экрана
// durable-терминала на reconnect brigade).
const termHistoryCap = 256 << 10 // 256 KiB

// terminal — pty одной команды внутри среды агента. Единственный читатель pty (readLoop)
// накапливает scrollback и шлёт вывод текущему подписчику (одно подключение brigade за раз).
type terminal struct {
	id      string
	durable bool
	ptmx    *os.File
	cmd     *exec.Cmd
	doneCh  chan struct{} // закрыт при выходе процесса

	mu      sync.Mutex
	hist    []byte
	subCh   chan []byte   // канал текущего подписчика (nil — нет)
	subDone chan struct{} // закрывается, чтобы отцепить подписчика (reconnect)
}

func (t *terminal) appendHist(b []byte) {
	t.hist = append(t.hist, b...)
	if len(t.hist) > termHistoryCap {
		t.hist = append([]byte(nil), t.hist[len(t.hist)-termHistoryCap:]...)
	}
}

// readLoop — единственный читатель pty: копит scrollback и шлёт текущему подписчику. По EOF
// (процесс завершился) закрывает doneCh и вызывает onExit (снятие с учёта durable-терминала).
func (t *terminal) readLoop(onExit func()) {
	buf := make([]byte, 32*1024)
	for {
		n, err := t.ptmx.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			t.mu.Lock()
			t.appendHist(chunk)
			sub, done := t.subCh, t.subDone
			t.mu.Unlock()
			if sub != nil {
				// Блокировка на медленном подписчике = backpressure pty (штатно для TTY);
				// отцепление подписчика прерывает отправку (чанк уже в scrollback).
				select {
				case sub <- chunk:
				case <-done:
				}
			}
		}
		if err != nil {
			break
		}
	}
	close(t.doneCh)
	onExit()
}

// subscribe отдаёт scrollback и канал live-вывода, отцепляя прежнего подписчика (reconnect).
func (t *terminal) subscribe() (scrollback []byte, ch <-chan []byte, detach func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.subDone != nil {
		close(t.subDone) // выгнать прежнего подписчика
	}
	done := make(chan struct{})
	c := make(chan []byte, 64)
	t.subCh, t.subDone = c, done
	sb := append([]byte(nil), t.hist...)
	return sb, c, func() {
		t.mu.Lock()
		if t.subDone == done {
			close(done)
			t.subCh, t.subDone = nil, nil
		}
		t.mu.Unlock()
	}
}

func (t *terminal) write(data []byte) error {
	_, err := t.ptmx.Write(data)
	return err
}

func (t *terminal) resize(cols, rows uint16) error {
	return pty.Setsize(t.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}

// kill гасит pty и процесс (эфемерный терминал — при закрытии стрима).
func (t *terminal) kill() {
	_ = t.ptmx.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	go func() { _ = t.cmd.Wait() }() // reap без зомби
}

// openReq — параметры запуска терминала (перекладка DaemonOpenTerminalRequest без gen-типов).
type openReq struct {
	id      string
	cmd     []string
	cwd     string
	env     []string
	cols    uint16
	rows    uint16
	durable bool
}

// terminalMgr — реестр терминалов демона по client-id.
type terminalMgr struct {
	mu   sync.Mutex
	byID map[string]*terminal
}

func newTerminalMgr() *terminalMgr { return &terminalMgr{byID: make(map[string]*terminal)} }

// open возвращает существующий durable-терминал (reattach) или спавнит новый в pty.
func (m *terminalMgr) open(req openReq) (*terminal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if req.durable {
		if t, ok := m.byID[req.id]; ok {
			return t, nil
		}
	}
	cols, rows := req.cols, req.rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	cmd := exec.Command(req.cmd[0], req.cmd[1:]...)
	cmd.Dir = req.cwd
	cmd.Env = append(os.Environ(), req.env...)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, err
	}
	t := &terminal{id: req.id, durable: req.durable, ptmx: ptmx, cmd: cmd, doneCh: make(chan struct{})}
	m.byID[req.id] = t
	go t.readLoop(func() { m.remove(req.id) })
	return t, nil
}

func (m *terminalMgr) get(id string) *terminal {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byID[id]
}

func (m *terminalMgr) remove(id string) {
	m.mu.Lock()
	delete(m.byID, id)
	m.mu.Unlock()
}

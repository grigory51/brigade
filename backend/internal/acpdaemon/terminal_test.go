package acpdaemon

import (
	"strings"
	"testing"
	"time"
)

// recv читает один чанк из канала терминала с таймаутом.
func recv(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case b := <-ch:
		return b
	case <-time.After(3 * time.Second):
		t.Fatal("terminal output timeout")
		return nil
	}
}

// TestTerminalEcho проверяет эфемерный терминал: cat в pty отражает ввод в вывод.
func TestTerminalEcho(t *testing.T) {
	m := newTerminalMgr()
	term, err := m.open(openReq{id: "t1", cmd: []string{"cat"}})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, ch, detach := term.subscribe()
	defer detach()

	if err := term.write([]byte("hello\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	// pty с включённым echo отражает ввод, cat дублирует его в stdout — «hello» появится.
	got := ""
	for i := 0; i < 3 && !strings.Contains(got, "hello"); i++ {
		got += string(recv(t, ch))
	}
	if !strings.Contains(got, "hello") {
		t.Fatalf("echo not seen, got %q", got)
	}

	// resize не должен падать.
	if err := term.resize(100, 30); err != nil {
		t.Fatalf("resize: %v", err)
	}

	// Эфемерный терминал зарегистрирован по id и находится для input/resize.
	if m.get("t1") == nil {
		t.Fatal("terminal not registered")
	}
	term.kill()
}

// TestTerminalDurableReattach проверяет durable-терминал: reopen того же id отдаёт scrollback,
// а не спавнит новый процесс.
func TestTerminalDurableReattach(t *testing.T) {
	m := newTerminalMgr()
	term, err := m.open(openReq{id: "d1", cmd: []string{"cat"}, durable: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, ch, detach := term.subscribe()
	term.write([]byte("scrolltest\n"))
	got := ""
	for i := 0; i < 3 && !strings.Contains(got, "scrolltest"); i++ {
		got += string(recv(t, ch))
	}
	detach() // отцепиться, pty durable — остаётся жив

	// Reopen того же id — тот же терминал (не новый процесс), scrollback содержит прошлый вывод.
	again, err := m.open(openReq{id: "d1", cmd: []string{"cat"}, durable: true})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if again != term {
		t.Fatal("durable reopen spawned a new terminal instead of reattaching")
	}
	scrollback, _, detach2 := again.subscribe()
	defer detach2()
	if !strings.Contains(string(scrollback), "scrolltest") {
		t.Fatalf("scrollback missing prior output: %q", scrollback)
	}
	term.kill()
}

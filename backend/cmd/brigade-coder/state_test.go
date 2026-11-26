package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func testStore(t *testing.T, workspace string) *stateStore {
	t.Helper()
	store, err := openStateStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.close)
	return store
}

func TestResultValidation(t *testing.T) {
	for _, result := range []Result{
		{Name: "site", Kind: WebResult, URL: "https://preview.example.test/app"},
		{Name: "site", Kind: WebResult, URL: "https://preview.example.test/?id=session-3000"},
		{Name: "tool", Kind: TerminalResult, Command: &Command{Argv: []string{"printf", "$(touch forbidden)"}, Cwd: "."}},
	} {
		if err := result.validate(); err != nil {
			t.Fatalf("valid result %#v: %v", result, err)
		}
	}
	for _, result := range []Result{
		{Name: "bad", Kind: WebResult, URL: "javascript:alert(1)"},
		{Name: "bad", Kind: WebResult, URL: "https://user:secret@example.test"},
		{Name: "bad", Kind: WebResult, URL: "https://example.test/?token=secret"},
		{Name: "bad", Kind: WebResult, URL: "https://example.test/?id=one&id=two"},
		{Name: "bad", Kind: WebResult, URL: "https://example.test/#secret"},
		{Name: "bad", Kind: "other"},
		{Name: " bad", Kind: WebResult, URL: "https://example.test"},
		{Name: "bad", Kind: TerminalResult, Command: &Command{Argv: []string{"sh"}, Cwd: "../outside"}},
		{Name: "bad", Kind: TerminalResult, Command: &Command{Argv: []string{}, Cwd: "."}},
		{Name: "bad", Kind: TerminalResult, Command: &Command{Argv: []string{"sh", "a\x00b"}, Cwd: "."}},
	} {
		if err := result.validate(); err == nil {
			t.Errorf("accepted invalid result: %#v", result)
		}
	}
}

func TestRegisterUpdatesSameNameAndOtherProcessSeesIt(t *testing.T) {
	workspace := t.TempDir()
	writer, reader := testStore(t, workspace), testStore(t, workspace)
	for _, url := range []string{"https://preview.example.test/one", "https://preview.example.test/two"} {
		if _, err := writer.register(context.Background(), Result{Name: "web", Kind: WebResult, URL: url}); err != nil {
			t.Fatal(err)
		}
	}
	state, err := reader.read()
	if err != nil || state.Revision != 2 || len(state.Results) != 1 || !strings.HasSuffix(state.Results[0].URL, "/two") {
		t.Fatalf("state = %#v, err = %v", state, err)
	}
	info, err := os.Stat(filepath.Join(workspace, stateDirectory, "state.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state permissions: %v, %v", info, err)
	}
}

func TestStateRejectsCorruptionAndEscapingDirectory(t *testing.T) {
	workspace := t.TempDir()
	store := testStore(t, workspace)
	if err := os.MkdirAll(filepath.Join(workspace, stateDirectory), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, stateDirectory, "state.json")
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.register(context.Background(), Result{Name: "site", Kind: WebResult, URL: "https://example.test"}); err == nil {
		t.Fatal("corrupt state must not be overwritten")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "broken" {
		t.Fatal("corrupt state changed")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(workspace, "outside")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.register(context.Background(), Result{Name: "tool", Kind: TerminalResult, Command: &Command{Argv: []string{"pwd"}, Cwd: "outside"}}); err == nil {
		t.Fatal("accepted escaping cwd")
	}
}

func TestLockCancellation(t *testing.T) {
	workspace := t.TempDir()
	store := testStore(t, workspace)
	if err := os.MkdirAll(filepath.Join(workspace, stateDirectory), 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(workspace, stateDirectory, "state.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := store.register(ctx, Result{Name: "site", Kind: WebResult, URL: "https://example.test"}); err == nil {
		t.Fatal("registration ignored cancellation")
	}
}

func TestProjectSelectionPreservesResultsAndSupportsEmptyWorkspace(t *testing.T) {
	workspace := t.TempDir()
	agent, host := testStore(t, workspace), testStore(t, workspace)
	initial, err := host.read()
	if err != nil || initial.ProjectPath != "." || len(initial.Results) != 0 {
		t.Fatalf("empty workspace state: %#v, %v", initial, err)
	}
	if _, err := agent.register(context.Background(), Result{Name: "site", Kind: WebResult, URL: "https://example.test"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "new-project"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.selectProject(context.Background(), "new-project"); err != nil {
		t.Fatal(err)
	}
	selected, err := host.read()
	if err != nil || selected.ProjectPath != "new-project" || selected.Revision != 2 || len(selected.Results) != 1 {
		t.Fatalf("selected state: %#v, %v", selected, err)
	}
	for _, path := range []string{"/tmp", "../outside", "missing"} {
		if _, err := agent.selectProject(context.Background(), path); err == nil {
			t.Fatalf("accepted invalid project %q", path)
		}
	}
}

func TestStateWriterProcess(t *testing.T) {
	workspace := os.Getenv("CODER_TEST_WORKSPACE")
	if workspace == "" {
		t.Skip("subprocess helper")
	}
	store := testStore(t, workspace)
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("%s-%d", os.Getenv("CODER_TEST_WRITER"), i)
		if _, err := store.register(context.Background(), Result{Name: name, Kind: WebResult, URL: "https://example.test"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTwoProcessesDoNotLoseUpdates(t *testing.T) {
	workspace := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	commands := []*exec.Cmd{}
	for _, name := range []string{"agent", "host"} {
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStateWriterProcess$")
		command.Env = append(os.Environ(), "CODER_TEST_WORKSPACE="+workspace, "CODER_TEST_WRITER="+name)
		command.Stdout, command.Stderr = os.Stderr, os.Stderr
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, command)
	}
	reader := testStore(t, workspace)
	for i := 0; i < 20; i++ {
		if _, err := reader.read(); err != nil {
			t.Fatalf("reader observed non-atomic state: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	for _, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	state, err := reader.read()
	if err != nil || len(state.Results) != 16 || state.Revision != 16 {
		t.Fatalf("lost concurrent updates: %#v, %v", state, err)
	}
}

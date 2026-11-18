package spawn

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCodexCLIWithoutDaemon(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	spawner := NewLocalSpawner()
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "resume"}[resume], func(t *testing.T) {
			var handle Handle
			var err error
			if resume {
				handle, err = spawner.Reattach(context.Background(), Persisted{
					SessionID: "session", AgentSessionID: "agent-session", Cwd: dir, Command: "codex",
				})
			} else {
				handle, err = spawner.Spawn(context.Background(), Spec{SessionID: "session", Cwd: dir, Command: "codex"})
			}
			if err != nil {
				t.Fatal(err)
			}
			defer handle.Terminate(context.Background())
			want := []string{"codex", "--no-daemon"}
			if resume {
				want = append(want, "resume", "--last")
			}
			if got := handle.(*localHandle).cmd.Args; !reflect.DeepEqual(got, want) {
				t.Fatalf("args = %v, want %v", got, want)
			}
		})
	}
}

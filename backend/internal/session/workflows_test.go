package session

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProjectSlug проверяет воспроизведение имени проектной папки Claude Code.
func TestProjectSlug(t *testing.T) {
	cases := map[string]string{
		"/home/agent/workspace":                     "-home-agent-workspace",
		"/Users/u/PersonalWorkspace/github/brigade": "-Users-u-PersonalWorkspace-github-brigade",
		"/srv/my_app/v1.2":                          "-srv-my-app-v1-2",
	}
	for in, want := range cases {
		if got := projectSlug(in); got != want {
			t.Errorf("projectSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestWorkflowInfo проверяет разбор файлов workflow-запуска в обоих состояниях:
// бегущий (journal + скрипт, свежие файлы) и завершённый (wf_<id>.json с meta.name).
func TestWorkflowInfo(t *testing.T) {
	base := t.TempDir()
	r := &Registry{}

	t.Run("бегущий: journal + имя из файла скрипта", func(t *testing.T) {
		runDir := filepath.Join(base, "subagents", "workflows", "wf_run1")
		mustMkdir(t, runDir)
		mustWrite(t, filepath.Join(runDir, "journal.jsonl"),
			`{"type":"started","agentId":"a1"}
{"type":"started","agentId":"a2"}
{"type":"result","agentId":"a1","result":{"x":1}}
`)
		mustMkdir(t, filepath.Join(base, "workflows", "scripts"))
		mustWrite(t, filepath.Join(base, "workflows", "scripts", "my-research-wf_run1.js"), "// script")

		info := r.workflowInfo(base, "wf_run1")
		if info.Name != "my-research" {
			t.Errorf("Name = %q, want my-research", info.Name)
		}
		if info.AgentsStarted != 2 || info.AgentsDone != 1 {
			t.Errorf("агентов %d/%d, want 2/1", info.AgentsDone, info.AgentsStarted)
		}
		if info.Done {
			t.Error("Done = true для бегущего запуска")
		}
		if !info.Active {
			t.Error("Active = false при свежих файлах")
		}
	})

	t.Run("завершённый: имя из meta.name в wf json", func(t *testing.T) {
		runDir := filepath.Join(base, "subagents", "workflows", "wf_done1")
		mustMkdir(t, runDir)
		mustWrite(t, filepath.Join(runDir, "journal.jsonl"),
			`{"type":"started","agentId":"a1"}
{"type":"result","agentId":"a1","result":"ok"}
`)
		mustWrite(t, filepath.Join(base, "workflows", "wf_done1.json"),
			`{"runId":"wf_done1","script":"export const meta = {\n  name: 'deep-research',\n  description: 'x',\n}"}`)

		info := r.workflowInfo(base, "wf_done1")
		if !info.Done {
			t.Error("Done = false при существующем wf json")
		}
		if info.Active {
			t.Error("Active = true для завершённого")
		}
		if info.Name != "deep-research" {
			t.Errorf("Name = %q, want deep-research", info.Name)
		}
	})
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

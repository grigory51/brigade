package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestChangesArgumentsAreLiteralAndReadOnly(t *testing.T) {
	for _, path := range []string{"", ".", "folder/.."} {
		_, status, diff, err := changesArguments(ChangesInput{Path: path})
		if err != nil || status[len(status)-2] != "." || diff[len(diff)-2] != "." {
			t.Fatalf("workspace root must use the all-files pathspec: status=%q diff=%q err=%v", status, diff, err)
		}
	}
	_, status, diff, err := changesArguments(ChangesInput{Scope: "staged", Path: ":(glob)*"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(diff, " "), "--cached -- :(top,literal):(glob)*") {
		t.Fatalf("unsafe pathspec: %q", diff)
	}
	for _, flag := range []string{"--no-ext-diff", "--no-textconv", "--no-renames", "--ignore-submodules=all"} {
		if !strings.Contains(strings.Join(diff, " "), flag) {
			t.Errorf("missing %s", flag)
		}
	}
	if status[0] != "status" || diff[0] != "diff" {
		t.Fatal("unexpected command")
	}
	for _, input := range []ChangesInput{{Scope: "reset"}, {Path: "../outside"}, {Path: "/etc/passwd"}, {Path: "a\x00b"}} {
		if _, _, _, err := changesArguments(input); err == nil {
			t.Errorf("accepted %#v", input)
		}
	}
}

func TestParseStatusAndOutputBounds(t *testing.T) {
	files := parseStatus("R  new name\x00old name\x00?? a\nname\x00 M truncated")
	want := []ChangedFile{{Path: "new name", OriginalPath: "old name", Index: "R", Worktree: " "}, {Path: "a\nname", Index: "?", Worktree: "?"}}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("files = %#v", files)
	}
	output := &boundedBuffer{limit: 5}
	if n, _ := output.Write([]byte("123456789")); n != 9 || output.String() != "12345" || !output.truncated {
		t.Fatalf("bounded writer: %#v, n=%d", output, n)
	}
	if n, _ := output.Write([]byte("more")); n != 4 || output.Len() != 5 {
		t.Fatal("writer grew beyond limit")
	}
	output = &boundedBuffer{limit: 5}
	if _, err := io.Copy(output, io.LimitReader(strings.NewReader("123456789"), 9)); err != nil || output.Len() != 5 || !output.truncated {
		t.Fatalf("io.Copy bypassed output limit: %v, %d bytes", err, output.Len())
	}
}

func TestUntrackedDiffBoundsBinaryAndSymlinks(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for name, text := range map[string]string{"text": "first\nsecond", "large": strings.Repeat("x", 70<<10), "binary": "a\x00b"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	patch, cut, err := untrackedDiff(root, "text")
	if err != nil || cut || !strings.Contains(patch, "+second\n\\ No newline at end of file") {
		t.Fatalf("text diff: %q, %v, %v", patch, cut, err)
	}
	patch, cut, err = untrackedDiff(root, "large")
	if err != nil || !cut || len(patch) > 66000 {
		t.Fatalf("large diff: %d bytes, %v, %v", len(patch), cut, err)
	}
	patch, _, err = untrackedDiff(root, "binary")
	if err != nil || !strings.Contains(patch, "Binary file") {
		t.Fatalf("binary diff: %q, %v", patch, err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := untrackedDiff(root, "link"); err == nil {
		t.Fatal("read symlink")
	}
}

func gitFixture(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Coder Test", "GIT_AUTHOR_EMAIL=coder@example.test", "GIT_COMMITTER_NAME=Coder Test", "GIT_COMMITTER_EMAIL=coder@example.test")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %q: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestBranchIncludesCommitsUncommittedAndUntrackedInProject(t *testing.T) {
	workspace := t.TempDir()
	project := filepath.Join(workspace, "repo")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, project, "init", "-b", "main")
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(project, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("tracked.txt", "base\n")
	gitFixture(t, project, "add", ".")
	gitFixture(t, project, "commit", "-m", "base")
	base := gitFixture(t, project, "rev-parse", "HEAD")
	gitFixture(t, project, "checkout", "-b", "feature")
	write("tracked.txt", "committed\n")
	gitFixture(t, project, "commit", "-am", "feature")
	write("staged.txt", "staged\n")
	gitFixture(t, project, "add", "staged.txt")
	write("tracked.txt", "committed\nunstaged\n")
	write("new.txt", "untracked\n")
	before := gitFixture(t, project, "status", "--porcelain=v1")
	changes, err := readChanges(context.Background(), workspace, ChangesInput{ProjectPath: "repo", Scope: "branch", Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if changes.MergeBase != base || changes.ProjectPath != "repo" {
		t.Fatalf("wrong project/base: %#v", changes)
	}
	for _, line := range []string{"+committed", "+unstaged", "+staged", "+untracked"} {
		if !strings.Contains(changes.Diff, line) {
			t.Errorf("missing %q in diff:\n%s", line, changes.Diff)
		}
	}
	if after := gitFixture(t, project, "status", "--porcelain=v1"); after != before {
		t.Fatalf("Git status changed: %s -> %s", before, after)
	}
	if _, err := readChanges(context.Background(), workspace, ChangesInput{ProjectPath: "repo", Scope: "branch", Base: "does-not-exist"}); err == nil {
		t.Fatal("missing base must fail")
	}
	if _, err := readChanges(context.Background(), workspace, ChangesInput{ProjectPath: "../outside"}); err == nil {
		t.Fatal("escaping project accepted")
	}
	if empty, err := readChanges(context.Background(), workspace, ChangesInput{}); err != nil || empty.Status != "not_repository" {
		t.Fatalf("empty workspace must be a normal state: %#v, %v", empty, err)
	}
	if _, err := readChanges(context.Background(), workspace, ChangesInput{ProjectPath: "repo", Scope: "branch"}); err == nil {
		t.Fatal("existing repository needs an explicit branch base")
	}
}

func TestAllChangesIncludesStagedFilesBeforeFirstCommit(t *testing.T) {
	workspace := t.TempDir()
	gitFixture(t, workspace, "init", "-b", "main")
	for _, name := range []string{".gitignore", "README.md", "a.md"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("content of "+name+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitFixture(t, workspace, "add", ".")
	changes, err := readChanges(context.Background(), workspace, ChangesInput{})
	if err != nil {
		t.Fatal(err)
	}
	if changes.Scope != "all" || len(changes.Files) != 3 {
		t.Fatalf("unexpected status: %#v", changes)
	}
	for _, name := range []string{".gitignore", "README.md", "a.md"} {
		if !strings.Contains(changes.Diff, "+++ b/"+name) || !strings.Contains(changes.Diff, "+content of "+name) {
			t.Errorf("missing %s in diff:\n%s", name, changes.Diff)
		}
	}
}

func TestMissingGitIsDistinctFromBrokenGit(t *testing.T) {
	workspace := t.TempDir()
	state, err := readChanges(context.Background(), workspace, ChangesInput{Scope: "branch"})
	if err != nil || state.Status != "not_repository" {
		t.Fatalf("empty workspace: %#v, %v", state, err)
	}
	if err := os.Symlink("missing-git-dir", filepath.Join(workspace, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, err := readChanges(context.Background(), workspace, ChangesInput{}); err == nil {
		t.Fatal("broken Git symlink must not become not_repository")
	}
}

func TestGitDoesNotExecuteRepositoryFilters(t *testing.T) {
	workspace := t.TempDir()
	gitFixture(t, workspace, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(workspace, "file.txt"), []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, workspace, "add", ".")
	gitFixture(t, workspace, "commit", "-m", "base")
	if err := os.WriteFile(filepath.Join(workspace, ".gitattributes"), []byte("*.txt filter=unsafe diff=unsafe\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, workspace, "config", "filter.unsafe.clean", "touch filter-ran; cat")
	gitFixture(t, workspace, "config", "filter.unsafe.required", "true")
	gitFixture(t, workspace, "config", "diff.unsafe.textconv", "touch textconv-ran")
	if err := os.WriteFile(filepath.Join(workspace, "file.txt"), []byte("after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readChanges(context.Background(), workspace, ChangesInput{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"filter-ran", "textconv-ran"} {
		if _, err := os.Stat(filepath.Join(workspace, name)); !os.IsNotExist(err) {
			t.Fatalf("external command executed: %s", name)
		}
	}
}

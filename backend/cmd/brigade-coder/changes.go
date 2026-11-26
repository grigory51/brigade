package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

type ChangesInput struct {
	ProjectPath string `json:"projectPath,omitempty" jsonschema:"Repository directory relative to workspace; default ."`
	Scope       string `json:"scope,omitempty" jsonschema:"all (default), unstaged, staged, or branch (merge-base through current worktree including untracked)"`
	Base        string `json:"base,omitempty" jsonschema:"Required for branch: explicit commit or ref, for example origin/main; never guessed"`
	Path        string `json:"path,omitempty" jsonschema:"Optional literal project-relative path; no glob or revision syntax"`
}

type ChangedFile struct {
	Path         string `json:"path"`
	OriginalPath string `json:"originalPath,omitempty"`
	Index        string `json:"index"`
	Worktree     string `json:"worktree"`
	Base         string `json:"base,omitempty"`
}

type Changes struct {
	Status          string        `json:"status"`
	ProjectPath     string        `json:"projectPath"`
	Scope           string        `json:"scope"`
	Base            string        `json:"base,omitempty"`
	MergeBase       string        `json:"mergeBase,omitempty"`
	Files           []ChangedFile `json:"files"`
	Diff            string        `json:"diff"`
	DiffTruncated   bool          `json:"diffTruncated"`
	StatusTruncated bool          `json:"statusTruncated"`
	Warnings        []string      `json:"warnings"`
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedBuffer) Len() int       { return b.buffer.Len() }
func (b *boundedBuffer) String() string { return b.buffer.String() }

func (b *boundedBuffer) Write(data []byte) (int, error) {
	size := len(data)
	remaining := b.limit - b.Len()
	if size > remaining {
		b.truncated = true
		data = data[:remaining]
	}
	b.buffer.Write(data)
	return size, nil
}

func runGit(ctx context.Context, workspace string, limit int, args ...string) (string, bool, error) {
	// Отключаем исполняемые расширения Git и унаследованные GIT_* overrides.
	argv := []string{"--no-pager", "--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "core.hooksPath=/dev/null", "-c", "core.quotePath=true"}
	if args[0] == "diff" || args[0] == "status" {
		// clean/process filters также могут запускать команды при чтении worktree.
		keys, truncated, err := runGit(ctx, workspace, 32<<10, "config", "--null", "--name-only", "--get-regexp", `^filter\..*\.(clean|process|required)$`)
		if err != nil || truncated {
			return "", false, fmt.Errorf("cannot safely enumerate Git filters (truncated=%t): %v", truncated, err)
		}
		for _, key := range strings.Split(keys, "\x00") {
			if key == "" {
				continue
			}
			value := ""
			if strings.HasSuffix(key, ".required") {
				value = "false"
			}
			argv = append(argv, "-c", key+"="+value)
		}
	}
	command := exec.CommandContext(ctx, "git", append(argv, args...)...)
	command.Dir = workspace
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1", "LC_ALL=C")
	command.WaitDelay = time.Second
	stdout := &boundedBuffer{limit: limit}
	stderr := &boundedBuffer{limit: 8192}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && stderr.Len() == 0 {
			if (args[0] == "diff" && len(args) > 1 && args[1] == "--no-index") ||
				(args[0] == "symbolic-ref" && len(args) > 1 && args[1] == "--quiet") ||
				(args[0] == "rev-parse" && len(args) > 2 && args[1] == "--verify" && args[2] == "--quiet") {
				return stdout.String(), stdout.truncated, nil
			}
		}
		if args[0] == "config" && errors.As(err, &exit) && exit.ExitCode() == 1 && stderr.Len() == 0 {
			return "", false, nil
		}
		if ctx.Err() != nil {
			return "", false, fmt.Errorf("git cancelled or timed out: %w", ctx.Err())
		}
		return "", false, fmt.Errorf("git %s: %w: %s", args[0], err, stderr.String())
	}
	return stdout.String(), stdout.truncated, nil
}

func changesArguments(input ChangesInput) (string, []string, []string, error) {
	scope := input.Scope
	if scope == "" {
		scope = "all"
	}
	if scope != "all" && scope != "unstaged" && scope != "staged" && scope != "branch" {
		return "", nil, nil, fmt.Errorf("scope must be all, unstaged, staged or branch")
	}
	if scope == "branch" && (len(input.Base) > 1024 || strings.ContainsRune(input.Base, 0)) {
		return "", nil, nil, fmt.Errorf("branch scope requires an explicit base ref, for example origin/main")
	}
	path := "."
	if input.Path != "" {
		if !filepath.IsLocal(input.Path) || len(input.Path) > 4096 || strings.ContainsRune(input.Path, 0) {
			return "", nil, nil, fmt.Errorf("path must be a literal workspace-relative path")
		}
		path = filepath.ToSlash(filepath.Clean(input.Path))
	}
	pathspec := "."
	if path != "." {
		pathspec = ":(top,literal)" + path
	}
	paths := []string{"--", pathspec, ":(top,exclude).brigade/coder"}
	status := append([]string{"status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=all"}, paths...)
	diff := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--ignore-submodules=all", "--submodule=short", "--unified=3"}
	if scope == "staged" {
		diff = append(diff, "--cached")
	}
	return scope, status, append(diff, paths...), nil
}

func parseStatus(output string) []ChangedFile {
	files := []ChangedFile{}
	records := strings.Split(output, "\x00")
	// Последняя запись без NUL может быть обрезана лимитом вывода.
	for i := 0; i < len(records)-1; i++ {
		record := records[i]
		if len(record) < 4 || record[2] != ' ' {
			continue
		}
		file := ChangedFile{Index: record[:1], Worktree: record[1:2], Path: record[3:]}
		if strings.ContainsAny(record[:2], "RC") {
			i++
			if i >= len(records)-1 {
				break
			}
			file.OriginalPath = records[i]
		}
		files = append(files, file)
	}
	return files
}

// openGitProject confines both Git commands and direct file reads to a project root.
func openGitProject(ctx context.Context, workspace, projectPath string) (string, *os.Root, bool, error) {
	if projectPath == "" {
		projectPath = "."
	}
	if !filepath.IsLocal(projectPath) || len(projectPath) > 4096 || strings.ContainsRune(projectPath, 0) {
		return "", nil, false, fmt.Errorf("projectPath must be workspace-relative")
	}
	workspaceRoot, err := os.OpenRoot(workspace)
	if err != nil {
		return "", nil, false, err
	}
	defer workspaceRoot.Close()
	projectRoot, err := workspaceRoot.OpenRoot(projectPath)
	if err != nil {
		return "", nil, false, fmt.Errorf("open project inside workspace: %w", err)
	}
	success := false
	defer func() {
		if !success {
			projectRoot.Close()
		}
	}()
	if _, err := projectRoot.Lstat(".git"); errors.Is(err, os.ErrNotExist) {
		success = true
		return "", projectRoot, false, nil
	} else if err != nil {
		return "", nil, false, fmt.Errorf("inspect project Git metadata: %w", err)
	}
	project, err := filepath.EvalSymlinks(filepath.Join(workspace, projectPath))
	if err != nil {
		return "", nil, false, err
	}
	workspacePath, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", nil, false, err
	}
	relative, err := filepath.Rel(workspacePath, project)
	if err != nil || !filepath.IsLocal(relative) {
		return "", nil, false, fmt.Errorf("projectPath escapes workspace")
	}
	top, truncated, err := runGit(ctx, project, 8192, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", nil, false, err
	}
	resolved, err := filepath.EvalSymlinks(strings.TrimSuffix(top, "\n"))
	if err != nil || truncated || resolved != project {
		return "", nil, false, fmt.Errorf("projectPath must identify a Git worktree root inside the workspace; parent repositories are not read")
	}
	success = true
	return project, projectRoot, true, nil
}

func readChanges(ctx context.Context, workspace string, input ChangesInput) (Changes, error) {
	scope, statusArgs, diffArgs, err := changesArguments(input)
	if err != nil {
		return Changes{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	projectPath := input.ProjectPath
	if projectPath == "" {
		projectPath = "."
	}
	project, projectRoot, repository, err := openGitProject(ctx, workspace, projectPath)
	if err != nil {
		return Changes{}, err
	}
	defer projectRoot.Close()
	if !repository {
		return Changes{Status: "not_repository", ProjectPath: projectPath, Scope: scope, Files: []ChangedFile{}, Warnings: []string{}}, nil
	}
	if scope == "branch" && input.Base == "" {
		return Changes{}, fmt.Errorf("branch scope requires an explicit base ref, for example origin/main")
	}
	mergeBase := ""
	if scope == "branch" {
		base, cut, err := runGit(ctx, project, 256, "rev-parse", "--verify", "--end-of-options", input.Base+"^{commit}")
		if err != nil || cut || !commitHash.MatchString(strings.TrimSpace(base)) {
			return Changes{}, fmt.Errorf("cannot resolve base %q: %v", input.Base, err)
		}
		common, cut, err := runGit(ctx, project, 256, "merge-base", "HEAD", strings.TrimSpace(base))
		if err != nil || cut || !commitHash.MatchString(strings.TrimSpace(common)) {
			return Changes{}, fmt.Errorf("no merge-base between HEAD and %q: %v", input.Base, err)
		}
		mergeBase = strings.TrimSpace(common)
		for i, arg := range diffArgs {
			if arg == "--" {
				diffArgs = append(diffArgs[:i:i], append([]string{mergeBase}, diffArgs[i:]...)...)
				break
			}
		}
	}
	status, statusTruncated, err := runGit(ctx, project, 64<<10, statusArgs...)
	if err != nil {
		return Changes{}, err
	}
	files := parseStatus(status)
	var netBase GitGraphRevision
	if scope == "all" {
		head, err := graphHead(ctx, project)
		if err != nil {
			return Changes{}, err
		}
		if head == "" {
			head, err = graphEmptyTree(ctx, project)
			if err != nil {
				return Changes{}, err
			}
		}
		netBase = GitGraphRevision{Kind: "commit", OID: head}
		for i, arg := range diffArgs {
			if arg == "--" {
				diffArgs = append(diffArgs[:i:i], append([]string{head}, diffArgs[i:]...)...)
				break
			}
		}
		// An untracked file may replace a path deleted from the index. Its net
		// patch is produced below, excluding the index-only deletion here.
		for _, file := range files {
			if file.Index == "?" && file.Worktree == "?" {
				diffArgs = append(diffArgs, ":(top,exclude,literal)"+file.Path)
			}
		}
	}
	diff, diffTruncated, err := runGit(ctx, project, 256<<10, diffArgs...)
	if err != nil {
		return Changes{}, err
	}
	changes := Changes{Status: "ready", ProjectPath: projectPath, Scope: scope, Base: input.Base, MergeBase: mergeBase, Files: files, DiffTruncated: diffTruncated, StatusTruncated: statusTruncated, Warnings: []string{}}
	if scope == "branch" {
		namesArgs := append([]string{"diff", "--name-status", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", "--ignore-submodules=all", mergeBase}, statusArgs[len(statusArgs)-3:]...)
		names, cut, err := runGit(ctx, project, 64<<10, namesArgs...)
		if err != nil {
			return Changes{}, err
		}
		changes.StatusTruncated = changes.StatusTruncated || cut
		records := strings.Split(names, "\x00")
		fileIndices := make(map[string]int, len(changes.Files))
		for i, file := range changes.Files {
			fileIndices[file.Path] = i
		}
		for i := 0; i+2 < len(records); i += 2 {
			if index, found := fileIndices[records[i+1]]; found {
				changes.Files[index].Base = records[i]
			} else {
				fileIndices[records[i+1]] = len(changes.Files)
				changes.Files = append(changes.Files, ChangedFile{Path: records[i+1], Base: records[i]})
			}
		}
	}
	output := &boundedBuffer{limit: 256 << 10}
	output.Write([]byte(diff))
	if scope != "staged" {
		for _, file := range changes.Files {
			if ctx.Err() != nil {
				return Changes{}, ctx.Err()
			}
			if file.Index != "?" || file.Worktree != "?" {
				continue
			}
			if output.Len() == output.limit {
				changes.DiffTruncated = true
				break
			}
			var patch string
			var cut bool
			var err error
			if scope == "all" {
				var net GitGraphFileDiff
				net, err = graphUntrackedDiff(ctx, project, projectRoot, netBase, GitGraphRevision{Kind: "working-tree"}, file.Path, 3, 64<<10)
				patch, cut = net.Patch, net.Truncated
				if err == nil && net.UnavailableReason != "" {
					err = fmt.Errorf("%s", net.UnavailableReason)
				}
			} else {
				patch, cut, err = untrackedDiff(projectRoot, file.Path)
			}
			changes.DiffTruncated = changes.DiffTruncated || cut
			if err != nil {
				if len(changes.Warnings) < 100 {
					changes.Warnings = append(changes.Warnings, fmt.Sprintf("%s: %v", file.Path, err))
				}
				continue
			}
			output.Write([]byte(patch))
		}
	}
	changes.Diff = strings.ToValidUTF8(output.String(), "?")
	changes.DiffTruncated = changes.DiffTruncated || output.truncated
	return changes, nil
}

var commitHash = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func untrackedDiff(root *os.Root, path string) (string, bool, error) {
	info, err := root.Lstat(path)
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("not a regular file; symlinks and special files are not read")
	}
	file, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return "", false, err
	}
	truncated := len(data) > 64<<10
	if truncated {
		data = data[:64<<10]
	}
	header := fmt.Sprintf("diff --git %s %s\nnew file mode %06o\n", strconv.Quote("a/"+path), strconv.Quote("b/"+path), 0100000|info.Mode().Perm())
	if bytes.ContainsRune(data, 0) || !utf8.Valid(data) {
		return header + "Binary file (content omitted)\n", truncated, nil
	}
	text := string(data)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if text == "" {
		return header + "Empty file\n", truncated, nil
	}
	var patch strings.Builder
	patch.WriteString(header)
	fmt.Fprintf(&patch, "--- /dev/null\n+++ %s\n@@ -0,0 +1,%d @@\n", strconv.Quote("b/"+path), len(lines))
	for _, line := range lines {
		patch.WriteString("+" + line + "\n")
	}
	if truncated {
		patch.WriteString("[Content truncated at 64 KiB]\n")
	} else if !strings.HasSuffix(text, "\n") {
		patch.WriteString("\\ No newline at end of file\n")
	}
	return patch.String(), truncated, nil
}

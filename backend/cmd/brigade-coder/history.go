package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	graphWorktreeOID   = "__WORKTREE__"
	graphMaxPageSize   = 100
	graphMaxDiffBytes  = 262144
	graphMetadataBytes = 1 << 20
)

type GitGraphRevision struct {
	Kind string `json:"kind" jsonschema:"commit or working-tree; stash is unsupported"`
	OID  string `json:"oid,omitempty" jsonschema:"Commit object ID or ref; omit for working-tree"`
}

type GitGraphAuthor struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

type GitGraphWorktree struct {
	Staged    int `json:"staged"`
	Unstaged  int `json:"unstaged"`
	Untracked int `json:"untracked"`
}

type GitGraphCommit struct {
	OID          string            `json:"oid"`
	Parents      []string          `json:"parents"`
	Message      string            `json:"message"`
	Kind         string            `json:"kind"`
	Author       *GitGraphAuthor   `json:"author,omitempty"`
	AuthoredAt   string            `json:"authoredAt,omitempty"`
	CommittedAt  string            `json:"committedAt,omitempty"`
	ChangedFiles int               `json:"changedFiles,omitempty"`
	Worktree     *GitGraphWorktree `json:"worktree,omitempty"`
}

type GitGraphRef struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
}

type GitGraphChange struct {
	Path              string `json:"path"`
	Kind              string `json:"kind"`
	Additions         int    `json:"additions"`
	Deletions         int    `json:"deletions"`
	Binary            bool   `json:"binary,omitempty"`
	Staged            bool   `json:"staged,omitempty"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
}

type GitGraphPage struct {
	Commits        []GitGraphCommit `json:"commits"`
	Refs           []GitGraphRef    `json:"refs"`
	Head           string           `json:"head,omitempty"`
	Cursor         string           `json:"cursor,omitempty"`
	HasMore        bool             `json:"hasMore"`
	RepositoryID   string           `json:"repositoryId,omitempty"`
	RepositoryName string           `json:"repositoryName,omitempty"`
}

type GitGraphCommitDetails struct {
	Commit  GitGraphCommit   `json:"commit"`
	Refs    []GitGraphRef    `json:"refs"`
	Changes []GitGraphChange `json:"changes"`
	Body    string           `json:"body,omitempty"`
}

type GitGraphComparison struct {
	Base      GitGraphRevision `json:"base"`
	Head      GitGraphRevision `json:"head"`
	Changes   []GitGraphChange `json:"changes"`
	Additions int              `json:"additions"`
	Deletions int              `json:"deletions"`
	Truncated bool             `json:"truncated,omitempty"`
}

type GitGraphFileDiff struct {
	Base              GitGraphRevision `json:"base"`
	Head              GitGraphRevision `json:"head"`
	Path              string           `json:"path"`
	Patch             string           `json:"patch,omitempty"`
	Binary            bool             `json:"binary,omitempty"`
	Truncated         bool             `json:"truncated,omitempty"`
	UnavailableReason string           `json:"unavailableReason,omitempty"`
}

type GitHistoryInput struct {
	ProjectPath        string   `json:"projectPath,omitempty"`
	Ref                string   `json:"ref,omitempty"`
	Refs               []string `json:"refs,omitempty" jsonschema:"Union of tips; takes precedence over ref; default all branches and tags"`
	Cursor             string   `json:"cursor,omitempty"`
	Limit              int      `json:"limit,omitempty" jsonschema:"Page size 1–100; default 50"`
	IncludeWorkingTree bool     `json:"includeWorkingTree,omitempty"`
}

type GitDetailsInput struct {
	ProjectPath string           `json:"projectPath,omitempty"`
	Revision    GitGraphRevision `json:"revision"`
}

type GitCompareInput struct {
	ProjectPath string           `json:"projectPath,omitempty"`
	Base        GitGraphRevision `json:"base"`
	Head        GitGraphRevision `json:"head"`
}

type GitDiffInput struct {
	ProjectPath string            `json:"projectPath,omitempty"`
	Base        *GitGraphRevision `json:"base,omitempty" jsonschema:"Default first parent of head, or HEAD for working-tree; empty tree for root or unborn HEAD"`
	Head        GitGraphRevision  `json:"head"`
	Path        string            `json:"path" jsonschema:"Literal project-relative file path; directories and traversal are rejected"`
	Context     *int              `json:"context,omitempty" jsonschema:"Context lines 0–100; default 3"`
}

// Error text is JSON so MCP clients can preserve protocol codes from isError results.
type gitGraphError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *gitGraphError) Error() string {
	data, _ := json.Marshal(struct {
		Error *gitGraphError `json:"error"`
	}{e})
	return string(data)
}

func graphError(code, message string) error {
	return &gitGraphError{Code: code, Message: message, Retryable: code == "snapshot_expired"}
}

func (s *stateStore) gitProjectPath(path string) (string, error) {
	if path != "" {
		return path, nil
	}
	state, err := s.read()
	return state.ProjectPath, err
}

func addGitGraphTools(server *mcp.Server, store *stateStore, annotations *mcp.ToolAnnotations) {
	mcp.AddTool(server, &mcp.Tool{Name: "coder.git_history", Description: "Read topological Git history and refs. Cursors expire when the repository snapshot changes. No repository returns an empty page. Stashes are unsupported.", Annotations: annotations},
		func(ctx context.Context, _ *mcp.CallToolRequest, input GitHistoryInput) (*mcp.CallToolResult, GitGraphPage, error) {
			path, err := store.gitProjectPath(input.ProjectPath)
			if err != nil {
				return nil, GitGraphPage{}, err
			}
			input.ProjectPath = path
			page, err := readGitHistory(ctx, store.workspace, input)
			return nil, page, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "coder.git_details", Description: "Read commit or working-tree details, including staged and untracked files; no repository mutation.", Annotations: annotations},
		func(ctx context.Context, _ *mcp.CallToolRequest, input GitDetailsInput) (*mcp.CallToolResult, GitGraphCommitDetails, error) {
			path, err := store.gitProjectPath(input.ProjectPath)
			if err != nil {
				return nil, GitGraphCommitDetails{}, err
			}
			input.ProjectPath = path
			details, err := readGitDetails(ctx, store.workspace, input)
			return nil, details, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "coder.git_compare", Description: "Read changes between commits or from a commit to the current worktree. Working-tree as base and stashes are unsupported.", Annotations: annotations},
		func(ctx context.Context, _ *mcp.CallToolRequest, input GitCompareInput) (*mcp.CallToolResult, GitGraphComparison, error) {
			path, err := store.gitProjectPath(input.ProjectPath)
			if err != nil {
				return nil, GitGraphComparison{}, err
			}
			input.ProjectPath = path
			comparison, err := readGitComparison(ctx, store.workspace, input)
			return nil, comparison, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "coder.git_diff", Description: "Read a literal file's unified diff (at most 262144 bytes). Omitted base means first parent of head, or HEAD for working-tree; root/unborn defaults to empty tree. No external diff, textconv or filters.", Annotations: annotations},
		func(ctx context.Context, _ *mcp.CallToolRequest, input GitDiffInput) (*mcp.CallToolResult, GitGraphFileDiff, error) {
			path, err := store.gitProjectPath(input.ProjectPath)
			if err != nil {
				return nil, GitGraphFileDiff{}, err
			}
			input.ProjectPath = path
			diff, err := readGitDiff(ctx, store.workspace, input)
			return nil, diff, err
		})
}

func graphGit(ctx context.Context, project string, args ...string) (string, error) {
	output, truncated, err := runGit(ctx, project, graphMetadataBytes, args...)
	if err != nil {
		return "", err
	}
	if truncated {
		return "", graphError("output_limit", "Git metadata exceeds 1 MiB")
	}
	return output, nil
}

func graphValidateRef(ref string) error {
	if ref == "" || len(ref) > 1024 || strings.HasPrefix(ref, "-") || strings.ContainsFunc(ref, unicode.IsSpace) ||
		strings.ContainsFunc(ref, unicode.IsControl) || strings.ContainsAny(ref, ":\\*?[") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") || ref == graphWorktreeOID {
		return graphError("bad_request", "Expected a single commit object ID or ref")
	}
	if ref == "stash" || strings.HasPrefix(ref, "stash^") || strings.HasPrefix(ref, "stash~") || strings.HasPrefix(ref, "refs/stash") {
		return graphError("unsupported", "Stashes are unsupported")
	}
	return nil
}

func graphResolveCommit(ctx context.Context, project, ref string) (string, error) {
	if err := graphValidateRef(ref); err != nil {
		return "", err
	}
	output, err := graphGit(ctx, project, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", graphError("revision_not_found", fmt.Sprintf("Cannot resolve %q: %v", ref, err))
	}
	oid := strings.TrimSpace(output)
	if !commitHash.MatchString(oid) {
		return "", graphError("revision_not_found", "Invalid commit object ID")
	}
	return oid, nil
}

func graphHead(ctx context.Context, project string) (string, error) {
	output, err := graphGit(ctx, project, "rev-parse", "--verify", "--quiet", "--end-of-options", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	if output == "" {
		symbolic, err := graphGit(ctx, project, "symbolic-ref", "--quiet", "HEAD")
		if err != nil {
			return "", err
		}
		if symbolic == "" {
			return "", graphError("revision_not_found", "HEAD is invalid")
		}
		return "", nil
	}
	return strings.TrimSpace(output), nil
}

func graphEmptyTree(ctx context.Context, project string) (string, error) {
	// hash-object without -w computes the repository-format OID without writing objects.
	output, err := graphGit(ctx, project, "hash-object", "-t", "tree", "--stdin")
	return strings.TrimSpace(output), err
}

func graphRefs(ctx context.Context, project, head string) ([]GitGraphRef, error) {
	output, err := graphGit(ctx, project, "for-each-ref", "--sort=refname", "--format=%(refname)%00%(objectname)%00%(objecttype)%00%(*objectname)%00%(*objecttype)", "refs/heads/", "refs/remotes/", "refs/tags/")
	if err != nil {
		return nil, err
	}
	refs := []GitGraphRef{}
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\x00")
		if len(fields) != 5 {
			return nil, graphError("internal_error", "Invalid ref record")
		}
		name, oid, objectType := fields[0], fields[1], fields[2]
		if objectType == "tag" {
			oid, objectType = fields[3], fields[4]
			if objectType == "tag" {
				oid, err = graphResolveCommit(ctx, project, name)
				if err != nil {
					continue
				}
				objectType = "commit"
			}
		}
		if objectType != "commit" {
			continue
		}
		kind := "head"
		if strings.HasPrefix(name, "refs/remotes/") {
			kind = "remote"
		}
		if strings.HasPrefix(name, "refs/tags/") {
			kind = "tag"
		}
		refs = append(refs, GitGraphRef{Name: name, Target: oid, Kind: kind})
	}
	if head != "" {
		refs = append(refs, GitGraphRef{Name: "HEAD", Target: head, Kind: "current"})
	}
	return refs, nil
}

const graphLogFormat = "--format=%H%x00%P%x00%an%x00%ae%x00%aI%x00%cI%x00%B"

func graphParseCommits(output string) ([]GitGraphCommit, []string, error) {
	commits, bodies := []GitGraphCommit{}, []string{}
	if output == "" {
		return commits, bodies, nil
	}
	fields := strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")
	if len(fields)%7 != 0 {
		return nil, nil, graphError("internal_error", "Invalid Git log record")
	}
	for i := 0; i < len(fields); i += 7 {
		if !commitHash.MatchString(fields[i]) {
			return nil, nil, graphError("internal_error", "Invalid commit OID")
		}
		body := strings.TrimSuffix(fields[i+6], "\n")
		message, _, _ := strings.Cut(body, "\n")
		commits = append(commits, GitGraphCommit{OID: fields[i], Parents: strings.Fields(fields[i+1]), Kind: "commit", Message: message,
			Author: &GitGraphAuthor{Name: fields[i+2], Email: fields[i+3]}, AuthoredAt: fields[i+4], CommittedAt: fields[i+5]})
		bodies = append(bodies, body)
	}
	return commits, bodies, nil
}

func graphCommit(ctx context.Context, project, oid string) (GitGraphCommit, string, error) {
	output, err := graphGit(ctx, project, "log", "-z", "--no-decorate", "--no-show-signature", "--encoding=UTF-8", "-1", graphLogFormat, oid, "--")
	if err != nil {
		return GitGraphCommit{}, "", err
	}
	commits, bodies, err := graphParseCommits(output)
	if err != nil {
		return GitGraphCommit{}, "", err
	}
	if len(commits) != 1 {
		return GitGraphCommit{}, "", graphError("revision_not_found", "Commit not found")
	}
	return commits[0], bodies[0], nil
}

func graphWorktree(ctx context.Context, workspace, projectPath, head string) (GitGraphCommit, Changes, error) {
	changes, err := readChanges(ctx, workspace, ChangesInput{ProjectPath: projectPath})
	if err != nil {
		return GitGraphCommit{}, Changes{}, err
	}
	if changes.StatusTruncated {
		return GitGraphCommit{}, Changes{}, graphError("output_limit", "Working-tree status is truncated")
	}
	counts := &GitGraphWorktree{}
	for _, file := range changes.Files {
		if file.Index == "?" {
			counts.Untracked++
			continue
		}
		if file.Index != " " && file.Index != "" {
			counts.Staged++
		}
		if file.Worktree != " " && file.Worktree != "" {
			counts.Unstaged++
		}
	}
	parents := []string{}
	if head != "" {
		parents = append(parents, head)
	}
	return GitGraphCommit{OID: graphWorktreeOID, Parents: parents, Message: "Working tree", Kind: "working-tree", Worktree: counts, ChangedFiles: len(changes.Files)}, changes, nil
}

type graphCursor struct {
	Snapshot string `json:"snapshot"`
	Offset   int    `json:"offset"`
}

func readGitHistory(ctx context.Context, workspace string, input GitHistoryInput) (GitGraphPage, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	page := GitGraphPage{Commits: []GitGraphCommit{}, Refs: []GitGraphRef{}}
	if input.Limit == 0 {
		input.Limit = 50
	}
	if input.Limit < 1 || input.Limit > graphMaxPageSize || len(input.Refs) > 100 || len(input.Cursor) > 4096 {
		return page, graphError("bad_request", "Invalid history limit, refs or cursor")
	}
	requested := append([]string{}, input.Refs...)
	if len(requested) == 0 && input.Ref != "" {
		requested = append(requested, input.Ref)
	}
	for _, ref := range requested {
		if err := graphValidateRef(ref); err != nil {
			return page, err
		}
	}
	project, root, repository, err := openGitProject(ctx, workspace, input.ProjectPath)
	if err != nil {
		return page, err
	}
	defer root.Close()
	if !repository {
		if input.Cursor != "" {
			return page, graphError("snapshot_expired", "Repository no longer exists")
		}
		return page, nil
	}
	page.RepositoryID, page.RepositoryName = input.ProjectPath, filepath.Base(project)
	if page.RepositoryID == "" {
		page.RepositoryID = "."
	}
	page.Head, err = graphHead(ctx, project)
	if err != nil {
		return page, err
	}
	page.Refs, err = graphRefs(ctx, project, page.Head)
	if err != nil {
		return page, err
	}
	tips := []string{}
	if len(requested) == 0 {
		for _, ref := range page.Refs {
			tips = append(tips, ref.Target)
		}
	} else {
		for _, ref := range requested {
			// An unborn HEAD is a valid empty history selection.
			if ref == "HEAD" && page.Head == "" {
				continue
			}
			oid, err := graphResolveCommit(ctx, project, ref)
			if err != nil {
				return page, err
			}
			tips = append(tips, oid)
		}
	}
	sort.Strings(tips)
	tips = compactGraphTips(tips)
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	encoder.Encode([]any{project, page.Head, page.Refs, tips, requested, input.IncludeWorkingTree})
	currentBranch, err := graphGit(ctx, project, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return page, err
	}
	encoder.Encode(currentBranch)
	var worktree GitGraphCommit
	if input.IncludeWorkingTree {
		var changes Changes
		worktree, changes, err = graphWorktree(ctx, workspace, input.ProjectPath, page.Head)
		if err != nil {
			return page, err
		}
		encoder.Encode(changes)
		// Include file metadata beyond the bounded preview and index blob IDs.
		for _, file := range changes.Files {
			info, err := root.Lstat(file.Path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return page, err
			}
			encoder.Encode([]any{file.Path, info.Size(), info.Mode(), info.ModTime().UnixNano()})
		}
		index, err := graphGit(ctx, project, "ls-files", "--stage", "-z")
		if err != nil {
			return page, err
		}
		encoder.Encode(index)
	}
	snapshot := fmt.Sprintf("%x", hash.Sum(nil))
	offset := 0
	if input.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(input.Cursor)
		var cursor graphCursor
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Offset < 1 || cursor.Offset > 10000000 || cursor.Snapshot == "" {
			return page, graphError("bad_request", "Invalid history cursor")
		}
		if cursor.Snapshot != snapshot {
			return page, graphError("snapshot_expired", "Repository changed; reload history")
		}
		offset = cursor.Offset
	}
	skip := offset
	hasWorktree := input.IncludeWorkingTree && worktree.ChangedFiles > 0
	if hasWorktree {
		if offset == 0 {
			page.Commits = append(page.Commits, worktree)
		} else {
			skip--
		}
	}
	if len(tips) > 0 {
		args := []string{"log", "-z", "--topo-order", "--no-decorate", "--no-show-signature", "--encoding=UTF-8", graphLogFormat,
			fmt.Sprintf("--skip=%d", skip), fmt.Sprintf("--max-count=%d", input.Limit+1-len(page.Commits))}
		args = append(args, tips...)
		args = append(args, "--")
		output, err := graphGit(ctx, project, args...)
		if err != nil {
			return page, err
		}
		commits, _, err := graphParseCommits(output)
		if err != nil {
			return page, err
		}
		page.Commits = append(page.Commits, commits...)
	}
	if len(page.Commits) > input.Limit {
		page.HasMore = true
		page.Commits = page.Commits[:input.Limit]
		data, _ := json.Marshal(graphCursor{Snapshot: snapshot, Offset: offset + len(page.Commits)})
		page.Cursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return page, nil
}

func compactGraphTips(tips []string) []string {
	result := tips[:0]
	for _, tip := range tips {
		if len(result) == 0 || result[len(result)-1] != tip {
			result = append(result, tip)
		}
	}
	return result
}

func graphResolveRevision(ctx context.Context, project string, revision GitGraphRevision, base bool) (GitGraphRevision, error) {
	switch revision.Kind {
	case "working-tree":
		if base {
			return GitGraphRevision{}, graphError("unsupported", "Working-tree as base is unsupported")
		}
		if revision.OID != "" {
			return GitGraphRevision{}, graphError("bad_request", "Working-tree revision must omit oid")
		}
		return revision, nil
	case "commit":
		empty, err := graphEmptyTree(ctx, project)
		if err != nil {
			return GitGraphRevision{}, err
		}
		if base && revision.OID == empty {
			return revision, nil
		}
		oid, err := graphResolveCommit(ctx, project, revision.OID)
		return GitGraphRevision{Kind: "commit", OID: oid}, err
	case "stash":
		return GitGraphRevision{}, graphError("unsupported", "Stashes are unsupported")
	default:
		return GitGraphRevision{}, graphError("bad_request", "Invalid revision kind")
	}
}

func graphBase(ctx context.Context, project string, base *GitGraphRevision, head GitGraphRevision) (GitGraphRevision, error) {
	if base != nil {
		return graphResolveRevision(ctx, project, *base, true)
	}
	oid := ""
	if head.Kind == "working-tree" {
		var err error
		oid, err = graphHead(ctx, project)
		if err != nil {
			return GitGraphRevision{}, err
		}
	} else {
		commit, _, err := graphCommit(ctx, project, head.OID)
		if err != nil {
			return GitGraphRevision{}, err
		}
		if len(commit.Parents) > 0 {
			oid = commit.Parents[0]
		}
	}
	if oid == "" {
		var err error
		oid, err = graphEmptyTree(ctx, project)
		if err != nil {
			return GitGraphRevision{}, err
		}
	}
	return GitGraphRevision{Kind: "commit", OID: oid}, nil
}

func graphDiffArgs(base, head GitGraphRevision, format string, contextLines int, path string) []string {
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--ignore-submodules=all", "--submodule=short", "--src-prefix=a/", "--dst-prefix=b/", "--no-relative"}
	if format == "patch" {
		args = append(args, "--patch", fmt.Sprintf("--unified=%d", contextLines))
	} else {
		args = append(args, format, "-z")
	}
	args = append(args, base.OID)
	if head.Kind == "commit" {
		args = append(args, head.OID)
	}
	args = append(args, "--")
	if path != "" {
		args = append(args, ":(top,literal)"+path)
	} else {
		args = append(args, ".")
	}
	return append(args, ":(top,exclude).brigade/coder")
}

func graphValidatePath(path string) error {
	if path == "" || path == "." || !filepath.IsLocal(path) || len(path) > 4096 || strings.ContainsRune(path, 0) || filepath.ToSlash(filepath.Clean(path)) != path ||
		path == stateDirectory || strings.HasPrefix(path, stateDirectory+"/") || path == ".git" || strings.HasPrefix(path, ".git/") {
		return graphError("bad_request", "Expected a literal project-relative file path")
	}
	return nil
}

func readGitDetails(ctx context.Context, workspace string, input GitDetailsInput) (GitGraphCommitDetails, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	details := GitGraphCommitDetails{Refs: []GitGraphRef{}, Changes: []GitGraphChange{}}
	project, root, repository, err := openGitProject(ctx, workspace, input.ProjectPath)
	if err != nil {
		return details, err
	}
	defer root.Close()
	if !repository {
		return details, graphError("repository_not_found", "Project is not a Git repository")
	}
	revision, err := graphResolveRevision(ctx, project, input.Revision, false)
	if err != nil {
		return details, err
	}
	head, err := graphHead(ctx, project)
	if err != nil {
		return details, err
	}
	var status Changes
	if revision.Kind == "working-tree" {
		details.Commit, status, err = graphWorktree(ctx, workspace, input.ProjectPath, head)
	} else {
		details.Commit, details.Body, err = graphCommit(ctx, project, revision.OID)
	}
	if err != nil {
		return details, err
	}
	refs, err := graphRefs(ctx, project, head)
	if err != nil {
		return details, err
	}
	for _, ref := range refs {
		if ref.Target == details.Commit.OID {
			details.Refs = append(details.Refs, ref)
		}
	}
	base, err := graphBase(ctx, project, nil, revision)
	if err != nil {
		return details, err
	}
	comparison, err := graphCompare(ctx, project, root, base, revision)
	if err != nil {
		return details, err
	}
	details.Changes = comparison.Changes
	if revision.Kind == "working-tree" {
		byPath := make(map[string]int, len(details.Changes))
		for i, change := range details.Changes {
			byPath[change.Path] = i
		}
		for _, file := range status.Files {
			staged := file.Index != " " && file.Index != "?" && file.Index != ""
			if index, ok := byPath[file.Path]; ok {
				details.Changes[index].Staged = details.Changes[index].Staged || staged
			} else {
				// Index-only changes remain visible even if worktree content equals HEAD.
				byPath[file.Path] = len(details.Changes)
				details.Changes = append(details.Changes, GitGraphChange{Path: file.Path, Kind: "modify", Staged: staged})
			}
		}
		sort.Slice(details.Changes, func(i, j int) bool { return details.Changes[i].Path < details.Changes[j].Path })
	}
	details.Commit.ChangedFiles = len(details.Changes)
	return details, nil
}

func readGitComparison(ctx context.Context, workspace string, input GitCompareInput) (GitGraphComparison, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	project, root, repository, err := openGitProject(ctx, workspace, input.ProjectPath)
	if err != nil {
		return GitGraphComparison{}, err
	}
	defer root.Close()
	if !repository {
		return GitGraphComparison{}, graphError("repository_not_found", "Project is not a Git repository")
	}
	head, err := graphResolveRevision(ctx, project, input.Head, false)
	if err != nil {
		return GitGraphComparison{}, err
	}
	base, err := graphBase(ctx, project, &input.Base, head)
	if err != nil {
		return GitGraphComparison{}, err
	}
	return graphCompare(ctx, project, root, base, head)
}

func graphCompare(ctx context.Context, project string, root *os.Root, base, head GitGraphRevision) (GitGraphComparison, error) {
	result := GitGraphComparison{Base: base, Head: head, Changes: []GitGraphChange{}}
	names, err := graphGit(ctx, project, graphDiffArgs(base, head, "--name-status", 0, "")...)
	if err != nil {
		return result, err
	}
	stats, err := graphGit(ctx, project, graphDiffArgs(base, head, "--numstat", 0, "")...)
	if err != nil {
		return result, err
	}
	changes := map[string]GitGraphChange{}
	records := strings.Split(strings.TrimSuffix(names, "\x00"), "\x00")
	if names != "" && len(records)%2 != 0 {
		return result, graphError("internal_error", "Invalid diff names")
	}
	for i := 0; i+1 < len(records); i += 2 {
		kind := "modify"
		switch records[i] {
		case "A":
			kind = "add"
		case "D":
			kind = "delete"
		case "U":
			kind = "unknown"
		}
		changes[records[i+1]] = GitGraphChange{Path: records[i+1], Kind: kind}
	}
	for _, record := range strings.Split(stats, "\x00") {
		if record == "" {
			continue
		}
		fields := strings.SplitN(record, "\t", 3)
		if len(fields) != 3 {
			return result, graphError("internal_error", "Invalid diff statistics")
		}
		change := changes[fields[2]]
		change.Path = fields[2]
		change.Binary = fields[0] == "-" || fields[1] == "-"
		if !change.Binary {
			change.Additions, err = strconv.Atoi(fields[0])
			if err != nil {
				return result, err
			}
			change.Deletions, err = strconv.Atoi(fields[1])
			if err != nil {
				return result, err
			}
		}
		changes[change.Path] = change
	}
	if head.Kind == "working-tree" {
		untracked, err := graphGit(ctx, project, "ls-files", "--others", "--exclude-standard", "-z", "--", ".", ":(top,exclude).brigade/coder")
		if err != nil {
			return result, err
		}
		for _, path := range strings.Split(untracked, "\x00") {
			if path == "" {
				continue
			}
			diff, err := graphUntrackedDiff(ctx, project, root, base, head, path, 3, graphMaxDiffBytes)
			if err != nil {
				return result, err
			}
			if diff.Patch == "" && diff.UnavailableReason == "" {
				delete(changes, path)
				continue
			}
			kind := "add"
			if old, ok := changes[path]; ok && old.Kind == "delete" {
				kind = "modify"
			}
			change := GitGraphChange{Path: path, Kind: kind, Binary: diff.Binary, UnavailableReason: diff.UnavailableReason}
			inHunk := false
			for _, line := range strings.Split(diff.Patch, "\n") {
				if strings.HasPrefix(line, "@@ ") {
					inHunk = true
					continue
				}
				if inHunk && strings.HasPrefix(line, "+") {
					change.Additions++
				}
				if inHunk && strings.HasPrefix(line, "-") {
					change.Deletions++
				}
			}
			changes[path] = change
			result.Truncated = result.Truncated || diff.Truncated
		}
	}
	for _, change := range changes {
		result.Changes = append(result.Changes, change)
		result.Additions += change.Additions
		result.Deletions += change.Deletions
	}
	sort.Slice(result.Changes, func(i, j int) bool { return result.Changes[i].Path < result.Changes[j].Path })
	return result, nil
}

func readGitDiff(ctx context.Context, workspace string, input GitDiffInput) (GitGraphFileDiff, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := graphValidatePath(input.Path); err != nil {
		return GitGraphFileDiff{}, err
	}
	contextLines := 3
	if input.Context != nil {
		contextLines = *input.Context
	}
	if contextLines < 0 || contextLines > 100 {
		return GitGraphFileDiff{}, graphError("bad_request", "context must be 0–100")
	}
	project, root, repository, err := openGitProject(ctx, workspace, input.ProjectPath)
	if err != nil {
		return GitGraphFileDiff{}, err
	}
	defer root.Close()
	if !repository {
		return GitGraphFileDiff{}, graphError("repository_not_found", "Project is not a Git repository")
	}
	head, err := graphResolveRevision(ctx, project, input.Head, false)
	if err != nil {
		return GitGraphFileDiff{}, err
	}
	base, err := graphBase(ctx, project, input.Base, head)
	if err != nil {
		return GitGraphFileDiff{}, err
	}
	result := GitGraphFileDiff{Base: base, Head: head, Path: input.Path}
	if head.Kind == "working-tree" {
		info, err := root.Lstat(input.Path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return result, err
		}
		if err == nil && info.IsDir() {
			return result, graphError("bad_request", "path must identify a file")
		}
		untracked, err := graphGit(ctx, project, "ls-files", "--others", "--exclude-standard", "-z", "--", ":(top,literal)"+input.Path)
		if err != nil {
			return result, err
		}
		if untracked == input.Path+"\x00" {
			return graphUntrackedDiff(ctx, project, root, base, head, input.Path, contextLines, graphMaxDiffBytes)
		}
	}
	// A literal directory pathspec would otherwise return several files' patches.
	for _, revision := range []GitGraphRevision{base, head} {
		if revision.Kind != "commit" {
			continue
		}
		entry, err := graphGit(ctx, project, "ls-tree", "-z", revision.OID, "--", ":(literal)"+input.Path)
		if err != nil {
			return result, err
		}
		if strings.HasPrefix(entry, "040000 ") {
			return result, graphError("bad_request", "path must identify a file")
		}
	}
	patch, truncated, err := runGit(ctx, project, graphMaxDiffBytes, graphDiffArgs(base, head, "patch", contextLines, input.Path)...)
	if err != nil {
		return result, err
	}
	result.Patch = strings.ToValidUTF8(patch, "?")
	if len(result.Patch) > graphMaxDiffBytes {
		result.Patch = result.Patch[:graphMaxDiffBytes]
		truncated = true
	}
	result.Truncated = truncated
	result.Binary = strings.Contains(patch, "\nBinary files ")
	return result, nil
}

// Untracked files can replace a path deleted from the index. Compare their actual
// content against the base blob, not an addition concatenated with a deletion.
func graphUntrackedDiff(ctx context.Context, project string, root *os.Root, base, head GitGraphRevision, path string, contextLines, contentLimit int) (GitGraphFileDiff, error) {
	result := GitGraphFileDiff{Base: base, Head: head, Path: path}
	if err := graphValidatePath(path); err != nil {
		return result, err
	}
	info, err := root.Lstat(path)
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() {
		result.UnavailableReason = "Symlinks and special untracked files are not read"
		return result, nil
	}
	file, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return result, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return result, graphError("snapshot_expired", "File changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(contentLimit)+1))
	if err != nil {
		return result, err
	}
	if len(data) > contentLimit {
		result.Truncated = true
		result.UnavailableReason = fmt.Sprintf("Untracked file exceeds %d bytes", contentLimit)
		return result, nil
	}
	entry, err := graphGit(ctx, project, "ls-tree", "-z", base.OID, "--", ":(literal)"+path)
	if err != nil {
		return result, err
	}
	old := ""
	oldExecutable := false
	hasBase := entry != ""
	if hasBase {
		metadata, _, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			result.UnavailableReason = "Base path is not a regular blob"
			return result, nil
		}
		oldExecutable = fields[0] == "100755"
		var truncated bool
		old, truncated, err = runGit(ctx, project, contentLimit, "cat-file", "blob", fields[2])
		if err != nil {
			return result, err
		}
		if truncated {
			result.Truncated = true
			result.UnavailableReason = fmt.Sprintf("Base blob exceeds %d bytes", contentLimit)
			return result, nil
		}
	}
	// Temporary files are outside the repository; neither index nor object store is written.
	directory, err := os.MkdirTemp("", "brigade-git-diff-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(directory)
	for name, content := range map[string][]byte{"before": []byte(old), "after": data} {
		mode := os.FileMode(0600)
		if (name == "before" && oldExecutable) || (name == "after" && info.Mode().Perm()&0111 != 0) {
			mode = 0700
		}
		if err := os.WriteFile(filepath.Join(directory, name), content, mode); err != nil {
			return result, err
		}
	}
	before := "before"
	if !hasBase {
		before = "/dev/null"
	}
	patch, truncated, err := runGit(ctx, directory, graphMaxDiffBytes, "diff", "--no-index", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--src-prefix=a/", "--dst-prefix=b/", fmt.Sprintf("--unified=%d", contextLines), "--", before, "after")
	if err != nil {
		return result, err
	}
	// Rewrite only generated headers, never the user's content lines.
	lines := strings.Split(patch, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "@@ ") {
			break
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			lines[i] = "diff --git " + strconv.Quote("a/"+path) + " " + strconv.Quote("b/"+path)
		case line == "--- a/before":
			lines[i] = "--- " + strconv.Quote("a/"+path)
		case line == "+++ b/after":
			lines[i] = "+++ " + strconv.Quote("b/"+path)
		case strings.HasPrefix(line, "Binary files "):
			result.Binary = true
			lines[i] = "Binary files " + strconv.Quote("a/"+path) + " and " + strconv.Quote("b/"+path) + " differ"
		}
	}
	result.Patch = strings.ToValidUTF8(strings.Join(lines, "\n"), "?")
	if len(result.Patch) > graphMaxDiffBytes {
		result.Patch = result.Patch[:graphMaxDiffBytes]
		truncated = true
	}
	result.Truncated = truncated
	return result, nil
}

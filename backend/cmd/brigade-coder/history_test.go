package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func graphWrite(t *testing.T, project, path, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(project, path), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func graphRepo(t *testing.T) string {
	t.Helper()
	project := t.TempDir()
	gitFixture(t, project, "init", "-b", "main")
	return project
}

func graphTestCommit(t *testing.T, project, message string) string {
	t.Helper()
	gitFixture(t, project, "add", ".")
	gitFixture(t, project, "commit", "-m", message)
	return gitFixture(t, project, "rev-parse", "HEAD")
}

func graphAssertCode(t *testing.T, err error, code string) {
	t.Helper()
	var protocol *gitGraphError
	if !errors.As(err, &protocol) || protocol.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func TestGitHistoryNoRepositoryAndUnbornStaged(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	page, err := readGitHistory(ctx, workspace, GitHistoryInput{IncludeWorkingTree: true})
	if err != nil || len(page.Commits) != 0 || page.HasMore || page.Commits == nil || page.Refs == nil {
		t.Fatalf("no repository: %#v, %v", page, err)
	}
	gitFixture(t, workspace, "init", "-b", "main")
	page, err = readGitHistory(ctx, workspace, GitHistoryInput{Ref: "HEAD"})
	if err != nil || len(page.Commits) != 0 {
		t.Fatalf("unborn history: %#v, %v", page, err)
	}
	graphWrite(t, workspace, "staged.txt", "staged\n")
	gitFixture(t, workspace, "add", "staged.txt")
	graphWrite(t, workspace, "untracked.txt", "untracked\n")
	page, err = readGitHistory(ctx, workspace, GitHistoryInput{IncludeWorkingTree: true})
	if err != nil || len(page.Commits) != 1 {
		t.Fatalf("unborn worktree: %#v, %v", page, err)
	}
	commit := page.Commits[0]
	if commit.OID != graphWorktreeOID || len(commit.Parents) != 0 || commit.Parents == nil || commit.Worktree.Staged != 1 || commit.Worktree.Untracked != 1 {
		t.Fatalf("worktree: %#v", commit)
	}
	worktree := GitGraphRevision{Kind: "working-tree"}
	details, err := readGitDetails(ctx, workspace, GitDetailsInput{Revision: worktree})
	if err != nil || len(details.Changes) != 2 || !details.Changes[0].Staged {
		t.Fatalf("unborn details: %#v, %v", details, err)
	}
	for _, path := range []string{"staged.txt", "untracked.txt"} {
		diff, err := readGitDiff(ctx, workspace, GitDiffInput{Head: worktree, Path: path})
		if err != nil || !strings.Contains(diff.Patch, "--- /dev/null") || !strings.Contains(diff.Patch, "+"+strings.TrimSuffix(path, ".txt")) || !commitHash.MatchString(diff.Base.OID) {
			t.Fatalf("unborn %s diff: %#v, %v", path, diff, err)
		}
		comparison, err := readGitComparison(ctx, workspace, GitCompareInput{Base: diff.Base, Head: worktree})
		if err != nil || len(comparison.Changes) != 2 || comparison.Additions != 2 || comparison.Deletions != 0 {
			t.Fatalf("unborn compare: %#v, %v", comparison, err)
		}
	}
}

func TestGitHistoryTopologyRefsPaginationAndSnapshots(t *testing.T) {
	ctx := context.Background()
	project := graphRepo(t)
	graphWrite(t, project, "root.txt", "root\n")
	rootOID := graphTestCommit(t, project, "root")
	gitFixture(t, project, "tag", "-a", "v1", "-m", "version one")
	gitFixture(t, project, "checkout", "-b", "feature")
	graphWrite(t, project, "feature.txt", "feature\n")
	featureOID := graphTestCommit(t, project, "feature")
	gitFixture(t, project, "checkout", "main")
	graphWrite(t, project, "main.txt", "main\n")
	mainOID := graphTestCommit(t, project, "main change")
	gitFixture(t, project, "merge", "--no-ff", "feature", "-m", "merge")
	mergeOID := gitFixture(t, project, "rev-parse", "HEAD")
	gitFixture(t, project, "update-ref", "refs/remotes/origin/main", mainOID)
	gitFixture(t, project, "checkout", "-b", "unmerged", rootOID)
	graphWrite(t, project, "unmerged.txt", "unmerged\n")
	unmergedOID := graphTestCommit(t, project, "unmerged")
	gitFixture(t, project, "checkout", "main")
	page, err := readGitHistory(ctx, project, GitHistoryInput{Limit: 2})
	if err != nil || !page.HasMore || page.Cursor == "" || len(page.Commits) != 2 || page.Head != mergeOID {
		t.Fatalf("first page: %#v, %v", page, err)
	}
	wantRefs := []GitGraphRef{
		{Name: "refs/heads/feature", Target: featureOID, Kind: "head"},
		{Name: "refs/heads/main", Target: mergeOID, Kind: "head"},
		{Name: "refs/heads/unmerged", Target: unmergedOID, Kind: "head"},
		{Name: "refs/remotes/origin/main", Target: mainOID, Kind: "remote"},
		{Name: "refs/tags/v1", Target: rootOID, Kind: "tag"},
		{Name: "HEAD", Target: mergeOID, Kind: "current"},
	}
	if !reflect.DeepEqual(page.Refs, wantRefs) {
		t.Fatalf("refs = %#v", page.Refs)
	}
	firstCursor := page.Cursor
	all := append([]GitGraphCommit{}, page.Commits...)
	for page.HasMore {
		page, err = readGitHistory(ctx, project, GitHistoryInput{Limit: 2, Cursor: page.Cursor})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page.Commits...)
		if len(all) > 5 {
			t.Fatal("pagination repeated commits")
		}
	}
	if len(all) != 5 || page.Cursor != "" {
		t.Fatalf("history = %#v", all)
	}
	positions := map[string]int{}
	for i, commit := range all {
		if _, ok := positions[commit.OID]; ok {
			t.Fatal("duplicate commit")
		}
		positions[commit.OID] = i
	}
	for _, commit := range all {
		for _, parent := range commit.Parents {
			if positions[parent] <= positions[commit.OID] {
				t.Fatalf("parent precedes child: %#v", all)
			}
		}
	}
	filtered, err := readGitHistory(ctx, project, GitHistoryInput{Ref: "does-not-exist", Refs: []string{"main", "feature"}})
	if err != nil || len(filtered.Commits) != 4 {
		t.Fatalf("refs precedence: %#v, %v", filtered, err)
	}
	_, err = readGitHistory(ctx, project, GitHistoryInput{Ref: "feature", Cursor: firstCursor})
	graphAssertCode(t, err, "snapshot_expired")
	gitFixture(t, project, "tag", "new-tag")
	_, err = readGitHistory(ctx, project, GitHistoryInput{Limit: 2, Cursor: firstCursor})
	graphAssertCode(t, err, "snapshot_expired")
	graphWrite(t, project, "root.txt", "changed\n")
	page, err = readGitHistory(ctx, project, GitHistoryInput{IncludeWorkingTree: true, Limit: 1})
	if err != nil || len(page.Commits) != 1 || page.Commits[0].OID != graphWorktreeOID {
		t.Fatalf("worktree page: %#v, %v", page, err)
	}
	next, err := readGitHistory(ctx, project, GitHistoryInput{IncludeWorkingTree: true, Limit: 1, Cursor: page.Cursor})
	if err != nil || len(next.Commits) != 1 || next.Commits[0].Kind != "commit" {
		t.Fatalf("page after worktree: %#v, %v", next, err)
	}
	graphWrite(t, project, "root.txt", "changed again\n")
	_, err = readGitHistory(ctx, project, GitHistoryInput{IncludeWorkingTree: true, Limit: 1, Cursor: page.Cursor})
	graphAssertCode(t, err, "snapshot_expired")
}

func TestGitDetailsRootMergeAndFileDiff(t *testing.T) {
	ctx := context.Background()
	project := graphRepo(t)
	graphWrite(t, project, "file.txt", "root\n")
	rootOID := graphTestCommit(t, project, "root subject\n\nroot body")
	rootRevision := GitGraphRevision{Kind: "commit", OID: rootOID}
	details, err := readGitDetails(ctx, project, GitDetailsInput{Revision: rootRevision})
	if err != nil || details.Commit.Message != "root subject" || details.Body != "root subject\n\nroot body" || len(details.Changes) != 1 || details.Changes[0].Kind != "add" {
		t.Fatalf("root details: %#v, %v", details, err)
	}
	data, err := json.Marshal(details.Commit)
	if err != nil || !strings.Contains(string(data), `"parents":[]`) {
		t.Fatalf("root parents JSON: %s, %v", data, err)
	}
	zero := 0
	diff, err := readGitDiff(ctx, project, GitDiffInput{Head: rootRevision, Path: "file.txt", Context: &zero})
	if err != nil || !strings.Contains(diff.Patch, "+root") || !strings.Contains(diff.Patch, "--- /dev/null") || diff.Base.OID == rootOID {
		t.Fatalf("root diff: %#v, %v", diff, err)
	}
	rootEmptyBase := diff.Base
	graphWrite(t, project, "file.txt", "second\n")
	graphWrite(t, project, "other.txt", "other\n")
	secondOID := graphTestCommit(t, project, "second")
	second := GitGraphRevision{Kind: "commit", OID: secondOID}
	comparison, err := readGitComparison(ctx, project, GitCompareInput{Base: rootRevision, Head: second})
	if err != nil || len(comparison.Changes) != 2 || comparison.Additions != 2 || comparison.Deletions != 1 {
		t.Fatalf("comparison: %#v, %v", comparison, err)
	}
	diff, err = readGitDiff(ctx, project, GitDiffInput{Head: second, Path: "file.txt"})
	if err != nil || diff.Base != rootRevision || !strings.Contains(diff.Patch, "-root") || !strings.Contains(diff.Patch, "+second") || strings.Contains(diff.Patch, "other.txt") {
		t.Fatalf("file diff: %#v, %v", diff, err)
	}
	diff, err = readGitDiff(ctx, project, GitDiffInput{Base: &rootEmptyBase, Head: rootRevision, Path: "file.txt"})
	if err != nil || !strings.Contains(diff.Patch, "+root") {
		t.Fatalf("explicit empty base: %#v, %v", diff, err)
	}
	gitFixture(t, project, "checkout", "-b", "feature", rootOID)
	graphWrite(t, project, "branch.txt", "branch\n")
	branch := graphTestCommit(t, project, "branch")
	gitFixture(t, project, "checkout", "main")
	gitFixture(t, project, "merge", "--no-ff", "feature", "-m", "merge")
	merge := GitGraphRevision{Kind: "commit", OID: "HEAD"}
	details, err = readGitDetails(ctx, project, GitDetailsInput{Revision: merge})
	if err != nil || !reflect.DeepEqual(details.Commit.Parents, []string{secondOID, branch}) || len(details.Changes) != 1 || details.Changes[0].Path != "branch.txt" {
		t.Fatalf("merge details: %#v, %v", details, err)
	}
	diff, err = readGitDiff(ctx, project, GitDiffInput{Head: merge, Path: "branch.txt"})
	if err != nil || diff.Base.OID != secondOID || !strings.Contains(diff.Patch, "+branch") {
		t.Fatalf("merge diff: %#v, %v", diff, err)
	}
}

func TestGitCombinedWorktreeChangesAreNetAndReadOnly(t *testing.T) {
	ctx := context.Background()
	project := graphRepo(t)
	for _, name := range []string{"combined.txt", "restored.txt", "replaced.txt", "deleted.txt"} {
		graphWrite(t, project, name, "base\n")
	}
	head := graphTestCommit(t, project, "base")
	graphWrite(t, project, "combined.txt", "staged intermediate\n")
	graphWrite(t, project, "restored.txt", "staged intermediate\n")
	graphWrite(t, project, "staged.txt", "new staged\n")
	gitFixture(t, project, "add", ".")
	graphWrite(t, project, "combined.txt", "final worktree\n")
	graphWrite(t, project, "restored.txt", "base\n")
	gitFixture(t, project, "rm", "--cached", "replaced.txt")
	graphWrite(t, project, "replaced.txt", "replacement worktree\n")
	gitFixture(t, project, "rm", "deleted.txt")
	graphWrite(t, project, "untracked.txt", "untracked\n")
	statusBefore := gitFixture(t, project, "status", "--porcelain=v1")
	indexBefore, err := os.ReadFile(filepath.Join(project, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	worktree := GitGraphRevision{Kind: "working-tree"}
	base := GitGraphRevision{Kind: "commit", OID: head}
	details, err := readGitDetails(ctx, project, GitDetailsInput{Revision: worktree})
	if err != nil {
		t.Fatal(err)
	}
	changes := map[string]GitGraphChange{}
	for _, change := range details.Changes {
		changes[change.Path] = change
	}
	if len(changes) != 6 || !changes["combined.txt"].Staged || !changes["restored.txt"].Staged || changes["untracked.txt"].Kind != "add" || changes["deleted.txt"].Kind != "delete" {
		t.Fatalf("worktree details: %#v", details)
	}
	comparison, err := readGitComparison(ctx, project, GitCompareInput{Base: base, Head: worktree})
	if err != nil || len(comparison.Changes) != 5 || comparison.Additions != 4 || comparison.Deletions != 3 {
		t.Fatalf("net comparison: %#v, %v", comparison, err)
	}
	for _, path := range []string{"combined.txt", "replaced.txt"} {
		diff, err := readGitDiff(ctx, project, GitDiffInput{Head: worktree, Path: path})
		if err != nil || strings.Count(diff.Patch, "diff --git ") != 1 || !strings.Contains(diff.Patch, "-base") || !strings.Contains(diff.Patch, "worktree") || strings.Contains(diff.Patch, "staged intermediate") {
			t.Fatalf("net diff %s: %#v, %v", path, diff, err)
		}
	}
	diff, err := readGitDiff(ctx, project, GitDiffInput{Head: worktree, Path: "restored.txt"})
	if err != nil || diff.Patch != "" {
		t.Fatalf("restored net diff: %#v, %v", diff, err)
	}
	legacy, err := readChanges(ctx, project, ChangesInput{Scope: "all"})
	if err != nil || strings.Contains(legacy.Diff, "staged intermediate") || strings.Count(legacy.Diff, "diff --git ") != 5 || !strings.Contains(legacy.Diff, "+final worktree") {
		t.Fatalf("changes all is not net: %#v, %v", legacy, err)
	}
	indexAfter, err := os.ReadFile(filepath.Join(project, ".git", "index"))
	if err != nil || string(indexAfter) != string(indexBefore) {
		t.Fatalf("index mutated: %v", err)
	}
	if gitFixture(t, project, "status", "--porcelain=v1") != statusBefore || gitFixture(t, project, "rev-parse", "HEAD") != head {
		t.Fatal("repository mutated")
	}
}

func TestGitGraphRejectsUnsafeInputsAndUnsupportedRevisions(t *testing.T) {
	ctx := context.Background()
	project := graphRepo(t)
	graphWrite(t, project, "file.txt", "safe\n")
	head := GitGraphRevision{Kind: "commit", OID: graphTestCommit(t, project, "base")}
	for _, ref := range []string{"--all", "-c core.pager=touch", "HEAD:file.txt", "HEAD..main", "HEAD\x00", "HEAD\n", graphWorktreeOID} {
		_, err := readGitHistory(ctx, project, GitHistoryInput{Ref: ref})
		graphAssertCode(t, err, "bad_request")
		_, err = readGitDetails(ctx, project, GitDetailsInput{Revision: GitGraphRevision{Kind: "commit", OID: ref}})
		graphAssertCode(t, err, "bad_request")
	}
	for _, path := range []string{"", ".", "../file.txt", "/etc/passwd", "x/../file.txt", "file\x00.txt", ".git/config", ".brigade/coder/state.json"} {
		_, err := readGitDiff(ctx, project, GitDiffInput{Head: head, Path: path})
		graphAssertCode(t, err, "bad_request")
	}
	for _, revision := range []GitGraphRevision{{Kind: "stash", OID: "stash@{0}"}, {Kind: "working-tree"}} {
		_, err := readGitComparison(ctx, project, GitCompareInput{Base: revision, Head: head})
		graphAssertCode(t, err, "unsupported")
		_, err = readGitDiff(ctx, project, GitDiffInput{Base: &revision, Head: head, Path: "file.txt"})
		graphAssertCode(t, err, "unsupported")
	}
	_, err := readGitDetails(ctx, project, GitDetailsInput{Revision: GitGraphRevision{Kind: "stash", OID: "stash"}})
	graphAssertCode(t, err, "unsupported")
	_, err = readGitHistory(ctx, project, GitHistoryInput{Ref: "missing"})
	graphAssertCode(t, err, "revision_not_found")
	_, err = readGitHistory(ctx, project, GitHistoryInput{Cursor: "bad cursor"})
	graphAssertCode(t, err, "bad_request")
	for _, limit := range []int{-1, 101} {
		_, err := readGitHistory(ctx, project, GitHistoryInput{Limit: limit})
		graphAssertCode(t, err, "bad_request")
	}
	for _, lines := range []int{-1, 101} {
		_, err := readGitDiff(ctx, project, GitDiffInput{Head: head, Path: "file.txt", Context: &lines})
		graphAssertCode(t, err, "bad_request")
	}
	if _, err := readGitHistory(ctx, project, GitHistoryInput{ProjectPath: "../"}); err == nil {
		t.Fatal("project traversal accepted")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(project, "outside")); err != nil {
		t.Fatal(err)
	}
	if _, err := readGitHistory(ctx, project, GitHistoryInput{ProjectPath: "outside"}); err == nil {
		t.Fatal("external symlink accepted")
	}
	if err := os.Mkdir(filepath.Join(project, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	page, err := readGitHistory(ctx, project, GitHistoryInput{ProjectPath: "nested"})
	if err != nil || len(page.Commits) != 0 {
		t.Fatalf("parent repository leaked: %#v, %v", page, err)
	}
	graphWrite(t, project, ":(glob)*", "literal\n")
	graphWrite(t, project, "--output=owned", "literal option\n")
	for _, path := range []string{":(glob)*", "--output=owned"} {
		diff, err := readGitDiff(ctx, project, GitDiffInput{Head: GitGraphRevision{Kind: "working-tree"}, Path: path})
		if err != nil || strings.Count(diff.Patch, "diff --git ") != 1 || strings.Contains(diff.Patch, "+safe") {
			t.Fatalf("literal path %q: %#v, %v", path, diff, err)
		}
	}
}

func TestGitGraphBoundsBinaryAndDisablesExternalDrivers(t *testing.T) {
	ctx := context.Background()
	project := graphRepo(t)
	graphWrite(t, project, "file.txt", "before\n")
	graphTestCommit(t, project, "base")
	graphWrite(t, project, ".gitattributes", "*.txt filter=unsafe diff=unsafe\n")
	gitFixture(t, project, "config", "filter.unsafe.clean", "touch filter-ran; cat")
	gitFixture(t, project, "config", "filter.unsafe.process", "touch process-ran")
	gitFixture(t, project, "config", "filter.unsafe.required", "true")
	gitFixture(t, project, "config", "diff.unsafe.textconv", "touch textconv-ran")
	gitFixture(t, project, "config", "diff.external", "touch diff-ran")
	graphWrite(t, project, "file.txt", strings.Repeat("changed line\n", 40000))
	graphWrite(t, project, "binary", "a\x00b")
	worktree := GitGraphRevision{Kind: "working-tree"}
	diff, err := readGitDiff(ctx, project, GitDiffInput{Head: worktree, Path: "file.txt"})
	if err != nil || !diff.Truncated || len(diff.Patch) > graphMaxDiffBytes {
		t.Fatalf("bounded diff: len=%d, truncated=%t, %v", len(diff.Patch), diff.Truncated, err)
	}
	diff, err = readGitDiff(ctx, project, GitDiffInput{Head: worktree, Path: "binary"})
	if err != nil || !diff.Binary {
		t.Fatalf("binary diff: %#v, %v", diff, err)
	}
	if _, err := readGitDetails(ctx, project, GitDetailsInput{Revision: worktree}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"filter-ran", "process-ran", "textconv-ran", "diff-ran"} {
		if _, err := os.Stat(filepath.Join(project, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("external driver executed: %s", name)
		}
	}
	graphWrite(t, project, "large", strings.Repeat("x", graphMaxDiffBytes+1))
	diff, err = readGitDiff(ctx, project, GitDiffInput{Head: worktree, Path: "large"})
	if err != nil || !diff.Truncated || diff.UnavailableReason == "" {
		t.Fatalf("large untracked: %#v, %v", diff, err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(project, "link")); err != nil {
		t.Fatal(err)
	}
	diff, err = readGitDiff(ctx, project, GitDiffInput{Head: worktree, Path: "link"})
	if err != nil || diff.UnavailableReason == "" || diff.Patch != "" {
		t.Fatalf("symlink diff: %#v, %v", diff, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := readGitHistory(cancelled, project, GitHistoryInput{}); err == nil {
		t.Fatal("cancelled request succeeded")
	}
}

func TestGitGraphMCPToolsUseSelectedProjectAndOptionalBase(t *testing.T) {
	workspace := t.TempDir()
	project := filepath.Join(workspace, "repo")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, project, "init", "-b", "main")
	graphWrite(t, project, "file.txt", "root\n")
	oid := graphTestCommit(t, project, "root")
	store := testStore(t, workspace)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := store.selectProject(ctx, "repo"); err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := newServer(store, "<html></html>").Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "graph-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, tool := range listed.Tools {
		if !strings.HasPrefix(tool.Name, "coder.git_") {
			continue
		}
		count++
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Fatalf("annotations: %#v", tool)
		}
		if tool.Name == "coder.git_diff" {
			data, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var schema struct {
				Required []string `json:"required"`
			}
			if err := json.Unmarshal(data, &schema); err != nil {
				t.Fatal(err)
			}
			for _, field := range schema.Required {
				if field == "base" || field == "projectPath" {
					t.Fatalf("optional input required: %s", data)
				}
			}
		}
	}
	if count != 4 {
		t.Fatalf("graph tool count = %d", count)
	}
	for name, input := range map[string]any{
		"coder.git_history": map[string]any{},
		"coder.git_details": GitDetailsInput{Revision: GitGraphRevision{Kind: "commit", OID: oid}},
		"coder.git_compare": GitCompareInput{Base: GitGraphRevision{Kind: "commit", OID: oid}, Head: GitGraphRevision{Kind: "working-tree"}},
		"coder.git_diff":    GitDiffInput{Head: GitGraphRevision{Kind: "commit", OID: oid}, Path: "file.txt"},
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: input})
		if err != nil || result.IsError {
			t.Fatalf("%s: %#v, %v", name, result, err)
		}
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if name == "coder.git_history" && !strings.Contains(string(data), oid) {
			t.Fatalf("selected project ignored: %s", data)
		}
		if name == "coder.git_diff" && !strings.Contains(string(data), "+root") {
			t.Fatalf("root patch missing: %s", data)
		}
	}
}

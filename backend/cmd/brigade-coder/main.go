package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var version = "dev"

const appURI = "ui://coder/app.html"

func newServer(store *stateStore, html string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "brigade-coder", Version: version}, nil)
	closedWorld := false
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld}
	addGitGraphTools(server, store, readOnly)
	readState := func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, State, error) {
		state, err := store.read()
		return nil, state, err
	}
	mcp.AddTool(server, &mcp.Tool{
		Name: "coder.open", Description: "Open Coder: files with a read-only Monaco viewer, named runnable results, Git and CI. Chat belongs to the host.",
		Meta: mcp.Meta{"ui": map[string]any{"resourceUri": appURI}}, Annotations: readOnly,
	}, readState)
	mcp.AddTool(server, &mcp.Tool{
		Name: "coder.state", Description: "Read the current named results from the workspace.", Annotations: readOnly,
	}, readState)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "coder.register_result",
		Description: "Register or replace a runnable deliverable: a web application preview or an interactive/long-running terminal program. Files are already browsable in the Files tab: never register cat, head, tail or similar commands just to display generated source or documents. Never executes commands or starts servers. Do not include secrets in URLs or argv. Web URLs must be registered Brigade HTTP(S) previews, without credentials or fragment; only the id routing query is allowed. Terminal commands use argv and a workspace-relative cwd.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &closedWorld, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, result Result) (*mcp.CallToolResult, State, error) {
		state, err := store.register(ctx, result)
		return nil, state, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "coder.changes", Description: "Read bounded Git changes in projectPath (workspace-relative, default .). Scope: unstaged, staged, or branch with an explicit base ref. Branch compares merge-base to the current worktree. Untracked regular text files are included, bounded to 64 KiB each and 256 KiB total. No mutation, external diff, textconv or submodule execution.", Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input ChangesInput) (*mcp.CallToolResult, Changes, error) {
		projectPath, err := store.gitProjectPath(input.ProjectPath)
		if err != nil {
			return nil, Changes{}, err
		}
		input.ProjectPath = projectPath
		changes, err := readChanges(ctx, store.workspace, input)
		return nil, changes, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "coder.select_project", Description: "Select an existing workspace-relative directory after creating or cloning a project. The workspace itself (.) is the default and can be empty; Git is not required. This updates Git and CI/CD automatically. No scanning or commands are executed.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &closedWorld, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input struct {
		ProjectPath string `json:"projectPath" jsonschema:"Existing directory relative to workspace, for example repo or .; never an absolute path"`
	}) (*mcp.CallToolResult, State, error) {
		state, err := store.selectProject(ctx, input.ProjectPath)
		return nil, state, err
	})
	server.AddResource(&mcp.Resource{URI: appURI, Name: "Coder", MIMEType: "text/html;profile=mcp-app"},
		func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
				URI: appURI, MIMEType: "text/html;profile=mcp-app", Text: html,
				Meta: mcp.Meta{"ui": map[string]any{"csp": map[string]any{
					"connectDomains": []string{}, "resourceDomains": []string{}, "frameDomains": []string{},
				}}},
			}}}, nil
		})
	return server
}

func run(ctx context.Context) error {
	workspace := os.Getenv("BRIGADE_WORKSPACE")
	if workspace == "" {
		return fmt.Errorf("BRIGADE_WORKSPACE is required")
	}
	store, err := openStateStore(workspace)
	if err != nil {
		return err
	}
	defer store.close()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	html, err := os.ReadFile(filepath.Join(filepath.Dir(executable), "..", "ui", "mcp-app.html"))
	if err != nil {
		return fmt.Errorf("read bundled UI: %w", err)
	}
	return newServer(store, string(html)).Run(ctx, &mcp.StdioTransport{})
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

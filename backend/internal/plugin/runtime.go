package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"
	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Runtime держит отдельный MCP-сеанс для MCP Apps host. Агент подключается к тому же
// bundle своим стандартным ACP stdio-транспортом; состояние между ними хранится в workspace.
type Runtime struct{ session *protocol.ClientSession }

func StartRuntime(ctx context.Context, server acpsdk.McpServer, cwd string, extraEnv []string) (*Runtime, error) {
	if server.Stdio == nil {
		return nil, errors.New("plugin: experience MCP server must use stdio")
	}
	cmd := exec.Command(server.Stdio.Command, server.Stdio.Args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), extraEnv...)
	for _, item := range server.Stdio.Env {
		cmd.Env = append(cmd.Env, item.Name+"="+item.Value)
	}
	var stderr bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	capabilities := &protocol.ClientCapabilities{}
	capabilities.AddExtension("io.modelcontextprotocol/ui", map[string]any{
		"mimeTypes": []string{"text/html;profile=mcp-app"},
	})
	client := protocol.NewClient(
		&protocol.Implementation{Name: "Brigade", Version: "1"},
		&protocol.ClientOptions{Capabilities: capabilities},
	)
	session, err := client.Connect(ctx, &protocol.CommandTransport{Command: cmd}, nil)
	if err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return nil, fmt.Errorf("plugin: start MCP server: %w: %s", err, detail)
		}
		return nil, fmt.Errorf("plugin: start MCP server: %w", err)
	}
	return &Runtime{session: session}, nil
}

func (r *Runtime) Close() error {
	if r == nil || r.session == nil {
		return nil
	}
	return r.session.Close()
}

// Dispatch поддерживает ровно методы, которые MCP Apps AppBridge может вызвать из iframe.
func (r *Runtime) Dispatch(ctx context.Context, method string, raw []byte) ([]byte, error) {
	decode := func(dst any) error {
		if len(raw) == 0 {
			raw = []byte("{}")
		}
		return json.Unmarshal(raw, dst)
	}
	var result any
	var err error
	switch method {
	case "ping":
		return []byte("{}"), r.session.Ping(ctx, nil)
	case "tools/list":
		var params protocol.ListToolsParams
		if err := decode(&params); err != nil {
			return nil, err
		}
		result, err = r.session.ListTools(ctx, &params)
	case "tools/call":
		var params protocol.CallToolParams
		if err := decode(&params); err != nil {
			return nil, err
		}
		result, err = r.session.CallTool(ctx, &params)
	case "resources/list":
		var params protocol.ListResourcesParams
		if err := decode(&params); err != nil {
			return nil, err
		}
		result, err = r.session.ListResources(ctx, &params)
	case "resources/templates/list":
		var params protocol.ListResourceTemplatesParams
		if err := decode(&params); err != nil {
			return nil, err
		}
		result, err = r.session.ListResourceTemplates(ctx, &params)
	case "resources/read":
		var params protocol.ReadResourceParams
		if err := decode(&params); err != nil {
			return nil, err
		}
		result, err = r.session.ReadResource(ctx, &params)
	case "prompts/list":
		var params protocol.ListPromptsParams
		if err := decode(&params); err != nil {
			return nil, err
		}
		result, err = r.session.ListPrompts(ctx, &params)
	case "prompts/get":
		var params protocol.GetPromptParams
		if err := decode(&params); err != nil {
			return nil, err
		}
		result, err = r.session.GetPrompt(ctx, &params)
	default:
		return nil, fmt.Errorf("plugin: unsupported MCP method %q", method)
	}
	if err != nil {
		return nil, fmt.Errorf("plugin: MCP %s: %w", method, err)
	}
	if result == nil {
		return nil, fmt.Errorf("plugin: MCP method %q failed", method)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("plugin: encode MCP result: %w", err)
	}
	return data, nil
}

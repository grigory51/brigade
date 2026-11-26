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
	"slices"
	"strings"
	"sync"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Runtime держит отдельный MCP-сеанс для MCP Apps host. Агент подключается к тому же
// bundle своим стандартным ACP stdio-транспортом; состояние между ними хранится в workspace.
type Runtime struct {
	server   acpsdk.McpServerStdio
	cwd      string
	extraEnv []string
	ctx      context.Context
	cancel   context.CancelFunc

	mu       sync.Mutex
	closed   bool
	session  *protocol.ClientSession
	starting *runtimeStart
}

type runtimeStart struct {
	done   chan struct{}
	cancel context.CancelFunc
	err    error
}

type runtimeTransport struct {
	protocol.CommandTransport
	connection protocol.Connection
}

func (t *runtimeTransport) Connect(ctx context.Context) (protocol.Connection, error) {
	connection, err := t.CommandTransport.Connect(ctx)
	t.connection = connection
	return connection, err
}

const runtimeStartTimeout = 15 * time.Second

var errRuntimeClosed = errors.New("plugin: runtime is closed")

// NewRuntime сохраняет конфигурацию; UI-only процесс запускается первым Dispatch.
func NewRuntime(server acpsdk.McpServer, cwd string, extraEnv []string) (*Runtime, error) {
	if server.Stdio == nil {
		return nil, errors.New("plugin: experience MCP server must use stdio")
	}
	if strings.TrimSpace(server.Stdio.Command) == "" {
		return nil, errors.New("plugin: experience MCP server command is empty")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Runtime{
		server: acpsdk.McpServerStdio{
			Name: server.Stdio.Name, Command: server.Stdio.Command,
			Args: slices.Clone(server.Stdio.Args), Env: slices.Clone(server.Stdio.Env),
		},
		cwd: cwd, extraEnv: slices.Clone(extraEnv), ctx: ctx, cancel: cancel,
	}, nil
}

func (r *Runtime) connect(ctx context.Context) (*protocol.ClientSession, error) {
	// Время жизни успешно запущенного процесса не связано с запросом iframe.
	cmd := exec.Command(r.server.Command, r.server.Args...)
	cmd.Dir = r.cwd
	cmd.Env = append(os.Environ(), r.extraEnv...)
	for _, item := range r.server.Env {
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
	transport := &runtimeTransport{CommandTransport: protocol.CommandTransport{Command: cmd, TerminateDuration: time.Second}}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		// SDK не закрывает transport при несовместимой версии протокола.
		if transport.connection != nil {
			_ = transport.connection.Close()
		}
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return nil, fmt.Errorf("plugin: start MCP server: %w: %s", err, detail)
		}
		return nil, fmt.Errorf("plugin: start MCP server: %w", err)
	}
	return session, nil
}

func (r *Runtime) getSession(ctx context.Context) (*protocol.ClientSession, error) {
	if r == nil {
		return nil, errRuntimeClosed
	}
	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return nil, errRuntimeClosed
		}
		if err := ctx.Err(); err != nil {
			r.mu.Unlock()
			return nil, err
		}
		if r.session != nil {
			session := r.session
			r.mu.Unlock()
			return session, nil
		}
		if starting := r.starting; starting != nil {
			r.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-starting.done:
				if starting.err != nil {
					return nil, starting.err
				}
				continue
			}
		}
		startCtx, cancel := context.WithTimeout(ctx, runtimeStartTimeout)
		starting := &runtimeStart{done: make(chan struct{}), cancel: cancel}
		r.starting = starting
		r.mu.Unlock()

		session, err := r.connect(startCtx)
		r.mu.Lock()
		if r.closed {
			err = errRuntimeClosed
		} else if err == nil {
			err = startCtx.Err()
		}
		cancel()
		if err == nil {
			r.session = session
		}
		r.mu.Unlock()
		if err != nil && session != nil {
			_ = session.Close()
		}
		r.mu.Lock()
		starting.err = err
		r.starting = nil
		close(starting.done)
		r.mu.Unlock()
		return session, err
	}
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.closed = true
	session, starting := r.session, r.starting
	r.session = nil
	if starting != nil {
		starting.cancel()
	}
	r.mu.Unlock()
	// SDK ожидает завершения вызовов перед закрытием транспорта.
	if r.cancel != nil {
		r.cancel()
	}
	if starting != nil {
		<-starting.done
	}
	if session != nil {
		return session.Close()
	}
	return nil
}

// Dispatch поддерживает ровно методы, которые MCP Apps AppBridge может вызвать из iframe.
func (r *Runtime) Dispatch(ctx context.Context, method string, raw []byte) ([]byte, error) {
	switch method {
	case "ping", "tools/list", "tools/call", "resources/list", "resources/templates/list", "resources/read", "prompts/list", "prompts/get":
	default:
		return nil, fmt.Errorf("plugin: unsupported MCP method %q", method)
	}
	session, err := r.getSession(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(r.ctx, cancel)
	defer stop()
	defer cancel()
	decode := func(dst any) error {
		if len(raw) == 0 {
			raw = []byte("{}")
		}
		return json.Unmarshal(raw, dst)
	}
	var result any
	switch method {
	case "ping":
		return []byte("{}"), session.Ping(ctx, nil)
	case "tools/list":
		var params protocol.ListToolsParams
		if err := decode(&params); err != nil {
			return nil, err
		}
		result, err = session.ListTools(ctx, &params)
	case "tools/call":
		var params protocol.CallToolParams
		if err := decode(&params); err != nil {
			return nil, err
		}
		result, err = session.CallTool(ctx, &params)
	case "resources/list":
		var params protocol.ListResourcesParams
		if err := decode(&params); err != nil {
			return nil, err
		}
		result, err = session.ListResources(ctx, &params)
	case "resources/templates/list":
		var params protocol.ListResourceTemplatesParams
		if err := decode(&params); err != nil {
			return nil, err
		}
		result, err = session.ListResourceTemplates(ctx, &params)
	case "resources/read":
		var params protocol.ReadResourceParams
		if err := decode(&params); err != nil {
			return nil, err
		}
		result, err = session.ReadResource(ctx, &params)
	case "prompts/list":
		var params protocol.ListPromptsParams
		if err := decode(&params); err != nil {
			return nil, err
		}
		result, err = session.ListPrompts(ctx, &params)
	case "prompts/get":
		var params protocol.GetPromptParams
		if err := decode(&params); err != nil {
			return nil, err
		}
		result, err = session.GetPrompt(ctx, &params)
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

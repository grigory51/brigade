package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grigory51/brigade/backend/internal/store"
)

func TestWriteCodexConfigKeepsSecretOutOfFile(t *testing.T) {
	home := t.TempDir()
	env, err := WriteCodexConfig(home, []store.McpServer{{
		Name: "podvid", Transport: store.McpTransportStdio, Command: "podvid", Args: []string{"mcp"},
		Env: []store.McpKeyValue{{Name: "TOKEN", Value: "${secret.PODVID}"}},
	}}, map[string]string{"PODVID": "very-secret"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "very-secret") || !strings.Contains(text, "BRIGADE_SECRET_PODVID") {
		t.Fatalf("небезопасный config.toml: %s", text)
	}
	if len(env) != 1 || env[0] != "BRIGADE_SECRET_PODVID=very-secret" {
		t.Fatalf("env: %v", env)
	}
}

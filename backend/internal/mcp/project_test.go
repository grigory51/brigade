package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grigory51/brigade/backend/internal/store"
)

func TestWriteProjectConfig(t *testing.T) {
	cwd := t.TempDir()
	// Чужие ключи в settings.json (плагин brigade) должны пережить включение серверов.
	if err := os.MkdirAll(filepath.Join(cwd, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".claude", "settings.json"),
		[]byte(`{"enabledPlugins":{"brigade@brigade-x":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	env, err := WriteProjectConfig(cwd, []store.McpServer{{
		Name:      "notion",
		Transport: store.McpTransportHTTP,
		URL:       "https://mcp.notion.com/mcp",
		Headers:   []store.McpKeyValue{{Name: "Authorization", Value: "Bearer ${secret.TOKEN}"}},
	}}, map[string]string{"TOKEN": "s3cr3t"})
	if err != nil {
		t.Fatalf("WriteProjectConfig: %v", err)
	}

	if len(env) != 1 || env[0] != "BRIGADE_SECRET_TOKEN=s3cr3t" {
		t.Fatalf("окружение: %v", env)
	}
	raw, err := os.ReadFile(filepath.Join(cwd, ".mcp.json"))
	if err != nil {
		t.Fatalf("чтение .mcp.json: %v", err)
	}
	got := string(raw)
	if strings.Contains(got, "s3cr3t") {
		t.Fatalf("значение секрета попало в файл:\n%s", got)
	}
	if !strings.Contains(got, "Bearer ${BRIGADE_SECRET_TOKEN}") {
		t.Fatalf("нет ссылки на переменную окружения:\n%s", got)
	}

	settings := map[string]any{}
	data, _ := os.ReadFile(filepath.Join(cwd, ".claude", "settings.json"))
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("settings.json не парсится: %v", err)
	}
	if settings["enableAllProjectMcpServers"] != true {
		t.Fatalf("серверы проекта не одобрены: %v", settings)
	}
	if settings["enabledPlugins"] == nil {
		t.Fatalf("чужие ключи settings.json потеряны: %v", settings)
	}

	// Пустой набор убирает файл: выключенный сервер не должен оставаться в сессии.
	if _, err := WriteProjectConfig(cwd, nil, nil); err != nil {
		t.Fatalf("WriteProjectConfig (пустой набор): %v", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".mcp.json")); !os.IsNotExist(err) {
		t.Fatalf(".mcp.json должен быть удалён (err=%v)", err)
	}
}

func TestWriteProjectConfigMissingSecret(t *testing.T) {
	_, err := WriteProjectConfig(t.TempDir(), []store.McpServer{{
		Name:      "notion",
		Transport: store.McpTransportHTTP,
		URL:       "https://mcp.notion.com/mcp",
		Headers:   []store.McpKeyValue{{Name: "Authorization", Value: "${secret.GONE}"}},
	}}, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "GONE") {
		t.Fatalf("ожидалась ошибка про секрет GONE, получено: %v", err)
	}
}

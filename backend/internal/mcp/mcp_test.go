package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/grigory51/brigade/backend/internal/store"
)

func TestBuildResolvesSecrets(t *testing.T) {
	secrets := map[string]string{"NOTION_TOKEN": "s3cr3t", "OTHER": "sec2"}
	servers := []store.McpServer{
		{
			Name:      "local-db",
			Transport: store.McpTransportStdio,
			Command:   "npx",
			Args:      []string{"-y", "server-postgres"},
			Env: []store.McpKeyValue{
				{Name: "DSN", Value: "postgres://localhost/db"},
				{Name: "TOKEN", Value: "${secret.NOTION_TOKEN}"},
			},
		},
		{
			Name:      "notion",
			// Ссылка внутри строки и несколько ссылок в одном значении — типовые случаи
			// (заголовок с префиксом, строка подключения).
			Transport: store.McpTransportHTTP,
			URL:       "https://mcp.notion.com/mcp",
			Headers: []store.McpKeyValue{
				{Name: "Authorization", Value: "Bearer ${secret.NOTION_TOKEN}"},
				{Name: "X-Pair", Value: "${secret.NOTION_TOKEN}/${secret.OTHER}"},
			},
		},
		{
			Name:      "events",
			Transport: store.McpTransportSSE,
			URL:       "https://example.test/sse",
		},
	}

	built, err := Build(servers, secrets)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(built) != 3 {
		t.Fatalf("ожидалось 3 сервера, получено %d", len(built))
	}

	// Вариант транспорта кодируется полем type только при маршалинге, поэтому проверяем
	// по JSON — в этой же форме конфиг уходит демону.
	raw, err := json.Marshal(built)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		`"type":"http"`, `"type":"sse"`, `"command":"npx"`,
		`{"name":"TOKEN","value":"s3cr3t"}`,
		`{"name":"Authorization","value":"Bearer s3cr3t"}`,
		`{"name":"X-Pair","value":"s3cr3t/sec2"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("в конфиге нет %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "${secret.") {
		t.Errorf("ссылка на секрет не развёрнута:\n%s", got)
	}
}

func TestBuildMissingSecret(t *testing.T) {
	_, err := Build([]store.McpServer{{
		Name:      "notion",
		Transport: store.McpTransportHTTP,
		URL:       "https://mcp.notion.com/mcp",
		Headers:   []store.McpKeyValue{{Name: "Authorization", Value: "${secret.GONE}"}},
	}}, map[string]string{})
	if err == nil {
		t.Fatal("ожидалась ошибка на отсутствующий секрет")
	}
	if !strings.Contains(err.Error(), "GONE") {
		t.Errorf("ошибка должна называть секрет: %v", err)
	}
}

func TestValidate(t *testing.T) {
	ok := store.McpServer{Name: "notion", Transport: store.McpTransportHTTP, URL: "https://mcp.notion.com/mcp"}
	if err := Validate(ok); err != nil {
		t.Fatalf("валидный конфиг отвергнут: %v", err)
	}

	bad := []store.McpServer{
		{Name: "имя с пробелом", Transport: store.McpTransportStdio, Command: "npx"},
		{Name: "no-command", Transport: store.McpTransportStdio},
		{Name: "no-scheme", Transport: store.McpTransportHTTP, URL: "mcp.notion.com"},
		{Name: "unknown", Transport: "grpc"},
		{Name: "empty-env", Transport: store.McpTransportStdio, Command: "npx",
			Env: []store.McpKeyValue{{Name: " ", Value: "x"}}},
	}
	for _, srv := range bad {
		if err := Validate(srv); err == nil {
			t.Errorf("конфиг %q должен быть отвергнут", srv.Name)
		}
	}
}

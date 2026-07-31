package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestMcpRoundTrip проверяет разбор колонок, которые не являются простыми скалярами:
// списки конфига MCP лежат JSON-строками, а набор серверов сессии — CSV.
func TestMcpRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	srv := McpServer{
		ID:        "srv-1",
		UserID:    "u1",
		Name:      "notion",
		Transport: McpTransportHTTP,
		URL:       "https://mcp.notion.com/mcp",
		Headers:   []McpKeyValue{{Name: "Authorization", Value: "${secret.NOTION}"}},
		CreatedAt: time.Now(),
	}
	if err := st.CreateMcpServer(ctx, srv); err != nil {
		t.Fatalf("CreateMcpServer: %v", err)
	}

	got, err := st.ListMcpServers(ctx, "u1")
	if err != nil {
		t.Fatalf("ListMcpServers: %v", err)
	}
	if len(got) != 1 || got[0].Name != "notion" || got[0].Transport != McpTransportHTTP {
		t.Fatalf("неожиданный список: %+v", got)
	}
	if len(got[0].Headers) != 1 || got[0].Headers[0].Value != "${secret.NOTION}" {
		t.Fatalf("заголовки не восстановились: %+v", got[0].Headers)
	}

	// Имя сервера уникально в рамках пользователя.
	dup := srv
	dup.ID = "srv-2"
	if err := st.CreateMcpServer(ctx, dup); err == nil {
		t.Fatal("дубль имени должен отвергаться")
	}

	srv.Transport = McpTransportStdio
	srv.Command = "npx"
	srv.Args = []string{"-y", "server-everything"}
	srv.Headers = nil
	srv.Env = []McpKeyValue{{Name: "TOKEN", Value: "${secret.NOTION}"}}
	if err := st.UpdateMcpServer(ctx, srv); err != nil {
		t.Fatalf("UpdateMcpServer: %v", err)
	}
	got, _ = st.ListMcpServers(ctx, "u1")
	if len(got[0].Args) != 2 || got[0].Args[1] != "server-everything" {
		t.Fatalf("аргументы не восстановились: %+v", got[0].Args)
	}
	if len(got[0].Headers) != 0 {
		t.Fatalf("заголовки должны были очиститься: %+v", got[0].Headers)
	}

	// Набор серверов сессии переживает запись и чтение.
	sess := Session{ID: "s1", UserID: "u1", Mode: SessionModeLocal, Kind: SessionKindACP,
		AgentType: "claude-code", Status: SessionStatusRunning, CreatedAt: time.Now(),
		McpServers: []string{"srv-1", "srv-9"}, Image: "ghcr.io/me/agent:v1", AuthProfile: "chatgpt"}
	if err := st.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	read, err := st.GetSession(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if len(read.McpServers) != 2 || read.McpServers[1] != "srv-9" {
		t.Fatalf("набор MCP сессии: %+v", read.McpServers)
	}
	// Образ сессии переживает запись и чтение: по нему сессия восстанавливается после
	// рестарта brigade.
	if read.Image != "ghcr.io/me/agent:v1" {
		t.Fatalf("образ сессии: %q", read.Image)
	}
	if read.AuthProfile != "chatgpt" {
		t.Fatalf("профиль авторизации: %q", read.AuthProfile)
	}
	if err := st.UpdateSessionMcp(ctx, "s1", nil); err != nil {
		t.Fatalf("UpdateSessionMcp: %v", err)
	}
	read, _ = st.GetSession(ctx, "s1")
	if len(read.McpServers) != 0 {
		t.Fatalf("пустой набор должен читаться пустым: %+v", read.McpServers)
	}

	if err := st.DeleteMcpServer(ctx, "srv-1", "u1"); err != nil {
		t.Fatalf("DeleteMcpServer: %v", err)
	}
	if err := st.DeleteMcpServer(ctx, "srv-1", "u1"); err == nil {
		t.Fatal("повторное удаление должно давать ErrNotFound")
	}
}

// TestAgentImages: список образов пользователя хранится JSON-строкой и перезаписывается
// целиком.
func TestAgentImages(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	settings, err := st.GetUserSettings(ctx, "u1")
	if err != nil {
		t.Fatalf("GetUserSettings: %v", err)
	}
	if len(settings.AgentImages) != 0 {
		t.Fatalf("по умолчанию образов быть не должно: %+v", settings.AgentImages)
	}

	if err := st.SetAgentImages(ctx, "u1", []string{"ghcr.io/me/a:v1", "ghcr.io/me/b:v2"}); err != nil {
		t.Fatalf("SetAgentImages: %v", err)
	}
	settings, _ = st.GetUserSettings(ctx, "u1")
	if len(settings.AgentImages) != 2 || settings.AgentImages[1] != "ghcr.io/me/b:v2" {
		t.Fatalf("образы: %+v", settings.AgentImages)
	}

	if err := st.SetAgentImages(ctx, "u1", nil); err != nil {
		t.Fatalf("SetAgentImages (очистка): %v", err)
	}
	settings, _ = st.GetUserSettings(ctx, "u1")
	if len(settings.AgentImages) != 0 {
		t.Fatalf("список должен был очиститься: %+v", settings.AgentImages)
	}
}

func TestSecrets(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	if err := st.SetSecret(ctx, "u1", "NOTION", "token-1"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	// Повторная запись того же имени заменяет значение, а не заводит вторую строку.
	if err := st.SetSecret(ctx, "u1", "NOTION", "token-2"); err != nil {
		t.Fatalf("SetSecret (замена): %v", err)
	}
	names, err := st.ListSecrets(ctx, "u1")
	if err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	if len(names) != 1 || names[0].Name != "NOTION" {
		t.Fatalf("список секретов: %+v", names)
	}
	values, err := st.SecretValues(ctx, "u1")
	if err != nil {
		t.Fatalf("SecretValues: %v", err)
	}
	if values["NOTION"] != "token-2" {
		t.Fatalf("значение секрета: %q", values["NOTION"])
	}
	if err := st.DeleteSecret(ctx, "u1", "NOTION"); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	if err := st.DeleteSecret(ctx, "u1", "NOTION"); err == nil {
		t.Fatal("повторное удаление должно давать ErrNotFound")
	}
}

// openTestStore поднимает БД во временном каталоге и заводит пользователя: на него
// ссылаются mcp_servers и user_secrets.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.DB().Exec(
		`INSERT INTO users (id, username, password_hash, created_at) VALUES ('u1', 'u', 'h', 0)`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return st
}

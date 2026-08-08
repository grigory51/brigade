package store

import (
	"context"
	"testing"
)

func TestMigrateUserMovesDataToOIDCUser(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	if _, err := st.DB().Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES ('u2', 'oidc', '', 0)`); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO auth_identities (provider, subject, user_id, created_at) VALUES ('https://id.example', 'sub', 'u2', 0)`,
		`INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, created_at) VALUES ('r1', 'u1', 'h1', 9999999999, 0), ('r2', 'u2', 'h2', 9999999999, 0)`,
		`INSERT INTO sessions (id, user_id, mode, kind, agent_type, status, created_at) VALUES ('s1', 'u1', 'docker', 'acp', 'codex', 'stopped', 0)`,
		`INSERT INTO user_settings (user_id, claude_token, updated_at) VALUES ('u1', 'old', 0), ('u2', 'new', 0)`,
		`INSERT INTO user_secrets (user_id, name, value, updated_at) VALUES ('u1', 'TOKEN', 'old', 0), ('u2', 'TOKEN', 'new', 0)`,
		`INSERT INTO mcp_servers (id, user_id, name, transport, created_at) VALUES ('m1', 'u1', 'server', 'stdio', 0), ('m2', 'u2', 'server', 'stdio', 0)`,
		`INSERT INTO notification_backends (id, user_id, kind, name, created_at, updated_at) VALUES ('n1', 'u1', 'ntfy', 'main', 0, 0)`,
		`INSERT INTO telegram_bots (id, user_id, token, telegram_id, username, name, agent_type, created_at, updated_at) VALUES ('b1', 'u1', 'token', 1, 'bot', 'Bot', 'codex', 0, 0)`,
		`INSERT INTO agent_connections (id, user_id, name, agent_type, auth_profile, secret, created_at, updated_at) VALUES ('a1', 'u1', 'Codex', 'codex', 'chatgpt', 'secret', 0, 0)`,
	}
	for _, statement := range statements {
		if _, err := st.DB().Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	users, err := st.ListUsers(ctx)
	if err != nil || len(users) != 2 {
		t.Fatalf("ListUsers: %+v err=%v", users, err)
	}
	var oidcUser UserSummary
	for _, user := range users {
		if user.ID == "u2" {
			oidcUser = user
		}
	}
	if oidcUser.Providers != "https://id.example" || oidcUser.Password {
		t.Fatalf("OIDC user: %+v", oidcUser)
	}

	if err := st.MigrateUser(ctx, "u1", "u2"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"sessions", "user_settings", "user_secrets", "mcp_servers", "notification_backends", "telegram_bots", "response_profiles", "agent_connections", "auth_identities", "refresh_tokens"} {
		var count int
		if err := st.DB().QueryRow(`SELECT count(*) FROM ` + table + ` WHERE user_id = 'u1'`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s: old rows=%d err=%v", table, count, err)
		}
	}
	var oldUsers, sessions, identities, oldTokens, newTokens int
	if err := st.DB().QueryRow(`SELECT count(*) FROM users WHERE id = 'u1'`).Scan(&oldUsers); err != nil {
		t.Fatal(err)
	}
	_ = st.DB().QueryRow(`SELECT count(*) FROM sessions WHERE user_id = 'u2'`).Scan(&sessions)
	_ = st.DB().QueryRow(`SELECT count(*) FROM auth_identities WHERE user_id = 'u2'`).Scan(&identities)
	_ = st.DB().QueryRow(`SELECT count(*) FROM refresh_tokens WHERE id = 'r1'`).Scan(&oldTokens)
	_ = st.DB().QueryRow(`SELECT count(*) FROM refresh_tokens WHERE id = 'r2' AND user_id = 'u2'`).Scan(&newTokens)
	if oldUsers != 0 || sessions != 1 || identities != 1 || oldTokens != 0 || newTokens != 1 {
		t.Fatalf("users=%d sessions=%d identities=%d oldTokens=%d newTokens=%d", oldUsers, sessions, identities, oldTokens, newTokens)
	}
	var setting, secret string
	_ = st.DB().QueryRow(`SELECT claude_token FROM user_settings WHERE user_id = 'u2'`).Scan(&setting)
	_ = st.DB().QueryRow(`SELECT value FROM user_secrets WHERE user_id = 'u2' AND name = 'TOKEN'`).Scan(&secret)
	if setting != "old" || secret != "old" {
		t.Fatalf("old data did not win conflicts: setting=%q secret=%q", setting, secret)
	}
}

func TestMigrateUserRejectsTargetWithSessions(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	_, _ = st.DB().Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES ('u2', 'oidc', '', 0)`)
	_, _ = st.DB().Exec(`INSERT INTO auth_identities (provider, subject, user_id, created_at) VALUES ('https://id.example', 'sub', 'u2', 0)`)
	_, _ = st.DB().Exec(`INSERT INTO sessions (id, user_id, mode, kind, agent_type, status, created_at) VALUES ('s2', 'u2', 'docker', 'acp', 'codex', 'stopped', 0)`)
	if err := st.MigrateUser(ctx, "u1", "u2"); err == nil {
		t.Fatal("migration accepted target with sessions")
	}
}

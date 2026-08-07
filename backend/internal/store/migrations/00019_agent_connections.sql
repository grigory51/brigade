-- +goose Up
CREATE TABLE agent_connections (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  agent_type TEXT NOT NULL,
  auth_profile TEXT NOT NULL,
  secret TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX idx_agent_connections_user ON agent_connections(user_id, created_at);

-- Существующие singleton-настройки становятся обычными подключениями. Значения уже
-- зашифрованы тем же cipher, поэтому переносим их без расшифровки.
INSERT INTO agent_connections
  (id, user_id, name, agent_type, auth_profile, secret, created_at, updated_at)
SELECT 'claude-' || user_id, user_id, 'Claude Code', 'claude-code', 'claude-token',
       claude_token, updated_at, updated_at
FROM user_settings WHERE claude_token <> '';

INSERT INTO agent_connections
  (id, user_id, name, agent_type, auth_profile, secret, created_at, updated_at)
SELECT 'codex-chatgpt-' || user_id, user_id, 'Codex · ChatGPT Plus', 'codex', 'chatgpt',
       codex_auth_json, updated_at, updated_at
FROM user_settings WHERE codex_auth_json <> '';

INSERT INTO agent_connections
  (id, user_id, name, agent_type, auth_profile, secret, created_at, updated_at)
SELECT 'codex-api-' || user_id, user_id, 'Codex · API Key', 'codex', 'api-key',
       codex_api_key, updated_at, updated_at
FROM user_settings WHERE codex_api_key <> '';

-- +goose Down
DROP TABLE agent_connections;

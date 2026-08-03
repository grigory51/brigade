-- +goose Up
CREATE TABLE telegram_bots (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token TEXT NOT NULL,
  telegram_id INTEGER NOT NULL UNIQUE,
  username TEXT NOT NULL,
  name TEXT NOT NULL,
  owner_telegram_id INTEGER NOT NULL DEFAULT 0,
  owner_telegram_username TEXT NOT NULL DEFAULT '',
  agent_type TEXT NOT NULL,
  auth_profile TEXT NOT NULL DEFAULT '',
  image TEXT NOT NULL DEFAULT '',
  mcp_servers TEXT NOT NULL DEFAULT '',
  bind_token_hash TEXT NOT NULL DEFAULT '',
  bind_token_expires_at INTEGER NOT NULL DEFAULT 0,
  update_offset INTEGER NOT NULL DEFAULT 0,
  supports_guest_queries INTEGER NOT NULL DEFAULT 0,
  has_topics_enabled INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX idx_telegram_bots_user ON telegram_bots(user_id);

CREATE TABLE telegram_conversations (
  bot_id TEXT NOT NULL REFERENCES telegram_bots(id) ON DELETE CASCADE,
  scope TEXT NOT NULL,
  chat_id INTEGER NOT NULL,
  thread_id INTEGER NOT NULL DEFAULT 0,
  session_id TEXT NOT NULL,
  PRIMARY KEY (bot_id, scope, chat_id, thread_id)
);

CREATE TABLE telegram_updates (
  bot_id TEXT NOT NULL REFERENCES telegram_bots(id) ON DELETE CASCADE,
  update_id INTEGER NOT NULL,
  payload TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'queued',
  response TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (bot_id, update_id)
);
CREATE INDEX idx_telegram_updates_pending ON telegram_updates(bot_id, state, update_id);

-- +goose Down
DROP TABLE telegram_updates;
DROP TABLE telegram_conversations;
DROP TABLE telegram_bots;

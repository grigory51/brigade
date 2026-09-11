-- +goose Up
ALTER TABLE telegram_bots ADD COLUMN session_mode TEXT NOT NULL DEFAULT 'threads'
  CHECK (session_mode IN ('threads', 'chat'));
ALTER TABLE telegram_bots ADD COLUMN new_session_action TEXT NOT NULL DEFAULT 'archive'
  CHECK (new_session_action IN ('archive', 'delete'));

-- +goose Down
ALTER TABLE telegram_bots DROP COLUMN new_session_action;
ALTER TABLE telegram_bots DROP COLUMN session_mode;

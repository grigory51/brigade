-- +goose Up
ALTER TABLE telegram_bots ADD COLUMN send_delay_seconds INTEGER NOT NULL DEFAULT 5;
ALTER TABLE sessions ADD COLUMN last_read_seq INTEGER NOT NULL DEFAULT 0;
UPDATE sessions SET last_read_seq = COALESCE((SELECT MAX(seq) FROM session_messages WHERE session_id = sessions.id), 0)
  WHERE execution_policy = 'on_demand';

-- +goose Down
ALTER TABLE sessions DROP COLUMN last_read_seq;
ALTER TABLE telegram_bots DROP COLUMN send_delay_seconds;

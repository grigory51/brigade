-- +goose Up
ALTER TABLE sessions ADD COLUMN last_message TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN last_message_at INTEGER;
ALTER TABLE sessions ADD COLUMN last_message_author TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions DROP COLUMN last_message_author;
ALTER TABLE sessions DROP COLUMN last_message_at;
ALTER TABLE sessions DROP COLUMN last_message;

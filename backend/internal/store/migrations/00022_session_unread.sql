-- +goose Up
ALTER TABLE sessions ADD COLUMN unread INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE sessions DROP COLUMN unread;

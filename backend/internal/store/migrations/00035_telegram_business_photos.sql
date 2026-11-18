-- +goose Up
ALTER TABLE session_messages ADD COLUMN telegram_photo_file_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE session_messages DROP COLUMN telegram_photo_file_id;

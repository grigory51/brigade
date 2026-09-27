-- +goose Up
ALTER TABLE telegram_bots DROP COLUMN purpose;

-- +goose Down
ALTER TABLE telegram_bots ADD COLUMN purpose TEXT NOT NULL DEFAULT 'assistant';

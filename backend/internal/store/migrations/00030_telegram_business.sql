-- +goose Up
ALTER TABLE telegram_bots ADD COLUMN purpose TEXT NOT NULL DEFAULT 'assistant';
ALTER TABLE telegram_bots ADD COLUMN business_connection_id TEXT NOT NULL DEFAULT '';
ALTER TABLE telegram_bots ADD COLUMN business_owner_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE telegram_bots ADD COLUMN business_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE telegram_bots ADD COLUMN business_can_reply INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE telegram_bots DROP COLUMN business_can_reply;
ALTER TABLE telegram_bots DROP COLUMN business_enabled;
ALTER TABLE telegram_bots DROP COLUMN business_owner_id;
ALTER TABLE telegram_bots DROP COLUMN business_connection_id;
ALTER TABLE telegram_bots DROP COLUMN purpose;

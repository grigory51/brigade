-- +goose Up
ALTER TABLE telegram_updates ADD COLUMN reset_plan TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE telegram_updates DROP COLUMN reset_plan;

-- +goose Up
ALTER TABLE sessions ADD COLUMN model_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions DROP COLUMN model_id;

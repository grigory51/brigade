-- +goose Up
ALTER TABLE plugins ADD COLUMN managed INTEGER NOT NULL DEFAULT 0 CHECK (managed IN (0, 1) AND (managed = 0 OR owner_id = ''));
ALTER TABLE sessions ADD COLUMN experience_owner_id TEXT;

-- +goose Down
ALTER TABLE sessions DROP COLUMN experience_owner_id;
ALTER TABLE plugins DROP COLUMN managed;

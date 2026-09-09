-- +goose Up
ALTER TABLE image_builds ADD COLUMN name TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE image_builds DROP COLUMN name;

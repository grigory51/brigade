-- +goose Up
ALTER TABLE user_settings ADD COLUMN codex_api_key TEXT NOT NULL DEFAULT '';
ALTER TABLE user_settings ADD COLUMN codex_auth_json TEXT NOT NULL DEFAULT '';
ALTER TABLE user_settings ADD COLUMN codex_default_profile TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN auth_profile TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions DROP COLUMN auth_profile;
ALTER TABLE user_settings DROP COLUMN codex_default_profile;
ALTER TABLE user_settings DROP COLUMN codex_auth_json;
ALTER TABLE user_settings DROP COLUMN codex_api_key;

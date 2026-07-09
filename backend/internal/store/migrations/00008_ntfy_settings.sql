-- +goose Up
-- Per-user настройки push-уведомлений через персональный ntfy. Лежат в той же изолированной
-- таблице user_settings, что и токен Claude и ключ памяти. ntfy_token (право публикации в
-- топик) — секрет: шифруется приложением (internal/secret), в БД ciphertext. server/topic —
-- не секреты (адрес и имя топика), хранятся как есть. ntfy_events — CSV включённых событий
-- (напр. "turn_end,error"). Пусто = не задано / уведомления выключены.
ALTER TABLE user_settings ADD COLUMN ntfy_server TEXT NOT NULL DEFAULT '';
ALTER TABLE user_settings ADD COLUMN ntfy_topic TEXT NOT NULL DEFAULT '';
ALTER TABLE user_settings ADD COLUMN ntfy_token TEXT NOT NULL DEFAULT '';
ALTER TABLE user_settings ADD COLUMN ntfy_events TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE user_settings DROP COLUMN ntfy_server;
ALTER TABLE user_settings DROP COLUMN ntfy_topic;
ALTER TABLE user_settings DROP COLUMN ntfy_token;
ALTER TABLE user_settings DROP COLUMN ntfy_events;

-- +goose Up
-- Per-user настройки личной памяти: git-remote заметок и приватный SSH-ключ. Лежат в той
-- же изолированной таблице user_settings, что и токен Claude. Секретные значения (ssh_key,
-- а также remote — может нести токен в URL) шифруются приложением перед записью
-- (internal/secret), поэтому в БД хранится ciphertext. Пусто = не задано.
ALTER TABLE user_settings ADD COLUMN memory_remote TEXT NOT NULL DEFAULT '';
ALTER TABLE user_settings ADD COLUMN memory_ssh_key TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE user_settings DROP COLUMN memory_remote;
ALTER TABLE user_settings DROP COLUMN memory_ssh_key;

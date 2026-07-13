-- +goose Up
-- Per-user SSH-ключ агента: brigade генерирует пару, приватную часть подкладывает в контейнер
-- сессии (~/.ssh/id_ed25519), публичную пользователь добавляет в GitHub. Стабилен per-user.
-- agent_ssh_key (приватный) — секрет, шифруется приложением (internal/secret), в БД ciphertext.
-- agent_ssh_pub — публичный ключ (authorized_keys line), не секрет. Пусто = ещё не сгенерирован.
ALTER TABLE user_settings ADD COLUMN agent_ssh_key TEXT NOT NULL DEFAULT '';
ALTER TABLE user_settings ADD COLUMN agent_ssh_pub TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE user_settings DROP COLUMN agent_ssh_key;
ALTER TABLE user_settings DROP COLUMN agent_ssh_pub;

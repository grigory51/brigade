-- +goose Up
-- Отдельный SSH-ключ памяти больше не используется: доступ к git@-remote личной памяти идёт по
-- общему per-user SSH-ключу агента (user_settings.agent_ssh_key, миграция 00009). Колонка
-- memory_ssh_key (добавлена в 00007) удаляется как мёртвая.
ALTER TABLE user_settings DROP COLUMN memory_ssh_key;

-- +goose Down
ALTER TABLE user_settings ADD COLUMN memory_ssh_key TEXT NOT NULL DEFAULT '';

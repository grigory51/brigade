-- +goose Up
-- Персональные MCP-серверы пользователя и хранилище секретов (vault) для их авторизации.
--
-- user_secrets — именованные секреты: значение шифруется приложением (internal/secret), в
-- БД ciphertext, наружу не отдаётся ни одним методом API. Конфиг MCP ссылается на секрет
-- строкой "${secret.NAME}", поэтому сам конфиг секрета не содержит и его можно показывать
-- и редактировать целиком.
CREATE TABLE user_secrets (
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, name)
);

-- mcp_servers — конфиги серверов. Параметры транспорта лежат в колонках своего вида
-- (stdio: command/args/env, http|sse: url/headers); списки — JSON, поскольку читаются
-- только целиком и поиска по ним нет.
CREATE TABLE mcp_servers (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    transport    TEXT NOT NULL,              -- stdio|http|sse
    command      TEXT NOT NULL DEFAULT '',
    args_json    TEXT NOT NULL DEFAULT '[]',
    env_json     TEXT NOT NULL DEFAULT '[]',
    url          TEXT NOT NULL DEFAULT '',
    headers_json TEXT NOT NULL DEFAULT '[]',
    created_at   INTEGER NOT NULL
);

-- Имя сервера задаёт префикс имён инструментов у модели (mcp__<name>__<tool>), поэтому в
-- рамках пользователя оно должно быть уникальным.
CREATE UNIQUE INDEX idx_mcp_servers_user_name ON mcp_servers(user_id, name);

-- Набор серверов, включённых в сессии (CSV идентификаторов). Хранится на сессии, а не на
-- пользователе: в разных сессиях нужны разные инструменты, и набор должен переживать
-- рестарт brigade вместе с сессией.
ALTER TABLE sessions ADD COLUMN mcp_servers TEXT NOT NULL DEFAULT '';

-- +goose Down
DROP TABLE mcp_servers;
DROP TABLE user_secrets;
ALTER TABLE sessions DROP COLUMN mcp_servers;

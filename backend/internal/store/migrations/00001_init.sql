-- +goose Up
-- Первичная схема: пользователи и сессии агентов с полями для resume после рестарта.
CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    INTEGER NOT NULL
);

CREATE TABLE sessions (
    id               TEXT PRIMARY KEY,
    user_id          TEXT NOT NULL REFERENCES users(id),
    mode             TEXT NOT NULL,              -- local|docker
    kind             TEXT NOT NULL,              -- cli|acp
    agent_type       TEXT NOT NULL,
    agent_session_id TEXT NOT NULL DEFAULT '',   -- для `claude --resume <id>`
    container_label  TEXT NOT NULL DEFAULT '',   -- метка docker-контейнера (docker-режим)
    status           TEXT NOT NULL,              -- running|stopped|failed
    cwd              TEXT NOT NULL DEFAULT '',
    created_at       INTEGER NOT NULL
);

CREATE INDEX idx_sessions_user_id ON sessions(user_id);
CREATE INDEX idx_sessions_status ON sessions(status);

-- +goose Down
DROP TABLE sessions;
DROP TABLE users;

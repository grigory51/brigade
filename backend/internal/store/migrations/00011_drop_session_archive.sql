-- +goose Up
-- Архив сессий переехал в личную память пользователя (git-репозиторий заметок, каталог
-- archive/), см. internal/memory/archive.go. В БД от него не остаётся ничего: снимки лент
-- и флаг archived больше не читаются.
--
-- ВАЖНО: перенос данных выполняла версия v0.27.1 (одноразовый MigrateArchivesToMemory на
-- старте). Обновление на эту версию МИМО v0.27.1 потеряет прежний архив: здесь схема
-- сносится безусловно.
--
-- Колонки archived/summary снимаются пересозданием таблицы, а не ALTER TABLE DROP COLUMN:
-- у инстансов, прошедших v0.27.1, их уже нет (дроп выполнил код миграции), и ALTER там
-- упал бы, заблокировав старт. Явный список колонок в INSERT ... SELECT одинаково работает
-- в обоих случаях.
DROP INDEX IF EXISTS idx_sessions_archived;
DROP TABLE IF EXISTS session_snapshots;

CREATE TABLE sessions_new (
    id               TEXT PRIMARY KEY,
    user_id          TEXT NOT NULL REFERENCES users(id),
    mode             TEXT NOT NULL,              -- local|docker
    kind             TEXT NOT NULL,              -- cli|acp
    agent_type       TEXT NOT NULL,
    agent_session_id TEXT NOT NULL DEFAULT '',   -- для `claude --resume <id>`
    container_label  TEXT NOT NULL DEFAULT '',   -- метка docker-контейнера (docker-режим)
    status           TEXT NOT NULL,              -- running|stopped|failed
    cwd              TEXT NOT NULL DEFAULT '',
    created_at       INTEGER NOT NULL,
    name             TEXT NOT NULL DEFAULT '',
    parent_id        TEXT NOT NULL DEFAULT ''
);

INSERT INTO sessions_new (id, user_id, mode, kind, agent_type, agent_session_id,
                          container_label, status, cwd, created_at, name, parent_id)
SELECT id, user_id, mode, kind, agent_type, agent_session_id,
       container_label, status, cwd, created_at, name, parent_id
FROM sessions;

DROP TABLE sessions;
ALTER TABLE sessions_new RENAME TO sessions;

CREATE INDEX idx_sessions_user_id ON sessions(user_id);
CREATE INDEX idx_sessions_status ON sessions(status);

-- +goose Down
-- Возврат пустой схемы архива: данные обратно из памяти не поднимаются.
ALTER TABLE sessions ADD COLUMN archived INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN summary TEXT NOT NULL DEFAULT '';

CREATE TABLE session_snapshots (
  session_id   TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
  messages     TEXT NOT NULL,
  created_at   INTEGER NOT NULL
);

CREATE INDEX idx_sessions_archived ON sessions(user_id, archived);

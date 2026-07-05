-- +goose Up
-- Архив сессий: флаг archived + краткий пересказ (recap) от агента. Снимок ленты чата
-- хранится отдельной таблицей session_snapshots (одна строка на сессию, лента в JSON):
-- живая история существует только в памяти агента, для readonly-просмотра архивной
-- сессии её фиксируем в момент архивации.
ALTER TABLE sessions ADD COLUMN archived INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN summary TEXT NOT NULL DEFAULT '';

CREATE TABLE session_snapshots (
  session_id   TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
  messages     TEXT NOT NULL,
  created_at   INTEGER NOT NULL
);

CREATE INDEX idx_sessions_archived ON sessions(user_id, archived);

-- +goose Down
DROP INDEX idx_sessions_archived;
DROP TABLE session_snapshots;
ALTER TABLE sessions DROP COLUMN summary;
ALTER TABLE sessions DROP COLUMN archived;

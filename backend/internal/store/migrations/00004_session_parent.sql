-- +goose Up
-- Ветвление сессий (Fork): parent_id ссылается на сессию-родителя. Пустое значение —
-- корневая сессия.
ALTER TABLE sessions ADD COLUMN parent_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions DROP COLUMN parent_id;

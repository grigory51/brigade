-- +goose Up
-- Пользовательское имя сессии для отображения в списке. Пустое значение означает
-- производную подпись на клиенте (тип агента + вид).
ALTER TABLE sessions ADD COLUMN name TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions DROP COLUMN name;

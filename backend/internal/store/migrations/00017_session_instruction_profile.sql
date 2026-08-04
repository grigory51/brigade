-- +goose Up
-- Внутренний профиль инструкций задаёт поведение агента без добавления служебного текста
-- в пользовательскую историю. Значение сохраняется для restore/reload сессии.
ALTER TABLE sessions ADD COLUMN instruction_profile TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions DROP COLUMN instruction_profile;

-- +goose Up
-- Per-user настройки Claude: подписочный токен пользователя. Хранится в отдельной
-- таблице (не в users), чтобы значение секрета было изолировано и не попадало в
-- обычные user-выборки. Отсутствие строки трактуется как «токен не задан».
CREATE TABLE user_settings (
    user_id      TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    claude_token TEXT NOT NULL DEFAULT '',
    updated_at   INTEGER NOT NULL
);

-- +goose Down
DROP TABLE user_settings;

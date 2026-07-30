-- +goose Up
-- Образ контейнера агента: сессия может подниматься на образе пользователя (свои
-- инструменты), а не только на базовом. Компоненты brigade (демон, node, адаптер,
-- MCP-сервер) в такой контейнер приезжают read-only volume'ами, см. internal/spawn.
--
-- image на сессии: набор инструментов — свойство конкретной работы, а не пользователя;
-- колонка нужна и для восстановления сессии после рестарта brigade.
ALTER TABLE sessions ADD COLUMN image TEXT NOT NULL DEFAULT '';

-- agent_images — список образов пользователя (JSON-массив ссылок). Читается целиком,
-- поиска по элементам нет, поэтому отдельной таблицы не заводим. Секретом не является —
-- не шифруется.
ALTER TABLE user_settings ADD COLUMN agent_images TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions DROP COLUMN image;
ALTER TABLE user_settings DROP COLUMN agent_images;

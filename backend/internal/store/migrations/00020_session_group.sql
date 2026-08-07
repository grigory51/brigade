-- +goose Up
ALTER TABLE sessions ADD COLUMN group_label TEXT NOT NULL DEFAULT '';

-- Existing Telegram topics are grouped immediately after the update.
UPDATE sessions
SET group_label = (
    SELECT CASE
        WHEN telegram_bots.username = '' THEN 'Telegram'
        ELSE 'Telegram · @' || telegram_bots.username
    END
    FROM telegram_conversations
    JOIN telegram_bots ON telegram_bots.id = telegram_conversations.bot_id
    WHERE telegram_conversations.session_id = sessions.id
    LIMIT 1
)
WHERE EXISTS (
    SELECT 1 FROM telegram_conversations
    WHERE telegram_conversations.session_id = sessions.id
);

-- +goose Down
ALTER TABLE sessions DROP COLUMN group_label;

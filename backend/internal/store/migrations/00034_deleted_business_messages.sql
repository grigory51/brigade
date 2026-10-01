-- +goose Up
UPDATE session_messages
SET delivery = 'deleted', included_in_context = 0
WHERE source LIKE 'telegram-business/%'
  AND content = '[Сообщение удалено в Telegram]';

-- +goose Down
UPDATE session_messages
SET delivery = 'received'
WHERE source LIKE 'telegram-business/%'
  AND delivery = 'deleted';

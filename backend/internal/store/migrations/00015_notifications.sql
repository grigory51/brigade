-- +goose Up
CREATE TABLE notification_backends (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  name TEXT NOT NULL,
  config TEXT NOT NULL DEFAULT '{}',
  secret TEXT NOT NULL DEFAULT '',
  events TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX idx_notification_backends_user ON notification_backends(user_id);

-- Переносим прежний singleton ntfy. ntfy_token уже зашифрован тем же cipher.
INSERT INTO notification_backends
  (id, user_id, kind, name, config, secret, events, created_at, updated_at)
SELECT
  'ntfy-' || user_id, user_id, 'ntfy', 'ntfy',
  json_object('server', ntfy_server, 'topic', ntfy_topic),
  ntfy_token, ntfy_events, updated_at, updated_at
FROM user_settings
WHERE ntfy_topic <> '';

-- +goose Down
DROP TABLE notification_backends;

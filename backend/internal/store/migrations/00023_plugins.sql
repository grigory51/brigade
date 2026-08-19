-- +goose Up
CREATE TABLE plugins (
  id TEXT NOT NULL,
  name TEXT NOT NULL,
  version TEXT NOT NULL,
  bundle_path TEXT NOT NULL,
  source TEXT NOT NULL,
  manifest_json TEXT NOT NULL,
  installed_at INTEGER NOT NULL,
  active INTEGER NOT NULL DEFAULT 1,
  PRIMARY KEY (id, version)
);
CREATE INDEX idx_plugins_active ON plugins(active, name);

ALTER TABLE sessions ADD COLUMN experience_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN experience_version TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions DROP COLUMN experience_version;
ALTER TABLE sessions DROP COLUMN experience_id;
DROP TABLE plugins;

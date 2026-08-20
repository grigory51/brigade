-- +goose Up
CREATE TABLE plugins_new (
  owner_id TEXT NOT NULL DEFAULT '',
  id TEXT NOT NULL,
  name TEXT NOT NULL,
  version TEXT NOT NULL,
  target TEXT NOT NULL DEFAULT 'portable',
  bundle_path TEXT NOT NULL,
  source TEXT NOT NULL,
  manifest_json TEXT NOT NULL,
  installed_at INTEGER NOT NULL,
  active INTEGER NOT NULL DEFAULT 1,
  PRIMARY KEY (owner_id, id, version, target)
);
INSERT INTO plugins_new
  (owner_id, id, name, version, target, bundle_path, source, manifest_json, installed_at, active)
SELECT '', id, name, version,
  CASE
    WHEN source LIKE '%darwin-arm64%' THEN 'darwin-arm64'
    WHEN source LIKE '%linux-amd64%' THEN 'linux-amd64'
    ELSE 'portable'
  END,
  bundle_path, source, manifest_json, installed_at, active
FROM plugins;
DROP TABLE plugins;
ALTER TABLE plugins_new RENAME TO plugins;
CREATE INDEX idx_plugins_active ON plugins(owner_id, active, name);

CREATE TABLE plugin_configs (
  user_id TEXT NOT NULL,
  plugin_id TEXT NOT NULL,
  values_json TEXT NOT NULL DEFAULT '{}',
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, plugin_id),
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE plugin_configs;
CREATE TABLE plugins_old (
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
INSERT OR IGNORE INTO plugins_old
  (id, name, version, bundle_path, source, manifest_json, installed_at, active)
SELECT id, name, version, bundle_path, source, manifest_json, installed_at, active
FROM plugins WHERE owner_id = '';
DROP TABLE plugins;
ALTER TABLE plugins_old RENAME TO plugins;
CREATE INDEX idx_plugins_active ON plugins(active, name);

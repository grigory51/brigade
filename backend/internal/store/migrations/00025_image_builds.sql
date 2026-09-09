-- +goose Up
CREATE TABLE image_builds (
  user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  id TEXT NOT NULL,
  script TEXT NOT NULL,
  status TEXT NOT NULL,
  log TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  image TEXT NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE image_builds;

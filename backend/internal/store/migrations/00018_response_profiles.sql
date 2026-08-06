-- +goose Up
CREATE TABLE response_profiles (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL COLLATE NOCASE,
  instructions TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(user_id, name)
);
CREATE INDEX idx_response_profiles_user ON response_profiles(user_id);

ALTER TABLE sessions ADD COLUMN response_profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE sessions ADD COLUMN response_profile_name TEXT NOT NULL DEFAULT 'Обычно';
ALTER TABLE sessions ADD COLUMN response_instructions TEXT NOT NULL DEFAULT '';

INSERT INTO response_profiles (id, user_id, name, instructions, created_at, updated_at)
SELECT lower(hex(randomblob(16))), id, 'Короткий',
  'Respond directly and concisely. Include only information needed to answer. Avoid greetings, restating the request, progress or tool-use narration, and unsolicited alternatives. Preserve necessary caveats and ask before producing exceptionally long output.',
  strftime('%s','now'), strftime('%s','now') FROM users;
INSERT INTO response_profiles (id, user_id, name, instructions, created_at, updated_at)
SELECT lower(hex(randomblob(16))), id, 'Подробный',
  'Give a thorough, well-structured answer with rationale, relevant context, examples, and important edge cases when useful. Do not pad simple answers or narrate tool use.',
  strftime('%s','now'), strftime('%s','now') FROM users;

-- +goose StatementBegin
CREATE TRIGGER seed_response_profiles_after_user_insert
AFTER INSERT ON users
BEGIN
  INSERT INTO response_profiles (id, user_id, name, instructions, created_at, updated_at)
  VALUES (lower(hex(randomblob(16))), NEW.id, 'Короткий',
    'Respond directly and concisely. Include only information needed to answer. Avoid greetings, restating the request, progress or tool-use narration, and unsolicited alternatives. Preserve necessary caveats and ask before producing exceptionally long output.',
    strftime('%s','now'), strftime('%s','now'));
  INSERT INTO response_profiles (id, user_id, name, instructions, created_at, updated_at)
  VALUES (lower(hex(randomblob(16))), NEW.id, 'Подробный',
    'Give a thorough, well-structured answer with rationale, relevant context, examples, and important edge cases when useful. Do not pad simple answers or narrate tool use.',
    strftime('%s','now'), strftime('%s','now'));
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER seed_response_profiles_after_user_insert;
ALTER TABLE sessions DROP COLUMN response_instructions;
ALTER TABLE sessions DROP COLUMN response_profile_name;
ALTER TABLE sessions DROP COLUMN response_profile_id;
DROP TABLE response_profiles;

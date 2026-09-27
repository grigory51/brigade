-- +goose Up
ALTER TABLE sessions ADD COLUMN execution_policy TEXT NOT NULL DEFAULT 'persistent';
ALTER TABLE sessions ADD COLUMN history_revision INTEGER NOT NULL DEFAULT 0;

CREATE TABLE session_messages (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    author TEXT NOT NULL,
    content TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT 'brigade',
    external_id TEXT NOT NULL DEFAULT '',
    included_in_context INTEGER NOT NULL DEFAULT 1,
    delivery TEXT NOT NULL DEFAULT 'received',
    reply_to_id TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_session_messages_session ON session_messages(session_id, seq);
CREATE UNIQUE INDEX idx_session_messages_external ON session_messages(session_id, source, external_id) WHERE external_id <> '';

CREATE TABLE session_runs (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    input_revision INTEGER NOT NULL,
    draft_message_id TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    completed_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_session_runs_session ON session_runs(session_id, created_at DESC);
CREATE UNIQUE INDEX idx_session_runs_active ON session_runs(session_id) WHERE status = 'running';

-- +goose Down
DROP TABLE session_runs;
DROP TABLE session_messages;
ALTER TABLE sessions DROP COLUMN history_revision;
ALTER TABLE sessions DROP COLUMN execution_policy;

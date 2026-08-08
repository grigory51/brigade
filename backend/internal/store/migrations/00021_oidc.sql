-- +goose Up
CREATE TABLE auth_identities (
    provider   TEXT NOT NULL,
    subject    TEXT NOT NULL,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (provider, subject),
    UNIQUE (provider, user_id)
);

CREATE INDEX idx_auth_identities_user_id ON auth_identities(user_id);

-- +goose Down
DROP TABLE auth_identities;

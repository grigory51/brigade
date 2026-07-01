-- +goose Up
-- Refresh-токены: персистентное хранилище для ротации access-JWT.
-- Храним только хэш токена (sha-256 в hex), сам токен у клиента; это исключает
-- использование refresh-токена при утечке содержимого БД. Поле expires_at — в
-- unix-секундах; истёкшие токены отбраковываются при проверке и удаляются.
-- revoked фиксирует отзыв (logout, ротация при обновлении) без удаления строки.
CREATE TABLE refresh_tokens (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id),
    token_hash TEXT NOT NULL UNIQUE,
    expires_at INTEGER NOT NULL,
    revoked    INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);

CREATE INDEX idx_refresh_tokens_user_id ON refresh_tokens(user_id);
CREATE INDEX idx_refresh_tokens_expires_at ON refresh_tokens(expires_at);

-- +goose Down
DROP TABLE refresh_tokens;

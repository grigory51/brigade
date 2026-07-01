package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound возвращается методами доступа, когда строка не найдена. Позволяет
// вызывающему отличать «нет записи» от прочих ошибок БД без сравнения с sql.ErrNoRows.
var ErrNotFound = errors.New("store: not found")

// Время хранится в колонках INTEGER как Unix-секунды (UTC). Хелперы централизуют
// преобразование, чтобы формат не разъезжался между запросами.
func toUnix(t time.Time) int64    { return t.UTC().Unix() }
func fromUnix(sec int64) time.Time { return time.Unix(sec, 0).UTC() }

// --- users ---

// CreateUser вставляет нового пользователя. created_at берётся из u.CreatedAt.
func (s *Store) CreateUser(ctx context.Context, u User) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, created_at) VALUES (?, ?, ?, ?)`,
		u.ID, u.Username, u.PasswordHash, toUnix(u.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("store: create user: %w", err)
	}
	return nil
}

// GetUserByID возвращает пользователя по идентификатору либо ErrNotFound.
func (s *Store) GetUserByID(ctx context.Context, id string) (User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, created_at FROM users WHERE id = ?`, id))
}

// GetUserByUsername возвращает пользователя по логину либо ErrNotFound.
func (s *Store) GetUserByUsername(ctx context.Context, username string) (User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, created_at FROM users WHERE username = ?`, username))
}

// CountUsers возвращает число пользователей. Используется при сидировании.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count users: %w", err)
	}
	return n, nil
}

func (s *Store) scanUser(row *sql.Row) (User, error) {
	var u User
	var createdAt int64
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, fmt.Errorf("store: scan user: %w", err)
	}
	u.CreatedAt = fromUnix(createdAt)
	return u, nil
}

// --- sessions ---

// CreateSession вставляет новую сессию.
func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions
		 (id, user_id, mode, kind, agent_type, agent_session_id, container_label, status, cwd, created_at, name)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.UserID, string(sess.Mode), string(sess.Kind), sess.AgentType,
		sess.AgentSessionID, sess.ContainerLabel, string(sess.Status), sess.Cwd, toUnix(sess.CreatedAt), sess.Name,
	)
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

// GetSession возвращает сессию по идентификатору либо ErrNotFound.
func (s *Store) GetSession(ctx context.Context, id string) (Session, error) {
	return s.scanSession(s.db.QueryRowContext(ctx, sessionSelect+` WHERE id = ?`, id))
}

// ListSessionsByUser возвращает сессии пользователя, новые первыми.
func (s *Store) ListSessionsByUser(ctx context.Context, userID string) ([]Session, error) {
	return s.querySessions(ctx, sessionSelect+` WHERE user_id = ? ORDER BY created_at DESC`, userID)
}

// ListSessionsByStatus возвращает сессии в заданном статусе. Используется при
// старте бэкенда для восстановления живых (running) сессий.
func (s *Store) ListSessionsByStatus(ctx context.Context, status SessionStatus) ([]Session, error) {
	return s.querySessions(ctx, sessionSelect+` WHERE status = ? ORDER BY created_at DESC`, string(status))
}

// UpdateSessionStatus меняет статус сессии. Возвращает ErrNotFound, если сессии нет.
func (s *Store) UpdateSessionStatus(ctx context.Context, id string, status SessionStatus) error {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET status = ? WHERE id = ?`, string(status), id)
	if err != nil {
		return fmt.Errorf("store: update session status: %w", err)
	}
	return affectedOne(res, "update session status")
}

// UpdateSessionName меняет отображаемое имя сессии. Возвращает ErrNotFound, если
// сессии нет.
func (s *Store) UpdateSessionName(ctx context.Context, id, name string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return fmt.Errorf("store: update session name: %w", err)
	}
	return affectedOne(res, "update session name")
}

// UpdateSessionResume сохраняет данные для восстановления (agent_session_id для
// `claude --resume`, container_label для re-attach в docker). Заполняются после
// фактического спавна агента, когда идентификаторы становятся известны.
func (s *Store) UpdateSessionResume(ctx context.Context, id, agentSessionID, containerLabel string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET agent_session_id = ?, container_label = ? WHERE id = ?`,
		agentSessionID, containerLabel, id)
	if err != nil {
		return fmt.Errorf("store: update session resume: %w", err)
	}
	return affectedOne(res, "update session resume")
}

// DeleteSession удаляет сессию. Возвращает ErrNotFound, если сессии нет.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return affectedOne(res, "delete session")
}

const sessionSelect = `SELECT id, user_id, mode, kind, agent_type, agent_session_id,
	container_label, status, cwd, created_at, name FROM sessions`

func (s *Store) querySessions(ctx context.Context, query string, args ...any) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: query sessions: %w", err)
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		sess, err := scanSessionRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate sessions: %w", err)
	}
	return out, nil
}

// rowScanner абстрагирует *sql.Row и *sql.Rows: оба умеют Scan, что позволяет
// переиспользовать разбор строки сессии для одиночной выборки и для списка.
type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanSession(row *sql.Row) (Session, error) {
	sess, err := scanSessionRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	return sess, err
}

func scanSessionRow(r rowScanner) (Session, error) {
	var sess Session
	var mode, kind, status string
	var createdAt int64
	err := r.Scan(&sess.ID, &sess.UserID, &mode, &kind, &sess.AgentType,
		&sess.AgentSessionID, &sess.ContainerLabel, &status, &sess.Cwd, &createdAt, &sess.Name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, err
		}
		return Session{}, fmt.Errorf("store: scan session: %w", err)
	}
	sess.Mode = SessionMode(mode)
	sess.Kind = SessionKind(kind)
	sess.Status = SessionStatus(status)
	sess.CreatedAt = fromUnix(createdAt)
	return sess, nil
}

// --- refresh_tokens ---

// CreateRefreshToken сохраняет выданный refresh-токен (по его хешу).
func (s *Store) CreateRefreshToken(ctx context.Context, t RefreshToken) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, revoked, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		t.ID, t.UserID, t.TokenHash, toUnix(t.ExpiresAt), boolToInt(t.Revoked), toUnix(t.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("store: create refresh token: %w", err)
	}
	return nil
}

// GetRefreshTokenByHash возвращает refresh-токен по хешу предъявленного секрета
// либо ErrNotFound. Проверку срока и отзыва выполняет вызывающий (auth).
func (s *Store) GetRefreshTokenByHash(ctx context.Context, hash string) (RefreshToken, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, token_hash, expires_at, revoked, created_at
		 FROM refresh_tokens WHERE token_hash = ?`, hash)
	var t RefreshToken
	var expiresAt, createdAt int64
	var revoked int
	if err := row.Scan(&t.ID, &t.UserID, &t.TokenHash, &expiresAt, &revoked, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RefreshToken{}, ErrNotFound
		}
		return RefreshToken{}, fmt.Errorf("store: scan refresh token: %w", err)
	}
	t.ExpiresAt = fromUnix(expiresAt)
	t.Revoked = revoked != 0
	t.CreatedAt = fromUnix(createdAt)
	return t, nil
}

// RevokeRefreshToken помечает токен отозванным (logout, ротация при обновлении).
func (s *Store) RevokeRefreshToken(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE refresh_tokens SET revoked = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: revoke refresh token: %w", err)
	}
	return affectedOne(res, "revoke refresh token")
}

// RevokeUserRefreshTokens отзывает все токены пользователя (logout со всех устройств).
func (s *Store) RevokeUserRefreshTokens(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE refresh_tokens SET revoked = 1 WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("store: revoke user refresh tokens: %w", err)
	}
	return nil
}

// DeleteExpiredRefreshTokens удаляет токены с истёкшим сроком (фоновая чистка).
func (s *Store) DeleteExpiredRefreshTokens(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE expires_at < ?`, toUnix(now))
	if err != nil {
		return 0, fmt.Errorf("store: delete expired refresh tokens: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// --- helpers ---

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// affectedOne приводит «0 затронутых строк» к ErrNotFound для UPDATE/DELETE по id.
func affectedOne(res sql.Result, op string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: %s: rows affected: %w", op, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

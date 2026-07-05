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
func toUnix(t time.Time) int64     { return t.UTC().Unix() }
func fromUnix(sec int64) time.Time { return time.Unix(sec, 0).UTC() }

// --- users ---

// GetUserByID возвращает пользователя по идентификатору либо ErrNotFound.
func (s *Store) GetUserByID(ctx context.Context, id string) (User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, created_at FROM users WHERE id = ?`, id))
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

// GetUserSettings возвращает настройки пользователя. Отсутствие строки — не ошибка:
// возвращаются дефолтные настройки (пустой токен).
func (s *Store) GetUserSettings(ctx context.Context, userID string) (UserSettings, error) {
	settings := UserSettings{UserID: userID}
	var updatedAt int64
	err := s.db.QueryRowContext(ctx,
		`SELECT claude_token, updated_at FROM user_settings WHERE user_id = ?`, userID).
		Scan(&settings.ClaudeToken, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return UserSettings{}, fmt.Errorf("store: get user settings: %w", err)
	}
	// updated_at сканируется, но не хранится в модели (никто не читает).
	_ = updatedAt
	return settings, nil
}

// --- sessions ---

// CreateSession вставляет новую сессию.
func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions
		 (id, user_id, mode, kind, agent_type, agent_session_id, container_label, status, cwd, created_at, name, parent_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.UserID, string(sess.Mode), string(sess.Kind), sess.AgentType,
		sess.AgentSessionID, sess.ContainerLabel, string(sess.Status), sess.Cwd, toUnix(sess.CreatedAt), sess.Name, sess.ParentID,
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

// ListSessionsByUser возвращает НЕархивные сессии пользователя, новые первыми. Архивные
// исключены — они живут на отдельной странице (см. ListArchivedByUser).
func (s *Store) ListSessionsByUser(ctx context.Context, userID string) ([]Session, error) {
	return s.querySessions(ctx, sessionSelect+` WHERE user_id = ? AND archived = 0 ORDER BY created_at DESC`, userID)
}

// ListArchivedByUser возвращает архивные сессии пользователя, новые первыми.
func (s *Store) ListArchivedByUser(ctx context.Context, userID string) ([]Session, error) {
	return s.querySessions(ctx, sessionSelect+` WHERE user_id = ? AND archived = 1 ORDER BY created_at DESC`, userID)
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

// SetSessionArchived помечает сессию архивной и записывает summary (recap от агента).
// Возвращает ErrNotFound, если сессии нет.
func (s *Store) SetSessionArchived(ctx context.Context, id, summary string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET archived = 1, summary = ? WHERE id = ?`, summary, id)
	if err != nil {
		return fmt.Errorf("store: set session archived: %w", err)
	}
	return affectedOne(res, "set session archived")
}

// SaveSessionSnapshot сохраняет снимок ленты чата (JSON) для readonly-просмотра архива.
// Идемпотентно перезаписывает существующий снимок сессии.
func (s *Store) SaveSessionSnapshot(ctx context.Context, sessionID, messagesJSON string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO session_snapshots (session_id, messages, created_at) VALUES (?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET messages = excluded.messages, created_at = excluded.created_at`,
		sessionID, messagesJSON, toUnix(at))
	if err != nil {
		return fmt.Errorf("store: save session snapshot: %w", err)
	}
	return nil
}

// GetSessionSnapshot возвращает снимок ленты (JSON) архивной сессии либо ErrNotFound.
func (s *Store) GetSessionSnapshot(ctx context.Context, sessionID string) (string, error) {
	var messages string
	err := s.db.QueryRowContext(ctx, `SELECT messages FROM session_snapshots WHERE session_id = ?`, sessionID).Scan(&messages)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: get session snapshot: %w", err)
	}
	return messages, nil
}

const sessionSelect = `SELECT id, user_id, mode, kind, agent_type, agent_session_id,
	container_label, status, cwd, created_at, name, parent_id, archived, summary FROM sessions`

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
	var archived int
	err := r.Scan(&sess.ID, &sess.UserID, &mode, &kind, &sess.AgentType,
		&sess.AgentSessionID, &sess.ContainerLabel, &status, &sess.Cwd, &createdAt, &sess.Name, &sess.ParentID,
		&archived, &sess.Summary)
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
	sess.Archived = archived != 0
	return sess, nil
}

// --- helpers ---

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

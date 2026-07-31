package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	var images string
	err := s.db.QueryRowContext(ctx,
		`SELECT claude_token, codex_api_key, codex_auth_json, codex_default_profile,
		 memory_remote, ntfy_server, ntfy_topic, ntfy_token, ntfy_events, agent_images, updated_at
		 FROM user_settings WHERE user_id = ?`, userID).
		Scan(&settings.ClaudeToken, &settings.CodexAPIKey, &settings.CodexAuthJSON, &settings.CodexDefaultProfile, &settings.MemoryRemote,
			&settings.NtfyServer, &settings.NtfyTopic, &settings.NtfyToken, &settings.NtfyEvents, &images, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return UserSettings{}, fmt.Errorf("store: get user settings: %w", err)
	}
	if images != "" {
		if err := decodeJSON(images, &settings.AgentImages); err != nil {
			return UserSettings{}, err
		}
	}
	// Секретные колонки хранятся зашифрованными — отдаём наружу расшифрованными.
	settings.ClaudeToken = s.cipher.Decrypt(settings.ClaudeToken)
	settings.CodexAPIKey = s.cipher.Decrypt(settings.CodexAPIKey)
	settings.CodexAuthJSON = s.cipher.Decrypt(settings.CodexAuthJSON)
	settings.MemoryRemote = s.cipher.Decrypt(settings.MemoryRemote)
	settings.NtfyToken = s.cipher.Decrypt(settings.NtfyToken)
	// updated_at сканируется, но не хранится в модели (никто не читает).
	_ = updatedAt
	return settings, nil
}

// --- sessions ---

// CreateSession вставляет новую сессию.
func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions
		 (id, user_id, mode, kind, agent_type, agent_session_id, container_label, status, cwd, created_at, name, parent_id, mcp_servers, image, auth_profile)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.UserID, string(sess.Mode), string(sess.Kind), sess.AgentType,
		sess.AgentSessionID, sess.ContainerLabel, string(sess.Status), sess.Cwd, toUnix(sess.CreatedAt), sess.Name, sess.ParentID,
		strings.Join(sess.McpServers, ","), sess.Image, sess.AuthProfile,
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

// ListSessionsByUser возвращает сессии пользователя, новые первыми. Архивных здесь нет
// по построению: архивация переносит сессию в личную память и удаляет строку из БД.
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
	container_label, status, cwd, created_at, name, parent_id, mcp_servers, image, auth_profile FROM sessions`

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
	var mode, kind, status, mcp string
	var createdAt int64
	err := r.Scan(&sess.ID, &sess.UserID, &mode, &kind, &sess.AgentType,
		&sess.AgentSessionID, &sess.ContainerLabel, &status, &sess.Cwd, &createdAt, &sess.Name, &sess.ParentID, &mcp, &sess.Image, &sess.AuthProfile)
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
	if mcp != "" {
		sess.McpServers = strings.Split(mcp, ",")
	}
	return sess, nil
}

// SetAgentImages сохраняет список образов агента пользователя. Строка создаётся, если
// настроек ещё не было.
func (s *Store) SetAgentImages(ctx context.Context, userID string, images []string) error {
	raw, err := json.Marshal(orEmpty(images))
	if err != nil {
		return fmt.Errorf("store: encode agent images: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO user_settings (user_id, agent_images, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET agent_images = excluded.agent_images, updated_at = excluded.updated_at`,
		userID, string(raw), toUnix(time.Now()))
	if err != nil {
		return fmt.Errorf("store: set agent images: %w", err)
	}
	return nil
}

// SetCodexAuthJSON сохраняет обновлённый официальный auth.json после завершения Codex.
// Метод нужен runtime-слою: refresh-токены могут ротироваться самим Codex во время сессии.
func (s *Store) SetCodexAuthJSON(ctx context.Context, userID, authJSON string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_settings (user_id, codex_auth_json, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET codex_auth_json = excluded.codex_auth_json, updated_at = excluded.updated_at`,
		userID, s.cipher.Encrypt(authJSON), toUnix(time.Now()))
	if err != nil {
		return fmt.Errorf("store: set codex auth: %w", err)
	}
	return nil
}

// UpdateSessionMcp сохраняет набор включённых MCP-серверов сессии.
func (s *Store) UpdateSessionMcp(ctx context.Context, id string, serverIDs []string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET mcp_servers = ? WHERE id = ?`,
		strings.Join(serverIDs, ","), id)
	if err != nil {
		return fmt.Errorf("store: update session mcp: %w", err)
	}
	return affectedOne(res, "update session mcp")
}

// --- mcp_servers ---

// ListMcpServers возвращает MCP-серверы пользователя в порядке имени.
func (s *Store) ListMcpServers(ctx context.Context, userID string) ([]McpServer, error) {
	rows, err := s.db.QueryContext(ctx, mcpServerSelect+` WHERE user_id = ? ORDER BY name`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list mcp servers: %w", err)
	}
	defer rows.Close()

	var out []McpServer
	for rows.Next() {
		var srv McpServer
		var transport, argsJSON, envJSON, headersJSON string
		var createdAt int64
		if err := rows.Scan(&srv.ID, &srv.UserID, &srv.Name, &transport, &srv.Command,
			&argsJSON, &envJSON, &srv.URL, &headersJSON, &createdAt); err != nil {
			return nil, fmt.Errorf("store: scan mcp server: %w", err)
		}
		srv.Transport = McpTransport(transport)
		srv.CreatedAt = fromUnix(createdAt)
		if err := decodeJSON(argsJSON, &srv.Args); err != nil {
			return nil, err
		}
		if err := decodeJSON(envJSON, &srv.Env); err != nil {
			return nil, err
		}
		if err := decodeJSON(headersJSON, &srv.Headers); err != nil {
			return nil, err
		}
		out = append(out, srv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate mcp servers: %w", err)
	}
	return out, nil
}

// CreateMcpServer вставляет новый сервер. Конфликт по имени (уникальный индекс) вернётся
// ошибкой БД — вызывающий переводит её в понятное сообщение.
func (s *Store) CreateMcpServer(ctx context.Context, srv McpServer) error {
	args, env, headers, err := encodeMcpLists(srv)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO mcp_servers (id, user_id, name, transport, command, args_json, env_json, url, headers_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		srv.ID, srv.UserID, srv.Name, string(srv.Transport), srv.Command, args, env, srv.URL, headers, toUnix(srv.CreatedAt))
	if err != nil {
		return fmt.Errorf("store: create mcp server: %w", err)
	}
	return nil
}

// UpdateMcpServer перезаписывает конфиг сервера пользователя целиком.
func (s *Store) UpdateMcpServer(ctx context.Context, srv McpServer) error {
	args, env, headers, err := encodeMcpLists(srv)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE mcp_servers SET name = ?, transport = ?, command = ?, args_json = ?, env_json = ?,
		 url = ?, headers_json = ? WHERE id = ? AND user_id = ?`,
		srv.Name, string(srv.Transport), srv.Command, args, env, srv.URL, headers, srv.ID, srv.UserID)
	if err != nil {
		return fmt.Errorf("store: update mcp server: %w", err)
	}
	return affectedOne(res, "update mcp server")
}

// DeleteMcpServer удаляет сервер пользователя.
func (s *Store) DeleteMcpServer(ctx context.Context, id, userID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM mcp_servers WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("store: delete mcp server: %w", err)
	}
	return affectedOne(res, "delete mcp server")
}

const mcpServerSelect = `SELECT id, user_id, name, transport, command, args_json, env_json,
	url, headers_json, created_at FROM mcp_servers`

func encodeMcpLists(srv McpServer) (args, env, headers string, err error) {
	a, err := json.Marshal(orEmpty(srv.Args))
	if err != nil {
		return "", "", "", fmt.Errorf("store: encode mcp args: %w", err)
	}
	e, err := json.Marshal(orEmpty(srv.Env))
	if err != nil {
		return "", "", "", fmt.Errorf("store: encode mcp env: %w", err)
	}
	h, err := json.Marshal(orEmpty(srv.Headers))
	if err != nil {
		return "", "", "", fmt.Errorf("store: encode mcp headers: %w", err)
	}
	return string(a), string(e), string(h), nil
}

// orEmpty подменяет nil-слайс пустым: в колонке должен лежать JSON-массив, а не null.
func orEmpty[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

func decodeJSON(raw string, dst any) error {
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return fmt.Errorf("store: decode mcp field: %w", err)
	}
	return nil
}

// --- user_secrets (vault) ---

// ListSecrets возвращает имена секретов пользователя (без значений).
func (s *Store) ListSecrets(ctx context.Context, userID string) ([]UserSecret, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, updated_at FROM user_secrets WHERE user_id = ? ORDER BY name`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list secrets: %w", err)
	}
	defer rows.Close()

	var out []UserSecret
	for rows.Next() {
		var sec UserSecret
		var updatedAt int64
		if err := rows.Scan(&sec.Name, &updatedAt); err != nil {
			return nil, fmt.Errorf("store: scan secret: %w", err)
		}
		sec.UpdatedAt = fromUnix(updatedAt)
		out = append(out, sec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate secrets: %w", err)
	}
	return out, nil
}

// SecretValues возвращает расшифрованные секреты пользователя (имя → значение). Только для
// серверной сборки конфига MCP: наружу значения не уходят.
func (s *Store) SecretValues(ctx context.Context, userID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, value FROM user_secrets WHERE user_id = ?`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: secret values: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, fmt.Errorf("store: scan secret value: %w", err)
		}
		out[name] = s.cipher.Decrypt(value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate secret values: %w", err)
	}
	return out, nil
}

// SetSecret задаёт или заменяет значение секрета (в БД — ciphertext).
func (s *Store) SetSecret(ctx context.Context, userID, name, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_secrets (user_id, name, value, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(user_id, name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		userID, name, s.cipher.Encrypt(value), toUnix(time.Now()))
	if err != nil {
		return fmt.Errorf("store: set secret: %w", err)
	}
	return nil
}

// DeleteSecret удаляет секрет пользователя.
func (s *Store) DeleteSecret(ctx context.Context, userID, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM user_secrets WHERE user_id = ? AND name = ?`, userID, name)
	if err != nil {
		return fmt.Errorf("store: delete secret: %w", err)
	}
	return affectedOne(res, "delete secret")
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

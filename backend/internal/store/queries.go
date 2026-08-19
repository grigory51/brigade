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

func (s *Store) ListUsers(ctx context.Context) ([]UserSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT u.id, u.username, u.password_hash <> '',
		COALESCE(group_concat(i.provider, ', '), '')
		FROM users u LEFT JOIN auth_identities i ON i.user_id = u.id
		GROUP BY u.id, u.username, u.password_hash, u.created_at
		ORDER BY u.created_at, u.username`)
	if err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	defer rows.Close()
	var users []UserSummary
	for rows.Next() {
		var user UserSummary
		if err := rows.Scan(&user.ID, &user.Username, &user.Password, &user.Providers); err != nil {
			return nil, fmt.Errorf("store: scan user summary: %w", err)
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

// GetUserSettings возвращает настройки пользователя. Отсутствие строки — не ошибка:
// возвращаются дефолтные настройки (пустой токен).
func (s *Store) GetUserSettings(ctx context.Context, userID string) (UserSettings, error) {
	settings := UserSettings{UserID: userID}
	var updatedAt int64
	var images string
	err := s.db.QueryRowContext(ctx,
		`SELECT claude_token, codex_api_key, codex_auth_json, codex_default_profile,
		 memory_remote, agent_images, updated_at
		 FROM user_settings WHERE user_id = ?`, userID).
		Scan(&settings.ClaudeToken, &settings.CodexAPIKey, &settings.CodexAuthJSON, &settings.CodexDefaultProfile, &settings.MemoryRemote,
			&images, &updatedAt)
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
	// updated_at сканируется, но не хранится в модели (никто не читает).
	_ = updatedAt
	return settings, nil
}

func (s *Store) ListAgentConnections(ctx context.Context, userID string) ([]AgentConnection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, name, agent_type, auth_profile, secret, created_at
		FROM agent_connections WHERE user_id = ? ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list agent connections: %w", err)
	}
	defer rows.Close()
	var out []AgentConnection
	for rows.Next() {
		var item AgentConnection
		var created int64
		if err := rows.Scan(&item.ID, &item.UserID, &item.Name, &item.AgentType, &item.AuthProfile, &item.Secret, &created); err != nil {
			return nil, fmt.Errorf("store: scan agent connection: %w", err)
		}
		item.Secret = s.cipher.Decrypt(item.Secret)
		item.CreatedAt = fromUnix(created)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) GetAgentConnection(ctx context.Context, userID, id string) (AgentConnection, error) {
	var item AgentConnection
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, name, agent_type, auth_profile, secret, created_at
		FROM agent_connections WHERE user_id = ? AND id = ?`, userID, id).
		Scan(&item.ID, &item.UserID, &item.Name, &item.AgentType, &item.AuthProfile, &item.Secret, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentConnection{}, ErrNotFound
	}
	if err != nil {
		return AgentConnection{}, fmt.Errorf("store: get agent connection: %w", err)
	}
	item.Secret = s.cipher.Decrypt(item.Secret)
	item.CreatedAt = fromUnix(created)
	return item, nil
}

func (s *Store) GetAgentConnectionByProfile(ctx context.Context, userID, agentType, authProfile string) (AgentConnection, error) {
	var item AgentConnection
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, name, agent_type, auth_profile, secret, created_at
		FROM agent_connections WHERE user_id = ? AND agent_type = ? AND auth_profile = ? ORDER BY created_at LIMIT 1`,
		userID, agentType, authProfile).Scan(&item.ID, &item.UserID, &item.Name, &item.AgentType, &item.AuthProfile, &item.Secret, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentConnection{}, ErrNotFound
	}
	if err != nil {
		return AgentConnection{}, fmt.Errorf("store: get agent connection by profile: %w", err)
	}
	item.Secret = s.cipher.Decrypt(item.Secret)
	item.CreatedAt = fromUnix(created)
	return item, nil
}

func (s *Store) SaveAgentConnection(ctx context.Context, item AgentConnection) error {
	now := toUnix(time.Now())
	res, err := s.db.ExecContext(ctx, `INSERT INTO agent_connections
		(id, user_id, name, agent_type, auth_profile, secret, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, agent_type=excluded.agent_type,
			auth_profile=excluded.auth_profile,
			secret=CASE WHEN excluded.secret = '' THEN agent_connections.secret ELSE excluded.secret END,
			updated_at=excluded.updated_at
		WHERE agent_connections.user_id = excluded.user_id`,
		item.ID, item.UserID, item.Name, item.AgentType, item.AuthProfile, s.cipher.Encrypt(item.Secret), now, now)
	if err != nil {
		return fmt.Errorf("store: save agent connection: %w", err)
	}
	return affectedOne(res, "save agent connection")
}

func (s *Store) SetAgentConnectionSecret(ctx context.Context, userID, id, secret string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE agent_connections SET secret = ?, updated_at = ? WHERE user_id = ? AND id = ?`,
		s.cipher.Encrypt(secret), toUnix(time.Now()), userID, id)
	if err != nil {
		return fmt.Errorf("store: set agent connection secret: %w", err)
	}
	return affectedOne(res, "set agent connection secret")
}

func (s *Store) DeleteAgentConnection(ctx context.Context, userID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM agent_connections WHERE user_id = ? AND id = ?`, userID, id)
	if err != nil {
		return fmt.Errorf("store: delete agent connection: %w", err)
	}
	return affectedOne(res, "delete agent connection")
}

// ListNotificationBackends возвращает все подключения уведомлений пользователя.
func (s *Store) ListNotificationBackends(ctx context.Context, userID string) ([]NotificationBackend, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, kind, name, config, secret, events
		 FROM notification_backends WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list notification backends: %w", err)
	}
	defer rows.Close()
	var out []NotificationBackend
	for rows.Next() {
		var b NotificationBackend
		if err := rows.Scan(&b.ID, &b.UserID, &b.Kind, &b.Name, &b.Config, &b.Secret, &b.Events); err != nil {
			return nil, fmt.Errorf("store: scan notification backend: %w", err)
		}
		b.Secret = s.cipher.Decrypt(b.Secret)
		out = append(out, b)
	}
	return out, rows.Err()
}

// SaveNotificationBackend создаёт или обновляет подключение. Пустой secret сохраняет
// прежний; для нового подключения это означает «без секрета».
func (s *Store) SaveNotificationBackend(ctx context.Context, b NotificationBackend) error {
	now := toUnix(time.Now())
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO notification_backends
		   (id, user_id, kind, name, config, secret, events, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   name = excluded.name,
		   config = excluded.config,
		   secret = CASE WHEN excluded.secret = '' THEN notification_backends.secret ELSE excluded.secret END,
		   events = excluded.events,
		   updated_at = excluded.updated_at
		 WHERE notification_backends.user_id = excluded.user_id`,
		b.ID, b.UserID, b.Kind, b.Name, b.Config, s.cipher.Encrypt(b.Secret), b.Events, now, now)
	if err != nil {
		return fmt.Errorf("store: save notification backend: %w", err)
	}
	return nil
}

func (s *Store) DeleteNotificationBackend(ctx context.Context, userID, id string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM notification_backends WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("store: delete notification backend: %w", err)
	}
	return nil
}

// --- telegram ---

const telegramBotSelect = `SELECT id, user_id, token, telegram_id, username, name,
	owner_telegram_id, owner_telegram_username, agent_type, auth_profile, image, mcp_servers,
	bind_token_hash, bind_token_expires_at, update_offset, supports_guest_queries,
	has_topics_enabled, created_at FROM telegram_bots`

func (s *Store) ListTelegramBots(ctx context.Context, userID string) ([]TelegramBot, error) {
	rows, err := s.db.QueryContext(ctx, telegramBotSelect+` WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list telegram bots: %w", err)
	}
	defer rows.Close()
	var out []TelegramBot
	for rows.Next() {
		bot, err := s.scanTelegramBot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, bot)
	}
	return out, rows.Err()
}

func (s *Store) ListAllTelegramBots(ctx context.Context) ([]TelegramBot, error) {
	rows, err := s.db.QueryContext(ctx, telegramBotSelect+` ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("store: list all telegram bots: %w", err)
	}
	defer rows.Close()
	var out []TelegramBot
	for rows.Next() {
		bot, err := s.scanTelegramBot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, bot)
	}
	return out, rows.Err()
}

func (s *Store) scanTelegramBot(row rowScanner) (TelegramBot, error) {
	var bot TelegramBot
	var token, mcp string
	var bindExpires, createdAt int64
	if err := row.Scan(&bot.ID, &bot.UserID, &token, &bot.TelegramID, &bot.Username, &bot.Name,
		&bot.OwnerTelegramID, &bot.OwnerTelegramUsername, &bot.AgentType, &bot.AuthProfile,
		&bot.Image, &mcp, &bot.BindTokenHash, &bindExpires, &bot.UpdateOffset,
		&bot.SupportsGuestQueries, &bot.HasTopicsEnabled, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TelegramBot{}, ErrNotFound
		}
		return TelegramBot{}, fmt.Errorf("store: scan telegram bot: %w", err)
	}
	bot.Token = s.cipher.Decrypt(token)
	bot.BindTokenExpiresAt = fromUnix(bindExpires)
	bot.CreatedAt = fromUnix(createdAt)
	if mcp != "" {
		bot.McpServers = strings.Split(mcp, ",")
	}
	return bot, nil
}

func (s *Store) GetTelegramBot(ctx context.Context, id string) (TelegramBot, error) {
	return s.scanTelegramBot(s.db.QueryRowContext(ctx, telegramBotSelect+` WHERE id = ?`, id))
}

func (s *Store) SaveTelegramBot(ctx context.Context, bot TelegramBot) error {
	now := toUnix(time.Now())
	_, err := s.db.ExecContext(ctx, `INSERT INTO telegram_bots
		(id, user_id, token, telegram_id, username, name, owner_telegram_id,
		 owner_telegram_username, agent_type, auth_profile, image, mcp_servers,
		 bind_token_hash, bind_token_expires_at, update_offset, supports_guest_queries,
		 has_topics_enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET token=excluded.token, telegram_id=excluded.telegram_id,
		 username=excluded.username, name=excluded.name, agent_type=excluded.agent_type,
		 auth_profile=excluded.auth_profile, image=excluded.image, mcp_servers=excluded.mcp_servers,
		 supports_guest_queries=excluded.supports_guest_queries,
		 has_topics_enabled=excluded.has_topics_enabled, updated_at=excluded.updated_at
		WHERE telegram_bots.user_id=excluded.user_id`,
		bot.ID, bot.UserID, s.cipher.Encrypt(bot.Token), bot.TelegramID, bot.Username, bot.Name,
		bot.OwnerTelegramID, bot.OwnerTelegramUsername, bot.AgentType, bot.AuthProfile,
		bot.Image, strings.Join(bot.McpServers, ","), bot.BindTokenHash,
		toUnix(bot.BindTokenExpiresAt), bot.UpdateOffset, bot.SupportsGuestQueries,
		bot.HasTopicsEnabled, now, now)
	if err != nil {
		return fmt.Errorf("store: save telegram bot: %w", err)
	}
	return nil
}

func (s *Store) DeleteTelegramBot(ctx context.Context, userID, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete telegram bot: %w", err)
	}
	defer tx.Rollback()
	for _, table := range []string{"telegram_updates", "telegram_conversations"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE bot_id = ?`, id); err != nil {
			return fmt.Errorf("store: delete telegram bot: %w", err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM telegram_bots WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("store: delete telegram bot: %w", err)
	}
	if err := affectedOne(res, "delete telegram bot"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetTelegramBinding(ctx context.Context, id, hash string, expires time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE telegram_bots SET bind_token_hash=?, bind_token_expires_at=?, updated_at=? WHERE id=?`, hash, toUnix(expires), toUnix(time.Now()), id)
	if err != nil {
		return fmt.Errorf("store: set telegram binding: %w", err)
	}
	return affectedOne(res, "set telegram binding")
}

func (s *Store) BindTelegramOwner(ctx context.Context, id string, telegramID int64, username string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE telegram_bots SET owner_telegram_id=?, owner_telegram_username=?, bind_token_hash='', bind_token_expires_at=0, updated_at=? WHERE id=?`, telegramID, username, toUnix(time.Now()), id)
	if err != nil {
		return fmt.Errorf("store: bind telegram owner: %w", err)
	}
	return affectedOne(res, "bind telegram owner")
}

func (s *Store) SetTelegramUpdateOffset(ctx context.Context, id string, offset int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE telegram_bots SET update_offset=? WHERE id=? AND update_offset < ?`, offset, id, offset)
	return err
}

func (s *Store) InsertTelegramUpdate(ctx context.Context, botID string, updateID int64, payload string) (bool, error) {
	now := toUnix(time.Now())
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO telegram_updates (bot_id, update_id, payload, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, botID, updateID, payload, now, now)
	if err != nil {
		return false, fmt.Errorf("store: insert telegram update: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) ListTelegramUpdates(ctx context.Context, botID, state string) ([]TelegramUpdate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT bot_id, update_id, payload, state, response, error, created_at FROM telegram_updates WHERE bot_id=? AND state=? ORDER BY update_id`, botID, state)
	if err != nil {
		return nil, fmt.Errorf("store: list telegram updates: %w", err)
	}
	defer rows.Close()
	var out []TelegramUpdate
	for rows.Next() {
		var update TelegramUpdate
		var created int64
		if err := rows.Scan(&update.BotID, &update.UpdateID, &update.Payload, &update.State, &update.Response, &update.Error, &created); err != nil {
			return nil, fmt.Errorf("store: scan telegram update: %w", err)
		}
		update.CreatedAt = fromUnix(created)
		out = append(out, update)
	}
	return out, rows.Err()
}

func (s *Store) SetTelegramUpdateState(ctx context.Context, botID string, updateID int64, state, response, errorText string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE telegram_updates SET state=?, response=?, error=?, updated_at=? WHERE bot_id=? AND update_id=?`, state, response, errorText, toUnix(time.Now()), botID, updateID)
	return err
}

func (s *Store) SetTelegramUpdatePayload(ctx context.Context, botID string, updateID int64, payload string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE telegram_updates SET payload=?, updated_at=? WHERE bot_id=? AND update_id=?`, payload, toUnix(time.Now()), botID, updateID)
	return err
}

func (s *Store) DeleteTelegramUpdate(ctx context.Context, botID string, updateID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM telegram_updates WHERE bot_id=? AND update_id=?`, botID, updateID)
	return err
}

func (s *Store) TelegramConversation(ctx context.Context, botID, scope string, chatID, threadID int64) (TelegramConversation, error) {
	var conversation TelegramConversation
	err := s.db.QueryRowContext(ctx, `SELECT bot_id, scope, chat_id, thread_id, session_id FROM telegram_conversations WHERE bot_id=? AND scope=? AND chat_id=? AND thread_id=?`, botID, scope, chatID, threadID).
		Scan(&conversation.BotID, &conversation.Scope, &conversation.ChatID, &conversation.ThreadID, &conversation.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return TelegramConversation{}, ErrNotFound
	}
	return conversation, err
}

func (s *Store) SetTelegramConversation(ctx context.Context, conversation TelegramConversation) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO telegram_conversations (bot_id, scope, chat_id, thread_id, session_id) VALUES (?, ?, ?, ?, ?) ON CONFLICT(bot_id, scope, chat_id, thread_id) DO UPDATE SET session_id=excluded.session_id`, conversation.BotID, conversation.Scope, conversation.ChatID, conversation.ThreadID, conversation.SessionID)
	return err
}

func (s *Store) DeleteTelegramConversation(ctx context.Context, botID, scope string, chatID, threadID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM telegram_conversations WHERE bot_id=? AND scope=? AND chat_id=? AND thread_id=?`, botID, scope, chatID, threadID)
	return err
}

// --- sessions ---

// CreateSession вставляет новую сессию.
func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions
		 (id, user_id, mode, kind, agent_type, agent_session_id, container_label, status, cwd, created_at, name, group_label, unread, mcp_servers, image, auth_profile, instruction_profile, response_profile_id, response_profile_name, response_instructions, experience_id, experience_version)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.UserID, string(sess.Mode), string(sess.Kind), sess.AgentType,
		sess.AgentSessionID, sess.ContainerLabel, string(sess.Status), sess.Cwd, toUnix(sess.CreatedAt), sess.Name, sess.GroupLabel, sess.Unread,
		strings.Join(sess.McpServers, ","), sess.Image, sess.AuthProfile, sess.InstructionProfile,
		sess.ResponseProfileID, sess.ResponseProfileName, sess.ResponseInstructions, sess.ExperienceID, sess.ExperienceVersion,
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

// UpdateSessionNameIfEmpty сохраняет имя от агента, не перетирая ручное переименование.
func (s *Store) UpdateSessionNameIfEmpty(ctx context.Context, id, name string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET name = ? WHERE id = ? AND name = ''`, name, id)
	if err != nil {
		return false, fmt.Errorf("store: update empty session name: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: update empty session name rows: %w", err)
	}
	return n > 0, nil
}

func (s *Store) MarkSessionUnread(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET unread = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: mark session unread: %w", err)
	}
	return nil
}

func (s *Store) MarkSessionRead(ctx context.Context, id, userID string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET unread = 0 WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("store: mark session read: %w", err)
	}
	return affectedOne(res, "mark session read")
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
	container_label, status, cwd, created_at, name, group_label, unread, mcp_servers, image, auth_profile, instruction_profile,
	response_profile_id, response_profile_name, response_instructions, experience_id, experience_version FROM sessions`

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
		&sess.AgentSessionID, &sess.ContainerLabel, &status, &sess.Cwd, &createdAt, &sess.Name, &sess.GroupLabel, &sess.Unread, &mcp, &sess.Image, &sess.AuthProfile, &sess.InstructionProfile,
		&sess.ResponseProfileID, &sess.ResponseProfileName, &sess.ResponseInstructions, &sess.ExperienceID, &sess.ExperienceVersion)
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

const pluginSelect = `SELECT id, name, version, bundle_path, source, manifest_json, installed_at FROM plugins`

func (s *Store) ListPlugins(ctx context.Context) ([]Plugin, error) {
	rows, err := s.db.QueryContext(ctx, pluginSelect+` WHERE active = 1 ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list plugins: %w", err)
	}
	defer rows.Close()
	var plugins []Plugin
	for rows.Next() {
		var plugin Plugin
		var installedAt int64
		if err := rows.Scan(&plugin.ID, &plugin.Name, &plugin.Version, &plugin.BundlePath, &plugin.Source, &plugin.ManifestJSON, &installedAt); err != nil {
			return nil, fmt.Errorf("store: scan plugin: %w", err)
		}
		plugin.InstalledAt = fromUnix(installedAt)
		plugins = append(plugins, plugin)
	}
	return plugins, rows.Err()
}

func (s *Store) GetPlugin(ctx context.Context, id, version string) (Plugin, error) {
	var plugin Plugin
	var installedAt int64
	where, args := ` WHERE id = ? AND active = 1`, []any{id}
	if version != "" {
		where, args = ` WHERE id = ? AND version = ?`, []any{id, version}
	}
	err := s.db.QueryRowContext(ctx, pluginSelect+where, args...).Scan(
		&plugin.ID, &plugin.Name, &plugin.Version, &plugin.BundlePath, &plugin.Source, &plugin.ManifestJSON, &installedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Plugin{}, ErrNotFound
	}
	if err != nil {
		return Plugin{}, fmt.Errorf("store: get plugin: %w", err)
	}
	plugin.InstalledAt = fromUnix(installedAt)
	return plugin, nil
}

func (s *Store) PutPlugin(ctx context.Context, plugin Plugin) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: put plugin: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE plugins SET active = 0 WHERE id = ?`, plugin.ID); err != nil {
		return fmt.Errorf("store: deactivate plugin: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO plugins (id, name, version, bundle_path, source, manifest_json, installed_at, active)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT(id, version) DO UPDATE SET name=excluded.name, bundle_path=excluded.bundle_path, source=excluded.source,
		manifest_json=excluded.manifest_json, installed_at=excluded.installed_at, active=1`,
		plugin.ID, plugin.Name, plugin.Version, plugin.BundlePath, plugin.Source, plugin.ManifestJSON, toUnix(plugin.InstalledAt))
	if err != nil {
		return fmt.Errorf("store: put plugin: %w", err)
	}
	return tx.Commit()
}

func (s *Store) DeletePlugin(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM plugins WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete plugin: %w", err)
	}
	return affectedOne(res, "delete plugin")
}

func (s *Store) PluginSessionCount(ctx context.Context, id string) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE experience_id = ?`, id).Scan(&count); err != nil {
		return 0, fmt.Errorf("store: count plugin sessions: %w", err)
	}
	return count, nil
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

// UpdateSessionInstructionProfile сохраняет внутренний профиль поведения агента.
func (s *Store) UpdateSessionInstructionProfile(ctx context.Context, id, profile string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET instruction_profile = ? WHERE id = ?`, profile, id)
	if err != nil {
		return fmt.Errorf("store: update session instruction profile: %w", err)
	}
	return affectedOne(res, "update session instruction profile")
}

func (s *Store) UpdateSessionResponseProfile(ctx context.Context, id, profileID, name, instructions string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET response_profile_id = ?, response_profile_name = ?, response_instructions = ? WHERE id = ?`,
		profileID, name, instructions, id)
	if err != nil {
		return fmt.Errorf("store: update session response profile: %w", err)
	}
	return affectedOne(res, "update session response profile")
}

// --- response_profiles ---

const responseProfileSelect = `SELECT id, user_id, name, instructions, created_at, updated_at FROM response_profiles`

func (s *Store) ListResponseProfiles(ctx context.Context, userID string) ([]ResponseProfile, error) {
	rows, err := s.db.QueryContext(ctx, responseProfileSelect+` WHERE user_id = ? ORDER BY created_at, name`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list response profiles: %w", err)
	}
	defer rows.Close()
	var out []ResponseProfile
	for rows.Next() {
		profile, err := scanResponseProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, profile)
	}
	return out, rows.Err()
}

func (s *Store) GetResponseProfile(ctx context.Context, id, userID string) (ResponseProfile, error) {
	profile, err := scanResponseProfile(s.db.QueryRowContext(ctx, responseProfileSelect+` WHERE id = ? AND user_id = ?`, id, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return ResponseProfile{}, ErrNotFound
	}
	return profile, err
}

func (s *Store) CreateResponseProfile(ctx context.Context, profile ResponseProfile) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO response_profiles (id, user_id, name, instructions, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		profile.ID, profile.UserID, profile.Name, profile.Instructions, toUnix(profile.CreatedAt), toUnix(profile.UpdatedAt))
	if err != nil {
		return fmt.Errorf("store: create response profile: %w", err)
	}
	return nil
}

func (s *Store) UpdateResponseProfile(ctx context.Context, profile ResponseProfile) error {
	res, err := s.db.ExecContext(ctx, `UPDATE response_profiles SET name = ?, instructions = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		profile.Name, profile.Instructions, toUnix(profile.UpdatedAt), profile.ID, profile.UserID)
	if err != nil {
		return fmt.Errorf("store: update response profile: %w", err)
	}
	return affectedOne(res, "update response profile")
}

func (s *Store) DeleteResponseProfile(ctx context.Context, id, userID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM response_profiles WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("store: delete response profile: %w", err)
	}
	return affectedOne(res, "delete response profile")
}

func scanResponseProfile(row rowScanner) (ResponseProfile, error) {
	var profile ResponseProfile
	var createdAt, updatedAt int64
	if err := row.Scan(&profile.ID, &profile.UserID, &profile.Name, &profile.Instructions, &createdAt, &updatedAt); err != nil {
		return ResponseProfile{}, err
	}
	profile.CreatedAt = fromUnix(createdAt)
	profile.UpdatedAt = fromUnix(updatedAt)
	return profile, nil
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

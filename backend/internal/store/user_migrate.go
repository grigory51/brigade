package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// MigrateUser переносит данные oldID в существующего OIDC-пользователя newID.
// При совпадении именованных настроек данные старого пользователя имеют приоритет.
func (s *Store) MigrateUser(ctx context.Context, oldID, newID string) error {
	if oldID == "" || newID == "" || oldID == newID {
		return errors.New("store: migrate user: нужны два разных user id")
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("store: migrate user: enable foreign keys: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: migrate user: begin: %w", err)
	}
	defer tx.Rollback()

	for _, id := range []string{oldID, newID} {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id = ?)`, id).Scan(&exists); err != nil {
			return fmt.Errorf("store: migrate user: check %q: %w", id, err)
		}
		if exists == 0 {
			return fmt.Errorf("store: migrate user: пользователь %q не найден", id)
		}
	}
	var external int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM auth_identities WHERE user_id = ?)`, newID).Scan(&external); err != nil {
		return fmt.Errorf("store: migrate user: check target identity: %w", err)
	}
	if external == 0 {
		return errors.New("store: migrate user: у нового пользователя нет OIDC identity")
	}
	var targetSessions int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE user_id = ?`, newID).Scan(&targetSessions); err != nil {
		return fmt.Errorf("store: migrate user: check target sessions: %w", err)
	}
	if targetSessions != 0 {
		return errors.New("store: migrate user: у нового пользователя уже есть сессии")
	}

	// Целевая OIDC identity и её refresh tokens остаются. Старые токены отзываются,
	// чтобы удалённая password-учётка не сохраняла активные сессии.
	statements := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM refresh_tokens WHERE user_id = ?`, []any{oldID}},
		{`DELETE FROM user_settings WHERE user_id = ? AND EXISTS (SELECT 1 FROM user_settings WHERE user_id = ?)`, []any{newID, oldID}},
		{`DELETE FROM user_secrets WHERE user_id = ? AND name IN (SELECT name FROM user_secrets WHERE user_id = ?)`, []any{newID, oldID}},
		{`DELETE FROM mcp_servers WHERE user_id = ? AND name IN (SELECT name FROM mcp_servers WHERE user_id = ?)`, []any{newID, oldID}},
		{`DELETE FROM plugin_configs WHERE user_id = ? AND plugin_id IN (SELECT plugin_id FROM plugin_configs WHERE user_id = ?)`, []any{newID, oldID}},
		{`DELETE FROM plugins WHERE owner_id = ? AND EXISTS (
			SELECT 1 FROM plugins old WHERE old.owner_id = ? AND old.id = plugins.id AND old.version = plugins.version AND old.target = plugins.target
		)`, []any{newID, oldID}},
		{`DELETE FROM response_profiles WHERE user_id = ? AND name COLLATE NOCASE IN (SELECT name FROM response_profiles WHERE user_id = ?)`, []any{newID, oldID}},
		{`DELETE FROM auth_identities WHERE user_id = ? AND provider IN (SELECT provider FROM auth_identities WHERE user_id = ?)`, []any{oldID, newID}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return fmt.Errorf("store: migrate user: merge conflicts: %w", err)
		}
	}

	for _, table := range []string{
		"sessions", "user_settings", "user_secrets", "mcp_servers", "notification_backends",
		"telegram_bots", "response_profiles", "agent_connections", "auth_identities", "plugin_configs",
	} {
		if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET user_id = ? WHERE user_id = ?`, newID, oldID); err != nil {
			return fmt.Errorf("store: migrate user: update %s: %w", table, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE plugins SET owner_id = ? WHERE owner_id = ?`, newID, oldID); err != nil {
		return fmt.Errorf("store: migrate user: update plugins: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, oldID)
	if err != nil {
		return fmt.Errorf("store: migrate user: delete old user: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: migrate user: deleted rows: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("store: migrate user: delete old user affected %d rows", affected)
	}
	if err := foreignKeyCheck(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: migrate user: commit: %w", err)
	}
	return nil
}

// DeleteUser удаляет пользователя без сессий и все связанные с ним настройки.
func (s *Store) DeleteUser(ctx context.Context, userID string) error {
	if userID == "" {
		return errors.New("store: delete user: user id не задан")
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("store: delete user: enable foreign keys: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete user: begin: %w", err)
	}
	defer tx.Rollback()

	var sessions int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE user_id = ?`, userID).Scan(&sessions); err != nil {
		return fmt.Errorf("store: delete user: check sessions: %w", err)
	}
	if sessions != 0 {
		return fmt.Errorf("store: delete user: у пользователя есть сессии (%d)", sessions)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("store: delete user: refresh tokens: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugins WHERE owner_id = ?`, userID); err != nil {
		return fmt.Errorf("store: delete user: plugins: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
	if err != nil {
		return fmt.Errorf("store: delete user: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete user: affected rows: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("store: delete user: пользователь %q не найден", userID)
	}
	if err := foreignKeyCheck(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: delete user: commit: %w", err)
	}
	return nil
}

func foreignKeyCheck(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("store: migrate user: foreign key check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("store: migrate user: foreign key check failed")
	}
	return rows.Err()
}

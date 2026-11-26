package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// HasManagedPlugin учитывает историю, чтобы CLI не мог вернуть удалённое из
// конфига системное приложение в каталог в обход plugins.bundles.
func (s *Store) HasManagedPlugin(ctx context.Context, id string) (bool, error) {
	var managed bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM plugins WHERE id = ? AND managed = 1)`, id).Scan(&managed); err != nil {
		return false, fmt.Errorf("store: check managed plugin: %w", err)
	}
	return managed, nil
}

// GetPluginVariant читает точную установленную версию, включая отключённые.
// Метод предназначен для установки; доступность проверяет GetPlugin.
func (s *Store) GetPluginVariant(ctx context.Context, ownerID, id, version, target string) (Plugin, error) {
	var plugin Plugin
	var installedAt int64
	err := s.db.QueryRowContext(ctx, pluginSelect+` WHERE owner_id = ? AND id = ? AND version = ? AND target = ?`, ownerID, id, version, target).Scan(
		&plugin.OwnerID, &plugin.ID, &plugin.Name, &plugin.Version, &plugin.Target, &plugin.BundlePath, &plugin.Source, &plugin.ManifestJSON, &installedAt, &plugin.Managed)
	if errors.Is(err, sql.ErrNoRows) {
		return Plugin{}, ErrNotFound
	}
	if err != nil {
		return Plugin{}, fmt.Errorf("store: get plugin variant: %w", err)
	}
	plugin.InstalledAt = fromUnix(installedAt)
	return plugin, nil
}

// SyncManagedPlugins заменяет доступный набор одной транзакцией. Исторические
// версии и пользовательские установки сохраняются; пустой набор отключает managed.
func (s *Store) SyncManagedPlugins(ctx context.Context, plugins []Plugin, localTarget string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: sync managed plugins: %w", err)
	}
	defer tx.Rollback()
	if err := pinLegacyPluginOwners(ctx, tx, localTarget); err != nil {
		return err
	}
	for _, plugin := range plugins {
		if plugin.OwnerID != "" || !plugin.Managed {
			return errors.New("store: managed plugins must be system-owned")
		}
		// При переходе CLI-установки под управление конфига её версии сохраняют
		// системного владельца, а доступность новых сессий задаётся конфигом.
		if _, err := tx.ExecContext(ctx, `UPDATE plugins SET managed = 1 WHERE owner_id = '' AND id = ?`, plugin.ID); err != nil {
			return fmt.Errorf("store: adopt managed plugin: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE plugins SET active = 0 WHERE managed = 1`); err != nil {
		return fmt.Errorf("store: deactivate managed plugins: %w", err)
	}
	for _, plugin := range plugins {
		if err := putPlugin(ctx, tx, plugin); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetPluginForOwner разрешает исторический pin без active/managed-фильтра.
// Авторизация ownerID относительно пользователя остаётся обязанностью вызывающего.
func (s *Store) GetPluginForOwner(ctx context.Context, ownerID, id, version, target string) (Plugin, error) {
	if version == "" {
		return Plugin{}, errors.New("store: pinned plugin version is required")
	}
	where, args := ` WHERE owner_id = ? AND id = ? AND version = ?`, []any{ownerID, id, version}
	if target != "" {
		where += ` AND target IN (?, ?, 'portable')`
		args = append(args, target, strings.SplitN(target, "-", 2)[0]+"-any")
	}
	where += ` ORDER BY target = ? DESC, installed_at DESC LIMIT 1`
	args = append(args, target)
	var plugin Plugin
	var installedAt int64
	err := s.db.QueryRowContext(ctx, pluginSelect+where, args...).Scan(
		&plugin.OwnerID, &plugin.ID, &plugin.Name, &plugin.Version, &plugin.Target, &plugin.BundlePath, &plugin.Source, &plugin.ManifestJSON, &installedAt, &plugin.Managed)
	if errors.Is(err, sql.ErrNoRows) {
		return Plugin{}, ErrNotFound
	}
	if err != nil {
		return Plugin{}, fmt.Errorf("store: get pinned plugin: %w", err)
	}
	plugin.InstalledAt = fromUnix(installedAt)
	return plugin, nil
}

// Владельцы legacy pins фиксируются до изменения каталога по прежнему алгоритму
// GetPlugin. Отсутствующие bundles остаются неразрешёнными; новые managed-записи
// не могут стать владельцем такой сессии при следующем старте.
func pinLegacyPluginOwners(ctx context.Context, tx *sql.Tx, localTarget string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, user_id, mode, experience_id, experience_version FROM sessions WHERE experience_id <> '' AND experience_owner_id IS NULL`)
	if err != nil {
		return fmt.Errorf("store: list legacy plugin pins: %w", err)
	}
	var sessions []Session
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.UserID, &sess.Mode, &sess.ExperienceID, &sess.ExperienceVersion); err != nil {
			rows.Close()
			return fmt.Errorf("store: scan legacy plugin pin: %w", err)
		}
		sessions = append(sessions, sess)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("store: iterate legacy plugin pins: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("store: close legacy plugin pins: %w", err)
	}
	for _, sess := range sessions {
		target := localTarget
		if sess.Mode == SessionModeDocker {
			target = "linux-amd64"
		}
		query := `SELECT owner_id, version FROM plugins WHERE owner_id IN ('', ?) AND id = ? AND managed = 0`
		args := []any{sess.UserID, sess.ExperienceID}
		if sess.ExperienceVersion != "" {
			query += ` AND version = ?`
			args = append(args, sess.ExperienceVersion)
		} else {
			query += ` AND active = 1`
		}
		query += ` AND target IN (?, ?, 'portable') ORDER BY owner_id = ? DESC, target = ? DESC, installed_at DESC LIMIT 1`
		args = append(args, target, strings.SplitN(target, "-", 2)[0]+"-any", sess.UserID, target)
		var ownerID, version string
		if err := tx.QueryRowContext(ctx, query, args...).Scan(&ownerID, &version); errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			return fmt.Errorf("store: resolve legacy plugin pin: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET experience_owner_id = ?, experience_version = ? WHERE id = ?`, ownerID, version, sess.ID); err != nil {
			return fmt.Errorf("store: pin legacy plugin owner: %w", err)
		}
	}
	return nil
}

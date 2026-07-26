package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/grigory51/brigade/backend/internal/memory"
)

// MigrateArchivesToMemory переносит архив сессий из БД в личную память пользователей и
// сносит осиротевшую схему. Одноразовая операция: архив теперь живёт в git-репозитории
// памяти (см. SessionArchive), а в БД остаются только живые сессии.
//
// Почему не goose-миграцией: перенос требует git и сети (клон репозитория пользователя,
// push), а goose-миграции применяются в store.Open — до того, как собраны сервисы. Дроп
// схемы там же снёс бы данные раньше, чем они уедут в память. Поэтому переносит и дропает
// этот код, после успешного переноса; повторный запуск — no-op (таблицы уже нет).
//
// Сбой у одного пользователя (не настроена память, недоступен remote) не прерывает
// остальных и НЕ приводит к дропу: данные остаются в БД до следующего старта.
func (r *Registry) MigrateArchivesToMemory(ctx context.Context) {
	if r.archive == nil {
		return
	}
	rows, err := r.legacyArchived(ctx)
	if err != nil {
		if errors.Is(err, errNoLegacyArchive) {
			return // схема уже снесена прошлым запуском
		}
		log.Printf("session: archive migration: %v", err)
		return
	}
	if len(rows) == 0 {
		r.dropLegacyArchive(ctx)
		return
	}

	moved := 0
	for _, row := range rows {
		if err := r.archive.ArchiveSession(ctx, row.userID, row.sess, row.messages); err != nil {
			log.Printf("session: archive migration %s: %v", row.sess.ID, err)
			continue
		}
		if err := r.store.DeleteSession(ctx, row.sess.ID); err != nil {
			log.Printf("session: archive migration %s delete row: %v", row.sess.ID, err)
			continue
		}
		moved++
	}
	log.Printf("session: archive migration: перенесено %d из %d", moved, len(rows))
	if moved == len(rows) {
		r.dropLegacyArchive(ctx)
	}
}

// errNoLegacyArchive — прежней схемы архива в БД уже нет.
var errNoLegacyArchive = errors.New("session: legacy archive schema is gone")

// legacyArchivedRow — архивная сессия из БД вместе с владельцем и снимком ленты.
type legacyArchivedRow struct {
	userID   string
	sess     memory.ArchivedSession
	messages []byte
}

// legacyArchived читает архивные сессии прямым SQL: колонки archived/summary и таблица
// session_snapshots из модели store уже убраны, а мигрировать нужно именно их.
func (r *Registry) legacyArchived(ctx context.Context) ([]legacyArchivedRow, error) {
	db := r.store.DB()
	// Отсутствие таблицы снимков — признак уже выполненной миграции (её дропает этот же код).
	var probe string
	err := db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'session_snapshots'`).Scan(&probe)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoLegacyArchive
	}
	if err != nil {
		return nil, fmt.Errorf("probe schema: %w", err)
	}

	rows, err := db.QueryContext(ctx,
		`SELECT s.id, s.user_id, s.name, s.agent_type, s.kind, s.parent_id, s.summary,
		        s.created_at, COALESCE(snap.messages, ''), COALESCE(snap.created_at, s.created_at)
		 FROM sessions s
		 LEFT JOIN session_snapshots snap ON snap.session_id = s.id
		 WHERE s.archived = 1`)
	if err != nil {
		return nil, fmt.Errorf("select archived: %w", err)
	}
	defer rows.Close()

	var out []legacyArchivedRow
	for rows.Next() {
		var (
			row              legacyArchivedRow
			messages         string
			created, archived int64
		)
		if err := rows.Scan(&row.sess.ID, &row.userID, &row.sess.Name, &row.sess.AgentType,
			&row.sess.Kind, &row.sess.ParentID, &row.sess.Summary, &created,
			&messages, &archived); err != nil {
			return nil, fmt.Errorf("scan archived: %w", err)
		}
		row.sess.Created = time.Unix(created, 0).UTC()
		row.sess.Archived = time.Unix(archived, 0).UTC()
		// Снимок мог не сохраниться (сессия архивировалась без живого агента) — валидный
		// случай, в память уедет пустая лента.
		if json.Valid([]byte(messages)) {
			row.messages = []byte(messages)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// dropLegacyArchive сносит осиротевшую схему архива. Колонки sessions.archived/summary
// больше не читаются (см. store.sessionSelect), поэтому их отсутствие ничего не ломает;
// ошибки только логируются — рабочий инстанс из-за неудачного дропа падать не должен.
func (r *Registry) dropLegacyArchive(ctx context.Context) {
	db := r.store.DB()
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_sessions_archived`,
		`DROP TABLE IF EXISTS session_snapshots`,
		`ALTER TABLE sessions DROP COLUMN archived`,
		`ALTER TABLE sessions DROP COLUMN summary`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			log.Printf("session: archive migration drop (%s): %v", stmt, err)
		}
	}
}

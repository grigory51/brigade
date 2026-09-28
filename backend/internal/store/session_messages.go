package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AddSessionMessage сохраняет сообщение и увеличивает ревизию ленты в одной транзакции.
// Повторная доставка внешнего сообщения не меняет ревизию.
func (s *Store) AddSessionMessage(ctx context.Context, message SessionMessage) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: begin message insert: %w", err)
	}
	defer tx.Rollback()
	var policy SessionExecutionPolicy
	if err := tx.QueryRowContext(ctx, `SELECT execution_policy FROM sessions WHERE id = ?`, message.SessionID).Scan(&policy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, fmt.Errorf("store: get session policy: %w", err)
	}
	if policy != SessionExecutionOnDemand {
		return false, errors.New("store: session does not own its message history")
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO session_messages
		(id, session_id, author, content, source, external_id, included_in_context, delivery, reply_to_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		message.ID, message.SessionID, message.Author, message.Content, message.Source, message.ExternalID,
		message.IncludedInContext, message.Delivery, message.ReplyToID, toUnix(message.CreatedAt))
	if err != nil {
		return false, fmt.Errorf("store: insert session message: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if count != 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET history_revision = history_revision + 1 WHERE id = ?`, message.SessionID); err != nil {
			return false, fmt.Errorf("store: increment history revision: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: commit session message: %w", err)
	}
	return count != 0, nil
}

func (s *Store) SessionMessages(ctx context.Context, sessionID string) ([]SessionMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_id, author, content, source, external_id,
		included_in_context, delivery, reply_to_id, created_at FROM session_messages
		WHERE session_id = ? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: list session messages: %w", err)
	}
	defer rows.Close()
	var messages []SessionMessage
	for rows.Next() {
		var message SessionMessage
		var createdAt int64
		if err := rows.Scan(&message.ID, &message.SessionID, &message.Author, &message.Content,
			&message.Source, &message.ExternalID, &message.IncludedInContext, &message.Delivery,
			&message.ReplyToID, &createdAt); err != nil {
			return nil, fmt.Errorf("store: scan session message: %w", err)
		}
		message.CreatedAt = fromUnix(createdAt)
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

// EditLocalSessionMessage меняет только локальную заметку владельца, не внешний ответ.
func (s *Store) EditLocalSessionMessage(ctx context.Context, sessionID, messageID, content string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE session_messages SET content = ?
		WHERE session_id = ? AND id = ? AND author = 'owner' AND source = 'brigade' AND delivery = 'received'`,
		content, sessionID, messageID)
	if err != nil {
		return fmt.Errorf("store: edit local message: %w", err)
	}
	if err := affectedOne(result, "edit local message"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET history_revision = history_revision + 1 WHERE id = ?`, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteLocalSessionMessage удаляет только локальную заметку владельца.
func (s *Store) DeleteLocalSessionMessage(ctx context.Context, sessionID, messageID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM session_messages
		WHERE session_id = ? AND id = ? AND author = 'owner' AND source = 'brigade' AND delivery = 'received'`,
		sessionID, messageID)
	if err != nil {
		return fmt.Errorf("store: delete local message: %w", err)
	}
	if err := affectedOne(result, "delete local message"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET history_revision = history_revision + 1 WHERE id = ?`, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// LastShadowMessages возвращает превью одним запросом для списка переписок владельца.
type ShadowPreview struct {
	Message     SessionMessage
	UnreadCount uint32
}

func (s *Store) LastShadowMessages(ctx context.Context, userID string) (map[string]ShadowPreview, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.session_id, m.content, m.author, m.delivery, m.created_at,
		(SELECT COUNT(*) FROM session_messages unread WHERE unread.session_id = m.session_id
		 AND unread.author = 'contact' AND unread.seq > s.last_read_seq)
		FROM session_messages m
		JOIN (SELECT session_id, MAX(seq) AS seq FROM session_messages GROUP BY session_id) latest
		  ON latest.session_id = m.session_id AND latest.seq = m.seq
		JOIN sessions s ON s.id = m.session_id
		WHERE s.user_id = ? AND s.execution_policy = 'on_demand'`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list shadow previews: %w", err)
	}
	defer rows.Close()
	previews := make(map[string]ShadowPreview)
	for rows.Next() {
		var preview ShadowPreview
		var createdAt int64
		if err := rows.Scan(&preview.Message.SessionID, &preview.Message.Content, &preview.Message.Author, &preview.Message.Delivery, &createdAt, &preview.UnreadCount); err != nil {
			return nil, fmt.Errorf("store: scan shadow preview: %w", err)
		}
		preview.Message.CreatedAt = fromUnix(createdAt)
		previews[preview.Message.SessionID] = preview
	}
	return previews, rows.Err()
}

func (s *Store) SetMessageIncluded(ctx context.Context, sessionID, messageID string, included bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE session_messages SET included_in_context = ?
		WHERE session_id = ? AND id = ? AND included_in_context <> ?`, included, sessionID, messageID, included)
	if err != nil {
		return fmt.Errorf("store: set message inclusion: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET history_revision = history_revision + 1 WHERE id = ?`, sessionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) EditSessionDraft(ctx context.Context, sessionID, messageID, content string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE session_messages SET content = ? WHERE session_id = ? AND id = ? AND delivery = 'draft'`, content, sessionID, messageID)
	if err != nil {
		return fmt.Errorf("store: edit draft: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET history_revision = history_revision + 1 WHERE id = ?`, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetSessionDraftDelivery(ctx context.Context, sessionID, messageID string, from, to MessageDelivery) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE session_messages SET delivery = ? WHERE session_id = ? AND id = ? AND delivery = ?`, to, sessionID, messageID, from)
	if err != nil {
		return fmt.Errorf("store: set draft delivery: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET history_revision = history_revision + 1 WHERE id = ?`, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SessionMessage(ctx context.Context, sessionID, messageID string) (SessionMessage, error) {
	var message SessionMessage
	var createdAt int64
	err := s.db.QueryRowContext(ctx, `SELECT id, session_id, author, content, source, external_id,
		included_in_context, delivery, reply_to_id, created_at FROM session_messages WHERE session_id = ? AND id = ?`, sessionID, messageID).
		Scan(&message.ID, &message.SessionID, &message.Author, &message.Content, &message.Source,
			&message.ExternalID, &message.IncludedInContext, &message.Delivery, &message.ReplyToID, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionMessage{}, ErrNotFound
	}
	if err != nil {
		return SessionMessage{}, fmt.Errorf("store: get session message: %w", err)
	}
	message.CreatedAt = fromUnix(createdAt)
	return message, nil
}

func (s *Store) ExternalSessionMessage(ctx context.Context, sessionID, source, externalID string) (SessionMessage, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM session_messages WHERE session_id=? AND source=? AND external_id=?`, sessionID, source, externalID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionMessage{}, ErrNotFound
	}
	if err != nil {
		return SessionMessage{}, err
	}
	return s.SessionMessage(ctx, sessionID, id)
}

func (s *Store) UpdateExternalSessionMessage(ctx context.Context, sessionID, source, externalID, content string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE session_messages SET content=? WHERE session_id=? AND source=? AND external_id=? AND content<>?`, content, sessionID, source, externalID, content)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET history_revision=history_revision+1 WHERE id=?`, sessionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) MarkSessionDraftSent(ctx context.Context, sessionID, messageID, source, externalID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Business update исходящего сообщения может прийти раньше ответа sendMessage.
	// В таком случае сохраняем отредактированный черновик как единственную запись.
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_messages WHERE session_id=? AND source=? AND external_id=? AND author='owner' AND id<>?`,
		sessionID, source, externalID, messageID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE session_messages SET delivery='sent', source=?, external_id=?, included_in_context=1 WHERE session_id=? AND id=? AND delivery='sending'`, source, externalID, sessionID, messageID)
	if err != nil {
		return err
	}
	if err := affectedOne(result, "mark draft sent"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET history_revision=history_revision+1 WHERE id=?`, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LastExternalContactTime(ctx context.Context, sessionID, source string) (time.Time, error) {
	var timestamp int64
	err := s.db.QueryRowContext(ctx, `SELECT max(created_at) FROM session_messages WHERE session_id=? AND source=? AND author='contact'`, sessionID, source).Scan(&timestamp)
	if err != nil {
		return time.Time{}, err
	}
	return fromUnix(timestamp), nil
}

func (s *Store) RecoverSendingDrafts(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT session_id FROM session_messages WHERE delivery='sending'`)
	if err != nil {
		return err
	}
	var sessionIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		sessionIDs = append(sessionIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if _, err := tx.ExecContext(ctx, `UPDATE session_messages SET delivery='uncertain' WHERE delivery='sending'`); err != nil {
		return err
	}
	for _, id := range sessionIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET history_revision=history_revision+1 WHERE id=?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

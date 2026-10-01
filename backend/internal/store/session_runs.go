package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrSessionRunActive = errors.New("store: session run already active")
var ErrSessionHistoryChanged = errors.New("переписка изменилась; запустите подготовку черновика заново")

func (s *Store) StartSessionRun(ctx context.Context, run SessionRun) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var policy SessionExecutionPolicy
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT execution_policy, history_revision FROM sessions WHERE id = ?`, run.SessionID).Scan(&policy, &revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if policy != SessionExecutionOnDemand {
		return errors.New("store: session does not support one-shot runs")
	}
	if revision != run.InputRevision {
		return ErrSessionHistoryChanged
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM session_runs WHERE session_id = ? AND status = 'running'`, run.SessionID).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return ErrSessionRunActive
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO session_runs (id, session_id, status, input_revision, created_at) VALUES (?, ?, 'running', ?, ?)`,
		run.ID, run.SessionID, run.InputRevision, toUnix(run.CreatedAt))
	if err != nil {
		return fmt.Errorf("store: start session run: %w", err)
	}
	return tx.Commit()
}

// AddSessionRunDraft сохраняет черновик только пока входная история не изменилась.
func (s *Store) AddSessionRunDraft(ctx context.Context, run SessionRun, message SessionMessage) error {
	if message.SessionID != run.SessionID || message.Delivery != MessageDeliveryDraft {
		return errors.New("store: invalid session run draft")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET history_revision=history_revision+1
		WHERE id=? AND history_revision=? AND execution_policy='on_demand'`, run.SessionID, run.InputRevision)
	if err != nil {
		return fmt.Errorf("store: check draft input revision: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrSessionHistoryChanged
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO session_messages
		(id, session_id, author, content, source, external_id, included_in_context, delivery, reply_to_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, message.ID, run.SessionID, message.Author,
		message.Content, message.Source, message.ExternalID, message.IncludedInContext,
		message.Delivery, message.ReplyToID, toUnix(message.CreatedAt))
	if err != nil {
		return fmt.Errorf("store: insert draft: %w", err)
	}
	return tx.Commit()
}

func (s *Store) FinishSessionRun(ctx context.Context, runID, status, draftMessageID, failure string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE session_runs SET status = ?, draft_message_id = ?, error = ?, completed_at = ? WHERE id = ? AND status = 'running'`,
		status, draftMessageID, failure, toUnix(time.Now()), runID)
	if err != nil {
		return fmt.Errorf("store: finish session run: %w", err)
	}
	return affectedOne(result, "finish session run")
}

func (s *Store) ActiveSessionRun(ctx context.Context, sessionID string) (SessionRun, error) {
	return s.scanSessionRun(s.db.QueryRowContext(ctx, `SELECT id, session_id, status, input_revision, draft_message_id, error, created_at, completed_at
		FROM session_runs WHERE session_id = ? AND status = 'running'`, sessionID))
}

func (s *Store) LatestSessionRun(ctx context.Context, sessionID string) (SessionRun, error) {
	return s.scanSessionRun(s.db.QueryRowContext(ctx, `SELECT id, session_id, status, input_revision, draft_message_id, error, created_at, completed_at
		FROM session_runs WHERE session_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`, sessionID))
}

func (s *Store) RunningSessionRuns(ctx context.Context) ([]SessionRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_id, status, input_revision, draft_message_id, error, created_at, completed_at
		FROM session_runs WHERE status = 'running'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRun
	for rows.Next() {
		run, err := scanSessionRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (s *Store) CountRunningSessionRuns(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM session_runs WHERE status = 'running'`).Scan(&count)
	return count, err
}

func (s *Store) scanSessionRun(row *sql.Row) (SessionRun, error) {
	run, err := scanSessionRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionRun{}, ErrNotFound
	}
	return run, err
}

func scanSessionRun(row rowScanner) (SessionRun, error) {
	var run SessionRun
	var createdAt, completedAt int64
	err := row.Scan(&run.ID, &run.SessionID, &run.Status, &run.InputRevision, &run.DraftMessageID, &run.Error, &createdAt, &completedAt)
	if err != nil {
		return SessionRun{}, err
	}
	run.CreatedAt = fromUnix(createdAt)
	if completedAt != 0 {
		run.CompletedAt = fromUnix(completedAt)
	}
	return run, nil
}

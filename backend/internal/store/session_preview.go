package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
	"unicode"
)

// UpdateSessionPreview сохраняет ограниченную проекцию ACP-реплики, не дублируя
// историю агента. Время имеет наносекундную точность для порядка соседних turn'ов.
// При неизвестном времени снимок sess используется для compare-and-swap: поздний
// backfill или refresh после reattach не заменяет preview нового turn.
func (s *Store) UpdateSessionPreview(ctx context.Context, sess Session, content string, at time.Time, author MessageAuthor) error {
	if author != MessageAuthorOwner && author != MessageAuthorAgent {
		return nil
	}
	const maxRunes = 300
	text := make([]rune, 0, maxRunes)
	space := false
	for _, char := range content {
		if unicode.IsSpace(char) {
			space = len(text) > 0
			continue
		}
		if unicode.IsControl(char) {
			continue
		}
		if space {
			if len(text)+1 >= maxRunes {
				break
			}
			text = append(text, ' ')
			space = false
		}
		text = append(text, char)
		if len(text) == maxRunes {
			break
		}
	}
	if len(text) == 0 {
		return nil
	}
	var timestamp sql.NullInt64
	condition := " AND (last_message_at IS NULL OR last_message_at <= ?)"
	var args []any
	if at.IsZero() {
		if string(text) == sess.LastMessage && author == sess.LastMessageAuthor {
			return nil
		}
		// Пустое preview заполняется только при неизвестном времени. Непустой
		// снимок используется только refresh после подключения к живому агенту.
		var previousAt sql.NullInt64
		if sess.LastMessage != "" && !sess.LastMessageAt.IsZero() {
			previousAt = sql.NullInt64{Int64: sess.LastMessageAt.UnixNano(), Valid: true}
		}
		condition = " AND last_message = ? AND last_message_at IS ?"
		args = []any{sess.LastMessage, previousAt}
	} else {
		timestamp = sql.NullInt64{Int64: at.UnixNano(), Valid: true}
		args = []any{timestamp}
	}
	args = append([]any{string(text), timestamp, author, sess.ID}, args...)
	_, err := s.db.ExecContext(ctx, `UPDATE sessions
		SET last_message = ?, last_message_at = ?, last_message_author = ?
		WHERE id = ? AND kind = 'acp' AND execution_policy = 'persistent'`+condition, args...)
	if err != nil {
		return fmt.Errorf("store: update session preview: %w", err)
	}
	return nil
}

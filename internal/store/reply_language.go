package store

import (
	"context"
	"database/sql"
	"errors"

	"vista/internal/replylanguage"
)

// ReplyLanguage is session metadata, separate from the model's message history.
// The latest preference also applies when a queued message starts a new run.
func (s *Store) ReplyLanguage(ctx context.Context, sessionID string) (string, error) {
	var code string
	err := s.db.QueryRowContext(ctx, `SELECT json_extract(data,'$.language') FROM events WHERE session_id=? AND kind='reply_language' ORDER BY sequence DESC LIMIT 1`, sessionID).Scan(&code)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return code, err
}

func (s *Store) SetReplyLanguage(ctx context.Context, sessionID, code string) error {
	if replylanguage.Name(code) == "" {
		return errors.New("unsupported reply language")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current string
	err = tx.QueryRowContext(ctx, `SELECT json_extract(data,'$.language') FROM events WHERE session_id=? AND kind='reply_language' ORDER BY sequence DESC LIMIT 1`, sessionID).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if current == code {
		return nil
	}
	if _, err := appendEvent(ctx, tx, sessionID, "reply_language", map[string]string{"language": code}); err != nil {
		return err
	}
	return tx.Commit()
}

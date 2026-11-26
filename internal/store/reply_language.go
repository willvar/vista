package store

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"vista/internal/replylanguage"
)

// Reply language is session metadata, not part of a restorable timeline.
// A branch without an override can still read the language at its fork.
func replyLanguage(ctx context.Context, tx *gorm.DB, sessionID string) (string, error) {
	var code string
	err := tx.WithContext(ctx).Raw(`SELECT COALESCE(
 (SELECT json_extract(data,'$.language') FROM events WHERE session_id=? AND kind='reply_language' ORDER BY sequence DESC LIMIT 1),
 (SELECT json_extract(data,'$.language') FROM history_events WHERE session_id=? AND kind='reply_language' ORDER BY sequence DESC LIMIT 1),'')`, sessionID, sessionID).Row().Scan(&code)
	return code, err
}

func (s *Store) ReplyLanguage(ctx context.Context, sessionID string) (string, error) {
	return replyLanguage(ctx, s.orm, sessionID)
}

func setReplyLanguage(ctx context.Context, tx *gorm.DB, sessionID, code string) error {
	if replylanguage.Name(code) == "" {
		return errors.New("unsupported reply language")
	}
	current, err := replyLanguage(ctx, tx, sessionID)
	if err != nil || current == code {
		return err
	}
	_, err = appendEvent(ctx, tx, sessionID, "reply_language", map[string]string{"language": code})
	return err
}

func (s *Store) SetReplyLanguage(ctx context.Context, sessionID, code string) error {
	tx, err := s.begin(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setReplyLanguage(ctx, tx, sessionID, code); err != nil {
		return err
	}
	return tx.Commit().Error
}

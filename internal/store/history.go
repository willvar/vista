package store

import (
	"context"
	"database/sql"
	"errors"
)

type HistoryItem struct {
	Sequence   int64  `json:"sequence"`
	Role       string `json:"role"`
	Bytes      int    `json:"bytes"`
	Preview    string `json:"preview"`
	ImageCount int    `json:"imageCount,omitempty"`
}

func (s *Store) HasMessagesBefore(ctx context.Context, sessionID string, before int64) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM messages WHERE session_id=? AND sequence<?)", sessionID, before).Scan(&exists)
	return exists, err
}

func (s *Store) HistoryIndex(ctx context.Context, sessionID string, before int64, limit int) ([]HistoryItem, error) {
	if before <= 0 || limit < 1 || limit > 10 {
		return nil, errors.New("invalid history index range")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT sequence,role,CASE WHEN output_bytes>0 THEN output_bytes ELSE length(CAST(content AS BLOB)) END,substr(content,1,100),(SELECT COUNT(*) FROM message_resources WHERE message_id=messages.id) FROM messages WHERE session_id=? AND sequence<? ORDER BY sequence DESC LIMIT ?", sessionID, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []HistoryItem{}
	for rows.Next() {
		var item HistoryItem
		if err := rows.Scan(&item.Sequence, &item.Role, &item.Bytes, &item.Preview, &item.ImageCount); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) HistoryMessage(ctx context.Context, sessionID string, sequence int64) (Message, error) {
	var message Message
	message.SessionID = sessionID
	err := s.db.QueryRowContext(ctx, "SELECT id,run_id,sequence,role,content,created_at,provider_id,model_id,model_key,output_turn,output_bytes,(SELECT COUNT(*) FROM message_resources WHERE message_id=messages.id) FROM messages WHERE session_id=? AND sequence=?", sessionID, sequence).Scan(&message.ID, &message.RunID, &message.Sequence, &message.Role, &message.Content, &message.CreatedAt, &message.Provider, &message.ModelID, &message.ModelKey, &message.OutputTurn, &message.OutputBytes, &message.ImageCount)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	return message, err
}

func (s *Store) NextHistoryMessage(ctx context.Context, sessionID string, after, before int64) (Message, error) {
	var message Message
	message.SessionID = sessionID
	err := s.db.QueryRowContext(ctx, "SELECT id,run_id,sequence,role,content,created_at,provider_id,model_id,model_key,output_turn,output_bytes,(SELECT COUNT(*) FROM message_resources WHERE message_id=messages.id) FROM messages WHERE session_id=? AND sequence>? AND sequence<? ORDER BY sequence LIMIT 1", sessionID, after, before).Scan(&message.ID, &message.RunID, &message.Sequence, &message.Role, &message.Content, &message.CreatedAt, &message.Provider, &message.ModelID, &message.ModelKey, &message.OutputTurn, &message.OutputBytes, &message.ImageCount)
	return message, err
}

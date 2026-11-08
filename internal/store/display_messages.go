package store

import (
	"context"
	"fmt"
	"sort"
)

// Display pages merge formal messages with already committed compaction
// summaries. Model history continues to use the canonical messages/checkpoints.
func (s *Store) DisplayMessagesBefore(ctx context.Context, sessionID string, before int64, limit int) ([]Message, error) {
	return s.displayMessages(ctx, sessionID, before, limit, false)
}

func (s *Store) DisplayMessagesAfter(ctx context.Context, sessionID string, after int64, limit int) ([]Message, error) {
	return s.displayMessages(ctx, sessionID, after, limit, true)
}

func (s *Store) displayMessages(ctx context.Context, sessionID string, cursor int64, limit int, forward bool) ([]Message, error) {
	var messages []Message
	var err error
	if forward {
		messages, err = s.MessagesAfter(ctx, sessionID, cursor, limit)
	} else {
		messages, err = s.MessagesBefore(ctx, sessionID, cursor, limit)
	}
	if err != nil {
		return nil, err
	}
	query := `SELECT sequence,COALESCE(json_extract(data,'$.runId'),''),json_extract(data,'$.summary'),created_at
 FROM events WHERE session_id=? AND kind='context_notice'
 AND COALESCE(json_extract(data,'$.depth'),0)=0
 AND json_type(data,'$.summary')='text' AND length(trim(json_extract(data,'$.summary')))>0`
	args := []any{sessionID}
	if forward {
		query += " AND sequence>?"
		args = append(args, cursor)
	} else if cursor > 0 {
		query += " AND sequence<?"
		args = append(args, cursor)
	}
	// A full formal-message page already bounds the only summaries that can
	// enter this page; do not preload summaries outside that visible range.
	if len(messages) == limit {
		if forward {
			query += " AND sequence<?"
			args = append(args, messages[len(messages)-1].Sequence)
		} else {
			query += " AND sequence>?"
			args = append(args, messages[0].Sequence)
		}
	}
	query += " ORDER BY sequence"
	if !forward {
		query += " DESC"
	}
	query += " LIMIT ?"
	args = append(args, limit)
	rows, err := s.reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		message := Message{SessionID: sessionID, Role: "assistant", ContextSummary: true}
		if err := rows.Scan(&message.Sequence, &message.RunID, &message.Content, &message.CreatedAt); err != nil {
			return nil, err
		}
		message.ID = fmt.Sprintf("context-summary/%d", message.Sequence)
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(messages, func(i, j int) bool { return messages[i].Sequence < messages[j].Sequence })
	if len(messages) > limit {
		if forward {
			messages = messages[:limit]
		} else {
			messages = messages[len(messages)-limit:]
		}
	}
	return messages, nil
}

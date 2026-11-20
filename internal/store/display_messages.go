package store

import (
	"context"
	"fmt"
	"sort"
)

// Display pages project committed compaction summaries, never internal plans.
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
	query := `WITH display AS (
 SELECT e.sequence,COALESCE(json_extract(e.data,'$.runId'),'') AS run_id,e.kind,e.created_at,
 COALESCE(json_extract(e.data,'$.summary'),'') AS summary,
 COALESCE(json_extract(e.data,'$.outputTurn'),0) AS output_turn,COALESCE(json_extract(e.data,'$.outputBytes'),0) AS output_bytes,
 CASE WHEN e.kind='context_notice' THEN COALESCE((SELECT MAX(start.sequence) FROM history_events start
  WHERE start.session_id=e.session_id AND json_extract(start.data,'$.runId')=json_extract(e.data,'$.runId')
  AND start.sequence<e.sequence AND COALESCE(json_extract(start.data,'$.depth'),0)=0
  AND (start.kind='context_notice' OR (start.kind='context_status' AND json_extract(start.data,'$.phase')='compacting'))),0) ELSE 0 END AS results_after,
  0 AS reserved
 FROM history_events e WHERE e.session_id=? AND e.kind='context_notice'
 AND COALESCE(json_extract(e.data,'$.depth'),0)=0 AND json_type(e.data,'$.summary')='text' AND length(trim(json_extract(e.data,'$.summary')))>0`

	args := []any{sessionID}
	if forward {
		query += " AND e.sequence>?"
		args = append(args, cursor)
	} else if cursor > 0 {
		query += " AND e.sequence<?"
		args = append(args, cursor)
	}
	// Do not read projections beyond the visible message page.
	if len(messages) == limit {
		if forward {
			query += " AND e.sequence<=?"
			args = append(args, messages[len(messages)-1].Sequence)
		} else {
			query += " AND e.sequence>=?"
			args = append(args, messages[0].Sequence)
		}
	}
	// Summary results carry a provider/model ID, but that pair can be shared
	// by several connections. Retain the exact request key even off the event page.
	query += `) SELECT sequence,run_id,kind,created_at,summary,results_after,output_turn,output_bytes,
 CASE WHEN kind='context_notice' THEN COALESCE((SELECT json_extract(request.data,'$.key') FROM history_events request
  WHERE request.session_id=? AND request.kind='model_request' AND json_extract(request.data,'$.runId')=display.run_id
  AND json_extract(request.data,'$.purpose')='summary' AND COALESCE(json_extract(request.data,'$.depth'),0)=0
  AND COALESCE(json_extract(request.data,'$.taskId'),'main') IN ('','main')
  AND request.sequence>display.results_after AND request.sequence<display.sequence ORDER BY request.sequence DESC LIMIT 1),'') ELSE '' END
 FROM display ORDER BY sequence`
	args = append(args, sessionID)
	if !forward {
		query += " DESC"
	}
	query += " LIMIT ?"
	args = append(args, limit)
	rows, err := s.orm.WithContext(ctx).Raw(query, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		message := Message{SessionID: sessionID, Role: "assistant"}
		var kind string
		if err := rows.Scan(&message.Sequence, &message.RunID, &kind, &message.CreatedAt, &message.Content, &message.ResultsAfter, &message.OutputTurn, &message.OutputBytes, &message.ModelKey); err != nil {
			return nil, err
		}
		message.ContextSummary = true
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

package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// Display pages project committed compaction summaries and final checklists.
// A final checklist shares its run_finished page cursor with the formal reply,
// but retains its update sequence for chronological display. Paging cannot
// split the two bodies. Canonical model history is unchanged.
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
 COALESCE(json_extract(e.data,'$.status'),'') AS status,
 CASE WHEN e.kind='context_notice' THEN COALESCE((SELECT MAX(start.sequence) FROM events start
  WHERE start.session_id=e.session_id AND json_extract(start.data,'$.runId')=json_extract(e.data,'$.runId')
  AND start.sequence<e.sequence AND COALESCE(json_extract(start.data,'$.depth'),0)=0
  AND (start.kind='context_notice' OR (start.kind='context_status' AND json_extract(start.data,'$.phase')='compacting'))),0) ELSE 0 END AS results_after,
 CASE WHEN e.kind='run_finished' THEN (SELECT json_object('items',COALESCE(json_extract(c.data,'$.items'),json_extract(c.data,'$.checklist.items')),'updatedAt',c.created_at,'updatedSequence',c.sequence) FROM events c
  WHERE c.session_id=e.session_id AND c.kind='checklist' AND json_extract(c.data,'$.runId')=json_extract(e.data,'$.runId')
  AND c.sequence<e.sequence AND COALESCE(json_extract(c.data,'$.depth'),0)=0
  AND COALESCE(json_extract(c.data,'$.taskId'),'main') IN ('','main') ORDER BY c.sequence DESC LIMIT 1) END AS checklist
 FROM events e WHERE e.session_id=? AND ((e.kind='context_notice'
 AND COALESCE(json_extract(e.data,'$.depth'),0)=0 AND json_type(e.data,'$.summary')='text' AND length(trim(json_extract(e.data,'$.summary')))>0)
 OR e.kind='run_finished')`
	args := []any{sessionID}
	if forward {
		query += " AND e.sequence>?"
		args = append(args, cursor)
	} else if cursor > 0 {
		query += " AND e.sequence<?"
		args = append(args, cursor)
	}
	// Include the boundary itself: a checklist can share a formal reply's
	// sequence, but projections outside the visible page are not read.
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
	query += `) SELECT sequence,run_id,kind,created_at,summary,status,results_after,COALESCE(checklist,'{}'),
 CASE WHEN kind='context_notice' THEN COALESCE((SELECT json_extract(request.data,'$.key') FROM events request
  WHERE request.session_id=? AND request.kind='model_request' AND json_extract(request.data,'$.runId')=display.run_id
  AND json_extract(request.data,'$.purpose')='summary' AND COALESCE(json_extract(request.data,'$.depth'),0)=0
  AND COALESCE(json_extract(request.data,'$.taskId'),'main') IN ('','main')
  AND request.sequence>display.results_after AND request.sequence<display.sequence ORDER BY request.sequence DESC LIMIT 1),'') ELSE '' END
 FROM display WHERE kind='context_notice' OR json_array_length(json_extract(checklist,'$.items'))>0 ORDER BY sequence`
	args = append(args, sessionID)
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
	bySequence := make(map[int64]int, len(messages))
	for index, message := range messages {
		bySequence[message.Sequence] = index
	}
	for rows.Next() {
		message := Message{SessionID: sessionID, Role: "assistant"}
		var kind, status, items string
		if err := rows.Scan(&message.Sequence, &message.RunID, &kind, &message.CreatedAt, &message.Content, &status, &message.ResultsAfter, &items, &message.ModelKey); err != nil {
			return nil, err
		}
		if kind == "context_notice" {
			message.ContextSummary = true
			message.ID = fmt.Sprintf("context-summary/%d", message.Sequence)
		} else {
			message.ID = fmt.Sprintf("checklist-summary/%d", message.Sequence)
			message.Content = ""
			message.Checklist = &ChecklistSummary{Status: status}
			if err := json.Unmarshal([]byte(items), message.Checklist); err != nil {
				return nil, err
			}
			if index, exists := bySequence[message.Sequence]; exists {
				messages[index].Checklist = message.Checklist
				continue
			}
		}
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

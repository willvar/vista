package store

import (
	"context"
	"strings"
)

// MessageResultBoundaries includes replies outside the current message page, so
// paging cannot charge an earlier reply's model calls to a later one.
func (s *Store) MessageResultBoundaries(ctx context.Context, sessionID string, messageIDs []string) (map[string]int64, error) {
	boundaries := make(map[string]int64, len(messageIDs))
	if len(messageIDs) == 0 {
		return boundaries, nil
	}
	var rows []struct {
		ID           string
		ResultsAfter int64
	}
	previous := s.orm.Table("history_messages AS previous").Select("MAX(previous.sequence)").
		Where("previous.session_id = reply.session_id AND previous.run_id = reply.run_id AND previous.role = ? AND previous.sequence < reply.sequence", "assistant")
	err := s.orm.WithContext(ctx).Table("history_messages AS reply").
		Select("reply.id, COALESCE((?), 0) AS results_after", previous).
		Where("reply.session_id = ? AND reply.id IN ? AND reply.role = ?", sessionID, messageIDs, "assistant").Scan(&rows).Error
	for _, row := range rows {
		boundaries[row.ID] = row.ResultsAfter
	}
	return boundaries, err
}

// MessagePageResults enriches display pages without reading model calls that
// cannot belong to any of their replies. Summary rows have their own bounds.
func (s *Store) MessagePageResults(ctx context.Context, sessionID string, items []Message) ([]Event, map[string]int64, map[string]string, error) {
	var messageIDs, runIDs []string
	seen := make(map[string]bool)
	for _, item := range items {
		if item.Role != "assistant" || item.RunID == "" {
			continue
		}
		if !item.ContextSummary {
			messageIDs = append(messageIDs, item.ID)
		}
		if !seen[item.RunID] {
			seen[item.RunID] = true
			runIDs = append(runIDs, item.RunID)
		}
	}
	bounds, err := s.MessageResultBoundaries(ctx, sessionID, messageIDs)
	if err != nil {
		return nil, nil, nil, err
	}
	var ranges []replyResultRange
	for _, item := range items {
		if item.Role == "assistant" && item.RunID != "" {
			after := bounds[item.ID]
			if item.ContextSummary {
				after = item.ResultsAfter
			}
			ranges = append(ranges, replyResultRange{item.RunID, after, item.Sequence})
		}
	}
	events, err := s.modelResultEvents(ctx, sessionID, ranges)
	if err != nil {
		return nil, nil, nil, err
	}
	efforts, err := s.RunReasoningEfforts(ctx, runIDs)
	return events, bounds, efforts, err
}

type replyResultRange struct {
	runID          string
	after, through int64
}

func (s *Store) modelResultEvents(ctx context.Context, sessionID string, ranges []replyResultRange) ([]Event, error) {
	out := []Event{}
	if len(ranges) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(ranges)*3+2)
	for _, r := range ranges {
		args = append(args, r.runID, r.after, r.through)
	}
	args = append(args, sessionID, sessionID)
	// A summary can overlap a formal reply's range. Deduplicate only sequences
	// and fetch each stored body once, in the original timeline order.
	query := `WITH bounds(run_id,after,through) AS (VALUES ` + strings.TrimSuffix(strings.Repeat("(?,?,?),", len(ranges)), ",") + `)
 SELECT sequence,kind,CAST(data AS BLOB) AS data,created_at FROM history_events WHERE session_id=? AND sequence IN (
  SELECT DISTINCT e.sequence FROM bounds CROSS JOIN history_events e
  WHERE e.session_id=? AND e.kind='model_result' AND json_extract(e.data,'$.runId')=bounds.run_id
    AND e.sequence>bounds.after AND e.sequence<=bounds.through
 ) ORDER BY sequence`
	rows, err := s.orm.WithContext(ctx).Raw(query, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		event := Event{SessionID: sessionID}
		if err := rows.Scan(&event.Sequence, &event.Kind, &event.Data, &event.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

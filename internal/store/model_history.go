package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"vista/internal/workerproto"
)

// RecordModelHistory is idempotent at each model/tool boundary. A late result
// cannot add model context after the run has been finalized.
func (s *Store) RecordModelHistory(ctx context.Context, sessionID string, result workerproto.Result) (*Event, error) {
	if result.RunID == "" || result.Turn <= 0 || result.Depth != 0 || result.ParentTaskID != "" || result.TaskID != "" && result.TaskID != "main" || result.History == nil || result.History.Path == "" || result.History.Index < 0 || result.History.Tokens < 0 {
		return nil, errors.New("invalid model history")
	}
	switch result.Status {
	case "assistant", "tool", "join", "question":
	default:
		return nil, errors.New("invalid model history part")
	}
	if result.Status != "tool" && result.History.Index != 0 {
		return nil, errors.New("invalid model history index")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	active, err := executionActive(ctx, tx, sessionID, result.RunID, "main")
	if err != nil || !active {
		return nil, err
	}
	var existing Event
	existing.SessionID = sessionID
	err = tx.QueryRowContext(ctx, `SELECT sequence,kind,CAST(data AS BLOB),created_at FROM events
 WHERE session_id=? AND kind='model_history' AND json_extract(data,'$.runId')=? AND json_extract(data,'$.turn')=?
 AND json_extract(data,'$.status')=? AND COALESCE(json_extract(data,'$.history.index'),0)=?`,
		sessionID, result.RunID, result.Turn, result.Status, result.History.Index).Scan(&existing.Sequence, &existing.Kind, &existing.Data, &existing.CreatedAt)
	if err == nil {
		var saved workerproto.Result
		if err := json.Unmarshal(existing.Data, &saved); err != nil {
			return nil, err
		}
		if saved.History == nil || *saved.History != *result.History {
			return nil, errors.New("conflicting model history")
		}
		return &existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	result.Kind, result.AckID = "model_history", ""
	event, err := appendEvent(ctx, tx, sessionID, "model_history", result)
	if err != nil {
		return nil, err
	}
	return &event, tx.Commit()
}

type ContextEntry struct {
	Sequence int64
	Message  *Message
	History  *workerproto.Result
}

// ContextEntries merges formal inputs with authoritative model messages. A
// formal reply is only a display projection when its model turn was retained.
// Checkpoints have separate message and event cutoffs: compaction can happen
// after tools, while the latest formal message is still the original input.
func (s *Store) ContextEntries(ctx context.Context, sessionID string, checkpoint *workerproto.ContextCheckpoint, before int64) ([]ContextEntry, error) {
	var throughMessage, throughEvent int64
	if checkpoint != nil {
		throughMessage, throughEvent = checkpoint.ThroughSequence, checkpoint.Sequence
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence,kind,data FROM (
 SELECT m.sequence,'message' AS kind,json_object('id',m.id,'runId',m.run_id,'role',m.role,'content',m.content,'outputTurn',m.output_turn,'outputBytes',m.output_bytes,
   'questionCallId',json_extract(input_event.data,'$.questionCallId')) AS data
 FROM messages m LEFT JOIN events input_event ON input_event.session_id=m.session_id AND input_event.sequence=m.sequence AND input_event.kind='message'
 WHERE m.session_id=? AND m.sequence>? AND (?=0 OR m.sequence<?)
 AND (m.role<>'assistant' OR NOT EXISTS(
   SELECT 1 FROM events e WHERE e.session_id=m.session_id AND e.kind='model_history' AND json_extract(e.data,'$.runId')=m.run_id
   AND ((m.output_turn>0 AND json_extract(e.data,'$.turn')=m.output_turn AND json_extract(e.data,'$.status')='assistant')
     OR (m.output_turn=0 AND json_extract(e.data,'$.status')='question' AND e.sequence<m.sequence
       AND NOT EXISTS(SELECT 1 FROM events f WHERE f.session_id=m.session_id AND f.sequence=m.sequence AND f.kind='run_finished' AND json_extract(f.data,'$.status')='failed')))))
 UNION ALL
 SELECT sequence,kind,data FROM events WHERE session_id=? AND kind='model_history' AND sequence>? AND (?=0 OR sequence<?)
 ) ORDER BY sequence`, sessionID, throughMessage, before, before, sessionID, throughEvent, before, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []ContextEntry
	for rows.Next() {
		var entry ContextEntry
		var kind string
		var raw []byte
		if err := rows.Scan(&entry.Sequence, &kind, &raw); err != nil {
			return nil, err
		}
		if kind == "message" {
			entry.Message = &Message{SessionID: sessionID, Sequence: entry.Sequence}
			if err := json.Unmarshal(raw, entry.Message); err != nil {
				return nil, err
			}
		} else {
			entry.History = &workerproto.Result{}
			if err := json.Unmarshal(raw, entry.History); err != nil {
				return nil, err
			}
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

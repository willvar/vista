package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"vista/internal/workerproto"
)

func (s *Store) RecordRuntimeEvent(ctx context.Context, sessionID string, notice workerproto.RuntimeEvent) (*Event, bool, error) {
	if notice.ID == "" || notice.RunID == "" || notice.Recipient == "" || notice.SourceID == "" {
		return nil, false, errors.New("invalid runtime event")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var sequence int64
	var status string
	err = tx.QueryRowContext(ctx, `SELECT sequence,status FROM runtime_events WHERE session_id=? AND event_id=?`, sessionID, notice.ID).Scan(&sequence, &status)
	if err == nil {
		var event Event
		event.SessionID = sessionID
		err = tx.QueryRowContext(ctx, `SELECT sequence,kind,CAST(data AS BLOB),created_at FROM events WHERE session_id=? AND sequence=?`, sessionID, sequence).Scan(&event.Sequence, &event.Kind, &event.Data, &event.CreatedAt)
		return &event, status == "pending", err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	var enabled bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r JOIN sessions s ON s.id=r.session_id
 WHERE r.id=? AND r.session_id=? AND s.is_open=1 AND r.status IN ('running','succeeded')
 AND NOT EXISTS(SELECT 1 FROM runs newer WHERE newer.session_id=r.session_id AND newer.rowid>=r.rowid AND newer.status IN ('failed','interrupted')))`, notice.RunID, sessionID).Scan(&enabled)
	if err != nil {
		return nil, false, err
	}
	// A background operation may outlive its child owner. Deliver its eventual
	// result to the nearest live ancestor, retaining the source identity.
	for notice.Recipient != "main" {
		var parent, taskStatus string
		err = tx.QueryRowContext(ctx, `SELECT parent_task_id,status FROM agent_tasks WHERE session_id=? AND run_id=? AND task_id=?`, sessionID, notice.RunID, notice.Recipient).Scan(&parent, &taskStatus)
		if errors.Is(err, sql.ErrNoRows) {
			enabled = false
			break
		}
		if err != nil {
			return nil, false, err
		}
		if taskStatus == "started" {
			var background bool
			if err := tx.QueryRowContext(ctx, `SELECT background FROM agent_tasks WHERE session_id=? AND run_id=? AND task_id=?`, sessionID, notice.RunID, notice.Recipient).Scan(&background); err != nil {
				return nil, false, err
			}
			var open bool
			if err := tx.QueryRowContext(ctx, `SELECT is_open FROM sessions WHERE id=?`, sessionID).Scan(&open); err != nil {
				return nil, false, err
			}
			enabled = open && (enabled || background)
			break
		}
		if parent == "" {
			notice.Recipient = "main"
		} else {
			notice.Recipient = parent
		}
	}
	status = "suppressed"
	if enabled {
		status = "pending"
	}
	event, err := appendEvent(ctx, tx, sessionID, "runtime_event", notice)
	if err != nil {
		return nil, false, err
	}
	data, err := json.Marshal(notice)
	if err != nil {
		return nil, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_events(session_id,run_id,event_id,recipient,sequence,status,data) VALUES(?,?,?,?,?,?,?)`, sessionID, notice.RunID, notice.ID, notice.Recipient, event.Sequence, status, data)
	if err != nil {
		return nil, false, err
	}
	return &event, enabled, tx.Commit()
}

func (s *Store) PendingRuntimeEvents(ctx context.Context, sessionID, runID, recipient string) ([]workerproto.RuntimeEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT CAST(e.data AS BLOB) FROM runtime_events e
 WHERE e.session_id=? AND e.recipient=? AND e.status='pending' AND (?='main' OR e.run_id=?)
 AND (EXISTS(SELECT 1 FROM runs r WHERE r.id=? AND r.session_id=e.session_id AND r.status='running')
 OR EXISTS(SELECT 1 FROM agent_tasks t WHERE t.run_id=? AND t.session_id=e.session_id AND t.task_id=? AND t.background=1 AND t.status='started'))
 ORDER BY e.sequence LIMIT 8`, sessionID, recipient, recipient, runID, runID, runID, recipient)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []workerproto.RuntimeEvent
	bytes := 0
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var event workerproto.RuntimeEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, err
		}
		if len(events) > 0 && bytes+len(raw) > 40<<10 {
			break
		}
		bytes += len(raw)
		events = append(events, event)
	}
	return events, rows.Err()
}

// PendingRuntimeSessions is used when a completion releases run capacity, not
// by a timer. A temporarily inadmissible continuation remains durable.
func (s *Store) PendingRuntimeSessions(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT e.session_id FROM runtime_events e JOIN sessions s ON s.id=e.session_id WHERE e.status='pending' AND e.recipient='main' AND s.is_open=1 ORDER BY e.session_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) HasPendingRuntimeEvents(ctx context.Context, sessionID string) (bool, error) {
	var pending bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_events WHERE session_id=? AND recipient='main' AND status='pending')`, sessionID).Scan(&pending)
	return pending, err
}

func deliverRuntimeEvent(ctx context.Context, tx *sql.Tx, sessionID, runID, recipient, eventID string) error {
	result, err := tx.ExecContext(ctx, `UPDATE runtime_events SET status='delivered',delivered_run_id=? WHERE session_id=? AND event_id=? AND recipient=? AND status='pending'`, runID, sessionID, eventID, recipient)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("runtime event is no longer pending for this recipient")
	}
	return nil
}

func (s *Store) DeliverChildRuntimeEvent(ctx context.Context, sessionID, runID, recipient, eventID string) (*Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var existing Event
	existing.SessionID = sessionID
	err = tx.QueryRowContext(ctx, `SELECT sequence,kind,CAST(data AS BLOB),created_at FROM events WHERE session_id=? AND kind='runtime_delivered' AND json_extract(data,'$.runId')=? AND json_extract(data,'$.taskId')=? AND json_extract(data,'$.eventId')=? ORDER BY sequence LIMIT 1`, sessionID, runID, recipient, eventID).Scan(&existing.Sequence, &existing.Kind, &existing.Data, &existing.CreatedAt)
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	active, err := executionActive(ctx, tx, sessionID, runID, recipient)
	if err != nil || !active {
		return nil, err
	}
	if err := deliverRuntimeEvent(ctx, tx, sessionID, runID, recipient, eventID); err != nil {
		return nil, err
	}
	event, err := appendEvent(ctx, tx, sessionID, "runtime_delivered", map[string]string{"runId": runID, "taskId": recipient, "eventId": eventID})
	if err != nil {
		return nil, err
	}
	return &event, tx.Commit()
}

func suppressRuntimeEvents(ctx context.Context, tx *sql.Tx, sessionID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE runtime_events SET status='suppressed' WHERE status='pending' AND (?='' OR session_id=?)`, sessionID, sessionID)
	return err
}

func (s *Store) SuppressRuntimeEvents(ctx context.Context, sessionID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := suppressRuntimeEvents(ctx, tx, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// BeginRuntimeRun creates an execution without inventing a user message. The
// durable completion is delivered through the inbox after context restoration.
func (s *Store) BeginRuntimeRun(ctx context.Context, sessionID, runID, modelKey string) (*Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var eligible bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sessions s WHERE s.id=? AND s.is_open=1 AND s.pending_kind='' AND s.input_queue_paused=0
 AND NOT EXISTS(SELECT 1 FROM runs r WHERE r.session_id=s.id AND r.status='running')
 AND NOT EXISTS(SELECT 1 FROM queued_inputs q WHERE q.session_id=s.id AND q.status='queued')
 AND EXISTS(SELECT 1 FROM runtime_events e WHERE e.session_id=s.id AND e.recipient='main' AND e.status='pending'))`, sessionID).Scan(&eligible)
	if err != nil || !eligible {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runs(id,session_id,model_key,status,created_at) VALUES(?,?,?,'running',?)`, runID, sessionID, modelKey, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	event, err := appendEvent(ctx, tx, sessionID, "run_started", map[string]any{"runId": runID, "runtime": true})
	if err != nil {
		return nil, err
	}
	return &event, tx.Commit()
}

func (s *Store) SaveCompletedReply(ctx context.Context, sessionID, runID string, reply CompletedReply) (*Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	active, err := executionActive(ctx, tx, sessionID, runID, "main")
	if err != nil || !active {
		return nil, err
	}
	if err := appendCompletedReply(ctx, tx, sessionID, runID, reply); err != nil {
		return nil, err
	}
	var event Event
	event.SessionID = sessionID
	err = tx.QueryRowContext(ctx, `SELECT sequence,kind,CAST(data AS BLOB),created_at FROM events WHERE session_id=? AND kind='message' AND json_extract(data,'$.runId')=? AND json_extract(data,'$.outputTurn')=? ORDER BY sequence DESC LIMIT 1`, sessionID, runID, reply.Output.Turn).Scan(&event.Sequence, &event.Kind, &event.Data, &event.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &event, tx.Commit()
}

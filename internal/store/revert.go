package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"vista/internal/workerproto"
)

func (s *Store) RevertTarget(ctx context.Context, sessionID, messageID string) (Message, error) {
	var sequence int64
	err := s.db.QueryRowContext(ctx, `SELECT sequence FROM messages WHERE session_id=? AND id=? AND role='user'`, sessionID, messageID).Scan(&sequence)
	if err != nil {
		return Message{}, err
	}
	return s.HistoryMessage(ctx, sessionID, sequence)
}

// Revert commits the new effective history after session execution has stopped.
// The terminal marker keeps SSE sequence numbers monotonic even when all prior
// messages are removed. Deleted context/checkpoint/output refs cannot be read
// through model history tools or resurrected by a replacement Worker.
func (s *Store) Revert(ctx context.Context, sessionID, messageID string, sequence int64) (Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages WHERE session_id=? AND id=? AND sequence=? AND role='user')`, sessionID, messageID, sequence).Scan(&exists); err != nil {
		return Event{}, err
	}
	if !exists {
		return Event{}, sql.ErrNoRows
	}
	var affectedRun string
	if err := tx.QueryRowContext(ctx, `SELECT run_id FROM messages WHERE id=? AND session_id=?`, messageID, sessionID).Scan(&affectedRun); err != nil {
		return Event{}, err
	}
	var running bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE session_id=? AND status='running') OR EXISTS(SELECT 1 FROM jobs WHERE session_id=? AND status IN ('starting','running')) OR EXISTS(SELECT 1 FROM agent_tasks WHERE session_id=? AND status='started')`, sessionID, sessionID, sessionID).Scan(&running); err != nil {
		return Event{}, err
	}
	if running {
		return Event{}, ErrBusy
	}
	var pending *PendingReply
	var pendingRaw []byte
	err = tx.QueryRowContext(ctx, `SELECT CAST(e.data AS BLOB) FROM events e WHERE e.session_id=? AND e.kind='run_finished' AND e.sequence=(SELECT MAX(sequence) FROM messages WHERE session_id=? AND sequence<?)`, sessionID, sessionID, sequence).Scan(&pendingRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Event{}, err
	}
	if err == nil {
		var data struct {
			Pending *PendingReply `json:"pendingReply"`
		}
		if err := json.Unmarshal(pendingRaw, &data); err != nil {
			return Event{}, err
		}
		pending = data.Pending
	}
	event, err := appendEvent(ctx, tx, sessionID, "conversation_reverted", map[string]any{"messageId": messageID, "beforeSequence": sequence, "pendingReply": pending})
	if err != nil {
		return Event{}, err
	}
	for _, q := range []string{
		`DELETE FROM model_reply_feedback WHERE message_id IN (SELECT id FROM messages WHERE session_id=? AND sequence>=?)`,
		`DELETE FROM model_output_chunks WHERE session_id=? AND event_sequence>=?`,
		`DELETE FROM jobs WHERE session_id=? AND id IN (SELECT json_extract(data,'$.id') FROM events WHERE session_id=jobs.session_id AND kind='job' GROUP BY json_extract(data,'$.id') HAVING MIN(sequence)>=?)`,
		`DELETE FROM artifacts WHERE session_id=? AND id IN (SELECT json_extract(data,'$.id') FROM events WHERE session_id=artifacts.session_id AND kind='artifact' GROUP BY json_extract(data,'$.id') HAVING MIN(sequence)>=?)`,
		`DELETE FROM agent_tasks WHERE session_id=? AND NOT EXISTS(SELECT 1 FROM events e WHERE e.session_id=agent_tasks.session_id AND e.kind='agent_task' AND json_extract(e.data,'$.runId')=agent_tasks.run_id AND json_extract(e.data,'$.task.id')=agent_tasks.task_id AND e.sequence<?)`,
		`DELETE FROM messages WHERE session_id=? AND sequence>=?`,
	} {
		if _, err := tx.ExecContext(ctx, q, sessionID, sequence); err != nil {
			return Event{}, err
		}
	}
	// Pending inputs are tied to the discarded future; do not resume them after
	// a rewind, including inputs attached to a partially retained execution.
	if _, err := tx.ExecContext(ctx, `DELETE FROM queued_inputs WHERE session_id=?`, sessionID); err != nil {
		return Event{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_resources WHERE session_id=? AND NOT EXISTS(SELECT 1 FROM message_resources mr JOIN messages m ON m.id=mr.message_id WHERE m.session_id=session_resources.session_id AND mr.hash=session_resources.hash)`, sessionID); err != nil {
		return Event{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE session_id=? AND sequence>=? AND sequence<? AND kind NOT IN ('session_renamed','session_title','session_selection','reply_language')`, sessionID, sequence, event.Sequence); err != nil {
		return Event{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE session_id=? AND NOT EXISTS(SELECT 1 FROM messages WHERE run_id=runs.id)`, sessionID); err != nil {
		return Event{}, err
	}
	// Earlier tasks may have completed during the discarded tail. Rebuild their
	// report projection from the retained event instead of keeping future data.
	if _, err := tx.ExecContext(ctx, `UPDATE agent_tasks SET data=(SELECT data FROM events e WHERE e.session_id=agent_tasks.session_id AND e.kind='agent_task' AND json_extract(e.data,'$.runId')=agent_tasks.run_id AND json_extract(e.data,'$.task.id')=agent_tasks.task_id ORDER BY e.sequence DESC LIMIT 1) WHERE session_id=?`, sessionID); err != nil {
		return Event{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT CAST(data AS BLOB) FROM agent_tasks WHERE session_id=? AND json_extract(data,'$.task.status')='started'`, sessionID)
	if err != nil {
		return Event{}, err
	}
	var interrupted []workerproto.Result
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return Event{}, err
		}
		var result workerproto.Result
		if err := json.Unmarshal(raw, &result); err != nil {
			rows.Close()
			return Event{}, err
		}
		interrupted = append(interrupted, result)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Event{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_tasks SET status=CASE WHEN json_extract(data,'$.task.status')='started' THEN 'interrupted' ELSE json_extract(data,'$.task.status') END, data=CASE WHEN json_extract(data,'$.task.status')='started' THEN json_set(data,'$.task.status','interrupted') ELSE data END WHERE session_id=?`, sessionID); err != nil {
		return Event{}, err
	}
	for _, result := range interrupted {
		result.Task.Status = "interrupted"
		if _, err := appendUnknownTools(ctx, tx, sessionID, result.RunID, result.TaskID); err != nil {
			return Event{}, err
		}
		if _, err := appendEvent(ctx, tx, sessionID, "agent_task", result); err != nil {
			return Event{}, err
		}
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runs SET status='interrupted',finished_at=? WHERE id=? AND session_id=?`, event.CreatedAt, affectedRun, sessionID)
	if err != nil {
		return Event{}, err
	}
	if count, err := changed.RowsAffected(); err != nil {
		return Event{}, err
	} else if count > 0 {
		if _, err := appendUnknownTools(ctx, tx, sessionID, affectedRun); err != nil {
			return Event{}, err
		}
		if _, err := appendEvent(ctx, tx, sessionID, "run_finished", map[string]any{"runId": affectedRun, "status": "interrupted", "reason": "Stopped by conversation rollback"}); err != nil {
			return Event{}, err
		}
	}
	var pendingKind, pendingContent, pendingRun string
	if pending != nil {
		pendingKind, pendingRun = pending.Kind, pending.RunID
		pendingContent, err = pendingStorageContent(pending)
		if err != nil {
			return Event{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET pending_kind=?,pending_content=?,pending_run_id=?,input_queue_paused=0,read_sequence=MIN(read_sequence,COALESCE((SELECT MAX(sequence) FROM messages WHERE session_id=? AND role='assistant'),0)) WHERE id=?`, pendingKind, pendingContent, pendingRun, sessionID, sessionID); err != nil {
		return Event{}, err
	}
	return event, tx.Commit()
}

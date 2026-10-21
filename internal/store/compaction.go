package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"vista/internal/workerproto"
)

func (s *Store) ContextCheckpoint(ctx context.Context, sessionID string) (*workerproto.ContextCheckpoint, error) {
	var raw []byte
	var sequence int64
	err := s.db.QueryRowContext(ctx, `SELECT sequence,json_extract(data,'$.checkpoint') FROM events WHERE session_id=? AND kind='context_notice' AND json_extract(data,'$.depth')=0 AND json_type(data,'$.checkpoint')='object' ORDER BY sequence DESC LIMIT 1`, sessionID).Scan(&sequence, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var checkpoint workerproto.ContextCheckpoint
	err = json.Unmarshal(raw, &checkpoint)
	checkpoint.Sequence = sequence
	return &checkpoint, err
}

func (s *Store) CompactionStatus(ctx context.Context, sessionID string) (*workerproto.CompactionStatus, error) {
	var raw []byte
	var sequence int64
	err := s.db.QueryRowContext(ctx, `SELECT sequence, CASE WHEN kind='context_status' THEN data ELSE json_extract(data,'$.context') END FROM events WHERE session_id=? AND (kind='context_status' OR (kind='context_notice' AND json_extract(data,'$.depth')=0 AND json_type(data,'$.context')='object')) ORDER BY sequence DESC LIMIT 1`, sessionID).Scan(&sequence, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var status workerproto.CompactionStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return nil, err
	}
	status.Sequence = sequence
	if status.Phase == "compacting" || status.Manual && status.Phase == "compacted" {
		var running bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE id=? AND session_id=? AND status='running')`, status.RunID, sessionID).Scan(&running); err != nil {
			return nil, err
		}
		if running && status.Manual {
			status.Phase = "compacting"
		}
		if !running && status.Phase == "compacting" {
			status.Phase = "failed"
			status.Error = "Compaction was interrupted; the previous context was retained"
		}
	}
	return &status, nil
}

// A maintenance run uses the same atomic execution lock without inventing a
// conversation message or changing the user's pending question/selection.
func (s *Store) BeginCompaction(ctx context.Context, sessionID, runID string) (int64, *Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	var open int
	if err := tx.QueryRowContext(ctx, "SELECT is_open FROM sessions WHERE id=?", sessionID).Scan(&open); err != nil {
		return 0, nil, err
	}
	if open == 0 {
		return 0, nil, ErrClosed
	}
	var busy bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM runs WHERE session_id=? AND status='running')", sessionID).Scan(&busy); err != nil {
		return 0, nil, err
	}
	if busy {
		return 0, nil, ErrBusy
	}
	var through int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM messages WHERE session_id=?", sessionID).Scan(&through); err != nil {
		return 0, nil, err
	}
	if through == 0 {
		return 0, nil, errors.New("no conversation history to compact")
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO runs(id,session_id,status,created_at) VALUES(?,?,'running',?)", runID, sessionID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return 0, nil, err
	}
	event, err := appendEvent(ctx, tx, sessionID, "context_status", workerproto.CompactionStatus{RunID: runID, Phase: "compacting", Manual: true})
	if err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return through, &event, nil
}

func isCompaction(ctx context.Context, tx *sql.Tx, sessionID, runID string) (bool, error) {
	var manual bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE session_id=? AND kind='context_status' AND json_extract(data,'$.runId')=? AND json_extract(data,'$.manual')=1)`, sessionID, runID).Scan(&manual)
	return manual, err
}

func (s *Store) FinishCompaction(ctx context.Context, sessionID, runID, status, message string) (*Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	manual, err := isCompaction(ctx, tx, sessionID, runID)
	if err != nil {
		return nil, err
	}
	if !manual {
		return nil, errors.New("run is not a manual compaction")
	}
	event, err := finishCompactionInTx(ctx, tx, sessionID, runID, status, message)
	if err != nil {
		return nil, err
	}
	return event, tx.Commit()
}

func finishCompactionInTx(ctx context.Context, tx *sql.Tx, sessionID, runID, status, message string) (*Event, error) {
	if status != "succeeded" && status != "failed" && status != "interrupted" {
		return nil, errors.New("invalid compaction result")
	}
	// The acknowledged checkpoint is the commit point, including a cancellation
	// that races with the final Worker frame.
	var committed bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE session_id=? AND kind='context_notice' AND json_extract(data,'$.runId')=? AND json_type(data,'$.checkpoint')='object')`, sessionID, runID).Scan(&committed)
	if err != nil {
		return nil, err
	}
	if committed {
		status, message = "succeeded", ""
	} else if status == "succeeded" {
		return nil, errors.New("compaction finished without a checkpoint")
	}
	result, err := tx.ExecContext(ctx, "UPDATE runs SET status=?,finished_at=? WHERE id=? AND session_id=? AND status='running'", status, time.Now().UTC().Format(time.RFC3339Nano), runID, sessionID)
	if err != nil {
		return nil, err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return nil, err
	}
	phase := "compacted"
	if status != "succeeded" {
		phase = "failed"
	}
	event, err := appendEvent(ctx, tx, sessionID, "context_status", workerproto.CompactionStatus{RunID: runID, Phase: phase, Manual: true, Error: message})
	return &event, err
}

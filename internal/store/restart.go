package store

import (
	"context"
	"encoding/json"

	"vista/internal/workerproto"

	"gorm.io/gorm"
)

// PrepareRestart records eligibility before shutdown can turn a running run
// into an interrupted one. The staged inbox state survives ordinary Worker
// cleanup, but is neither delivered nor scheduled until startup reconciles it.
func (s *Store) PrepareRestart(ctx context.Context) error {
	tx, err := s.begin(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := prepareRestart(ctx, tx); err != nil {
		return err
	}
	return tx.Commit().Error
}

func prepareRestart(ctx context.Context, tx *gorm.DB) error {
	// An earlier startup may have left recovery waiting for admission. Keep
	// that notice across another restart without manufacturing a second one.
	if _, err := execSQL(ctx, tx, `UPDATE runtime_events SET status='restart'
 WHERE status='pending' AND json_extract(data,'$.kind')='service_restart'
 AND NOT EXISTS(SELECT 1 FROM runs r WHERE r.session_id=runtime_events.session_id AND r.status='running')`); err != nil {
		return err
	}
	rows, err := tx.WithContext(ctx).Raw(`SELECT r.id,r.session_id FROM runs r JOIN sessions s ON s.id=r.session_id
 WHERE r.status='running' AND s.is_open=1 AND s.pending_kind=''
  AND NOT EXISTS(SELECT 1 FROM schedule_runs sr WHERE sr.run_id=r.id AND sr.state='running')
 AND NOT EXISTS(SELECT 1 FROM events e WHERE e.session_id=r.session_id AND e.kind='run_cancel_requested' AND json_extract(e.data,'$.runId')=r.id)
 AND NOT EXISTS(SELECT 1 FROM runtime_events e WHERE e.session_id=r.session_id AND e.event_id='restart/'||r.id)`).Rows()
	if err != nil {
		return err
	}
	type candidate struct{ run, session string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.run, &c.session); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range candidates {
		manual, err := isCompaction(ctx, tx, c.session, c.run)
		if err != nil {
			return err
		}
		if manual {
			continue
		}
		notice := workerproto.RuntimeEvent{ID: "restart/" + c.run, RunID: c.run, Recipient: "main", SourceID: c.run,
			Kind: "service_restart", Status: "interrupted", Content: "Vista restarted during this task. Continue the user's authorized task from the retained conversation and completed tool results. A tool without a confirmed result has an unknown outcome: verify its effects before retrying. Background processes and child tasks were stopped; their preservation, reattachment or recreation is not guaranteed."}
		event, err := appendEvent(ctx, tx, c.session, "runtime_event", notice)
		if err != nil {
			return err
		}
		data, err := json.Marshal(notice)
		if err != nil {
			return err
		}
		if _, err := execSQL(ctx, tx, `INSERT INTO runtime_events(session_id,run_id,event_id,recipient,sequence,status,data) VALUES(?,?,?,?,?,'restart',?)`, c.session, c.run, notice.ID, "main", event.Sequence, data); err != nil {
			return err
		}
	}
	return nil
}

// RecoverExecutions also covers abrupt Server exit: eligibility capture,
// interruption and activation share one transaction. A second startup cannot
// lose a continuation that has not yet been admitted or acknowledged.
func (s *Store) RecoverExecutions(ctx context.Context) error {
	tx, err := s.begin(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := prepareRestart(ctx, tx); err != nil {
		return err
	}
	if _, err := interruptRunning(ctx, tx, ""); err != nil {
		return err
	}
	if _, err := execSQL(ctx, tx, `UPDATE runtime_events SET status='pending' WHERE status='restart'
 AND EXISTS(SELECT 1 FROM runs r JOIN sessions s ON s.id=r.session_id
 WHERE r.id=runtime_events.run_id AND r.status='interrupted' AND s.is_open=1 AND s.pending_kind=''
 AND NOT EXISTS(SELECT 1 FROM runs newer WHERE newer.session_id=r.session_id AND newer.rowid>r.rowid))`); err != nil {
		return err
	}
	// A completed run or a newly pending question wins a shutdown race. Do
	// not let other retained notices revive that ineligible execution either.
	if _, err := execSQL(ctx, tx, `UPDATE runtime_events SET status='suppressed'
 WHERE status IN ('pending','restart') AND session_id IN (SELECT session_id FROM runtime_events WHERE status='restart')`); err != nil {
		return err
	}
	return tx.Commit().Error
}

func restartStaged(ctx context.Context, tx *gorm.DB, sessionID, runID string) (bool, error) {
	var staged bool
	err := tx.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM runtime_events WHERE session_id=? AND run_id=? AND status='restart')`, sessionID, runID).Row().Scan(&staged)
	return staged, err
}

// Cancellation intent must commit before IPC: a crash between the accepted
// request and the Worker's terminal result must not turn Cancel into Resume.
func (s *Store) RequestRunCancellation(ctx context.Context, sessionID, runID string) error {
	tx, err := s.begin(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active bool
	if err := tx.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM runs WHERE id=? AND session_id=? AND status='running')`, runID, sessionID).Row().Scan(&active); err != nil {
		return err
	}
	if active {
		if _, err := appendEvent(ctx, tx, sessionID, "run_cancel_requested", map[string]string{"runId": runID}); err != nil {
			return err
		}
	}
	if err := suppressRuntimeEvents(ctx, tx, sessionID); err != nil {
		return err
	}
	if err := markQueuedInTx(ctx, tx, sessionID, runID, "interrupted"); err != nil {
		return err
	}
	return tx.Commit().Error
}

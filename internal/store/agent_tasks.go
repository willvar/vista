package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"vista/internal/workerproto"
)

// After foreground finalization only a registered, live background task may
// keep writing to the original trace. A sibling or late root event cannot.
func executionActive(ctx context.Context, tx *sql.Tx, sessionID, runID, taskID string) (bool, error) {
	var active bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE id=? AND session_id=? AND status='running')
 OR EXISTS(SELECT 1 FROM agent_tasks WHERE run_id=? AND session_id=? AND task_id=? AND background=1 AND status='started')`, runID, sessionID, runID, sessionID, taskID).Scan(&active)
	return active, err
}

func (s *Store) RecordAgentTask(ctx context.Context, sessionID string, result workerproto.Result) (*Event, error) {
	task := result.Task
	if task == nil || result.RunID == "" || task.ID == "" || task.ID != result.TaskID || task.ParentID != result.ParentTaskID ||
		(task.Status != "started" && task.Status != "succeeded" && task.Status != "failed" && task.Status != "canceled" && task.Status != "interrupted") {
		return nil, errors.New("invalid agent task")
	}
	result.AckID = ""
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var changed sql.Result
	if task.Status == "started" {
		active, err := executionActive(ctx, tx, sessionID, result.RunID, task.ParentID)
		if err != nil || !active {
			return nil, err
		}
		changed, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_tasks(session_id,run_id,task_id,parent_task_id,join_policy,status,background,data)
 VALUES(?,?,?,?,?,'started',? OR EXISTS(SELECT 1 FROM agent_tasks WHERE run_id=? AND task_id=? AND background=1),?)`,
			sessionID, result.RunID, task.ID, task.ParentID, task.Join, task.Join == "detached", result.RunID, task.ParentID, data)
		if err != nil {
			return nil, err
		}
	} else {
		changed, err = tx.ExecContext(ctx, `UPDATE agent_tasks SET status=?,data=? WHERE session_id=? AND run_id=? AND task_id=? AND status='started'`, task.Status, data, sessionID, result.RunID, task.ID)
		if err != nil {
			return nil, err
		}
	}
	n, err := changed.RowsAffected()
	if err != nil || n == 0 {
		return nil, err
	}
	// The foreground gate must still see unmatched main-agent tool starts.
	// Detached tasks have no later run finish, so settle their own tools here.
	if task.Status != "started" && task.ParentID != "" {
		unknown, err := appendUnknownTools(ctx, tx, sessionID, result.RunID, task.ID)
		if err != nil {
			return nil, err
		}
		if unknown > 0 && task.Status == "succeeded" {
			return nil, errors.New("task ended without confirmed tool results")
		}
	}
	event, err := appendEvent(ctx, tx, sessionID, "agent_task", result)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &event, nil
}

func (s *Store) ActiveAgentTask(ctx context.Context, sessionID, runID, taskID string) (bool, error) {
	var active bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_tasks WHERE session_id=? AND run_id=? AND task_id=? AND status='started' AND parent_task_id!='')`, sessionID, runID, taskID).Scan(&active)
	return active, err
}

// Worker loss stops all its tasks, including those whose foreground run already
// ended. Persist the terminal trace instead of attempting side effects again.
func interruptAgentTasks(ctx context.Context, tx *sql.Tx, sessionID string) ([]Event, error) {
	rows, err := tx.QueryContext(ctx, `SELECT session_id,data FROM agent_tasks WHERE status='started' AND (?='' OR session_id=?) ORDER BY rowid`, sessionID, sessionID)
	if err != nil {
		return nil, err
	}
	type pending struct {
		sessionID string
		result    workerproto.Result
	}
	var tasks []pending
	for rows.Next() {
		var item pending
		var data []byte
		if err := rows.Scan(&item.sessionID, &data); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal(data, &item.result); err != nil {
			rows.Close()
			return nil, err
		}
		tasks = append(tasks, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	var events []Event
	for _, item := range tasks {
		item.result.Task.Status = "interrupted"
		data, err := json.Marshal(item.result)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_tasks SET status='interrupted',data=? WHERE run_id=? AND task_id=?`, data, item.result.RunID, item.result.TaskID); err != nil {
			return nil, err
		}
		if _, err := appendUnknownTools(ctx, tx, item.sessionID, item.result.RunID, item.result.TaskID); err != nil {
			return nil, err
		}
		event, err := appendEvent(ctx, tx, item.sessionID, "agent_task", item.result)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

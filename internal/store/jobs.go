package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type Job struct {
	StartedAt         string `json:"startedAt,omitempty"`
	ExpiresAt         string `json:"expiresAt,omitempty"`
	Signal            string `json:"signal,omitempty"`
	TerminationReason string `json:"terminationReason,omitempty"`
	CallID            string `json:"callId,omitempty"`
	TaskID            string `json:"taskId,omitempty"`
	ParentTaskID      string `json:"parentTaskId,omitempty"`
	ID                string `json:"id"`
	SessionID         string `json:"sessionId"`
	RunID             string `json:"runId"`
	Directory         string `json:"directory"`
	Command           string `json:"command"`
	OutputPath        string `json:"outputPath"`
	Status            string `json:"status"`
	Bytes             int64  `json:"bytes"`
	ExitCode          int    `json:"exitCode"`
	Complete          bool   `json:"complete"`
	Error             string `json:"error,omitempty"`
	CreatedAt         string `json:"createdAt"`
	FinishedAt        string `json:"finishedAt,omitempty"`
}

func (s *Store) RecordJob(ctx context.Context, job Job) (*Event, error) {
	var previousStatus string
	if job.ID == "" || job.SessionID == "" || job.RunID == "" {
		return nil, errors.New("invalid job identity")
	}
	if job.Bytes < 0 || len(job.Error) > 512 {
		return nil, errors.New("invalid job result")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if job.Status == "starting" {
		active, err := executionActive(ctx, tx, job.SessionID, job.RunID, job.TaskID)
		if err != nil || !active {
			return nil, err
		}
		job.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,session_id,run_id,directory,command,output_path,status,created_at) VALUES(?,?,?,?,?,?,?,?)`, job.ID, job.SessionID, job.RunID, job.Directory, job.Command, job.OutputPath, job.Status, job.CreatedAt); err != nil {
			return nil, err
		}
	} else {
		if job.Status != "running" && job.Status != "succeeded" && job.Status != "failed" && job.Status != "interrupted" {
			return nil, errors.New("invalid job status")
		}
		if job.Status != "running" {
			job.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		if err := tx.QueryRowContext(ctx, "SELECT created_at,status FROM jobs WHERE id=? AND session_id=? AND run_id=?", job.ID, job.SessionID, job.RunID).Scan(&job.CreatedAt, &previousStatus); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil
			}
			return nil, err
		}
		var result sql.Result
		result, err = tx.ExecContext(ctx, `UPDATE jobs SET status=?,bytes=?,exit_code=?,complete=?,error=?,finished_at=? WHERE id=? AND session_id=? AND run_id=? AND status IN ('starting','running')`, job.Status, job.Bytes, job.ExitCode, job.Complete, job.Error, nullableTime(job.FinishedAt), job.ID, job.SessionID, job.RunID)
		if err == nil {
			var changed int64
			changed, err = result.RowsAffected()
			if err == nil && changed == 0 {
				return nil, nil
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if err := restoreOutputLink(ctx, tx, job.SessionID, "job", job.ID, &job.CallID, &job.TaskID, &job.ParentTaskID); err != nil {
		return nil, err
	}
	kind, data := "job", any(job)
	if previousStatus == "running" && job.Status == "running" {
		// Output growth is replayable metadata, not another process action.
		kind, data = "job_output", struct {
			ID    string `json:"id"`
			RunID string `json:"runId"`
			Bytes int64  `json:"bytes"`
		}{job.ID, job.RunID, job.Bytes}
	}
	event, err := appendEvent(ctx, tx, job.SessionID, kind, data)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &event, nil
}

func nullableTime(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) Jobs(ctx context.Context, sessionID string) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,session_id,run_id,directory,command,output_path,status,bytes,exit_code,complete,error,created_at,COALESCE(finished_at,'') FROM jobs WHERE session_id=? ORDER BY status IN ('starting','running') DESC,created_at DESC,id DESC LIMIT 200`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Job{}
	for rows.Next() {
		var job Job
		if err := rows.Scan(&job.ID, &job.SessionID, &job.RunID, &job.Directory, &job.Command, &job.OutputPath, &job.Status, &job.Bytes, &job.ExitCode, &job.Complete, &job.Error, &job.CreatedAt, &job.FinishedAt); err != nil {
			return nil, err
		}
		result = append(result, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	ids := make([]string, len(result))
	for i := range result {
		ids[i] = result[i].ID
	}
	links, err := s.outputLinks(ctx, sessionID, "job", ids)
	if err != nil {
		return nil, err
	}
	for i := range result {
		link := links[result[i].ID]
		result[i].CallID, result[i].TaskID, result[i].ParentTaskID = link.CallID, link.TaskID, link.ParentTaskID
		result[i].StartedAt, result[i].ExpiresAt, result[i].Signal, result[i].TerminationReason = link.StartedAt, link.ExpiresAt, link.Signal, link.TerminationReason
	}
	return result, nil
}

func (s *Store) GetJob(ctx context.Context, sessionID, jobID string) (Job, error) {
	var job Job
	err := s.db.QueryRowContext(ctx, `SELECT id,session_id,run_id,directory,command,output_path,status,bytes,exit_code,complete,error,created_at,COALESCE(finished_at,'') FROM jobs WHERE session_id=? AND id=?`, sessionID, jobID).Scan(&job.ID, &job.SessionID, &job.RunID, &job.Directory, &job.Command, &job.OutputPath, &job.Status, &job.Bytes, &job.ExitCode, &job.Complete, &job.Error, &job.CreatedAt, &job.FinishedAt)
	if err != nil {
		return job, err
	}
	links, err := s.outputLinks(ctx, sessionID, "job", []string{job.ID})
	link := links[job.ID]
	job.CallID, job.TaskID, job.ParentTaskID = link.CallID, link.TaskID, link.ParentTaskID
	job.StartedAt, job.ExpiresAt, job.Signal, job.TerminationReason = link.StartedAt, link.ExpiresAt, link.Signal, link.TerminationReason
	return job, err
}

func (s *Store) InterruptJobs(ctx context.Context, sessionID, reason string) ([]Event, error) {
	if reason == "" {
		reason = "Worker stopped before the background job result was confirmed"
	}
	if len(reason) > 512 {
		reason = reason[:512]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	query := `SELECT id,session_id,run_id,directory,command,output_path,status,bytes,exit_code,complete,created_at FROM jobs WHERE status IN ('starting','running')`
	args := []any{}
	if sessionID != "" {
		query += " AND session_id=?"
		args = append(args, sessionID)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	jobs := []Job{}
	for rows.Next() {
		var job Job
		if err := rows.Scan(&job.ID, &job.SessionID, &job.RunID, &job.Directory, &job.Command, &job.OutputPath, &job.Status, &job.Bytes, &job.ExitCode, &job.Complete, &job.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	events := make([]Event, 0, len(jobs))
	for _, job := range jobs {
		var data []byte
		if err := tx.QueryRowContext(ctx, `SELECT data FROM events WHERE session_id=? AND kind='job' AND json_extract(data,'$.id')=? ORDER BY sequence DESC LIMIT 1`, job.SessionID, job.ID).Scan(&data); err == nil {
			var previous Job
			if err := json.Unmarshal(data, &previous); err != nil {
				return nil, err
			}
			job.StartedAt, job.ExpiresAt = previous.StartedAt, previous.ExpiresAt
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		job.Status, job.Error = "interrupted", reason
		job.TerminationReason = "interrupted"
		job.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status='interrupted',error=?,finished_at=? WHERE id=?`, reason, job.FinishedAt, job.ID); err != nil {
			return nil, err
		}
		if err := restoreOutputLink(ctx, tx, job.SessionID, "job", job.ID, &job.CallID, &job.TaskID, &job.ParentTaskID); err != nil {
			return nil, err
		}
		event, err := appendEvent(ctx, tx, job.SessionID, "job", job)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return events, nil
}

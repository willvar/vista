package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"
)

type Artifact struct {
	CallID       string `json:"callId,omitempty"`
	TaskID       string `json:"taskId,omitempty"`
	ParentTaskID string `json:"parentTaskId,omitempty"`
	ID           string `json:"id"`
	SessionID    string `json:"sessionId"`
	RunID        string `json:"runId"`
	OutputPath   string `json:"outputPath"`
	Status       string `json:"status"`
	Bytes        int64  `json:"bytes"`
	Complete     bool   `json:"complete"`
	Error        string `json:"error,omitempty"`
	CreatedAt    string `json:"createdAt"`
	FinishedAt   string `json:"finishedAt,omitempty"`
}

func (s *Store) RecordArtifact(ctx context.Context, artifact Artifact) (*Event, error) {
	if artifact.ID == "" || artifact.SessionID == "" || artifact.RunID == "" || artifact.OutputPath == "" || artifact.Bytes < 0 || len(artifact.Error) > 512 {
		return nil, errors.New("invalid artifact record")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if artifact.Status == "started" {
		active, err := executionActive(ctx, tx, artifact.SessionID, artifact.RunID, artifact.TaskID)
		if err != nil || !active {
			return nil, err
		}
		artifact.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, "INSERT INTO artifacts(id,session_id,run_id,output_path,status,created_at) VALUES(?,?,?,?,?,?)", artifact.ID, artifact.SessionID, artifact.RunID, artifact.OutputPath, artifact.Status, artifact.CreatedAt); err != nil {
			return nil, err
		}
	} else {
		if artifact.Status != "succeeded" && artifact.Status != "failed" {
			return nil, errors.New("invalid artifact status")
		}
		if err := tx.QueryRowContext(ctx, "SELECT created_at FROM artifacts WHERE id=? AND session_id=? AND run_id=?", artifact.ID, artifact.SessionID, artifact.RunID).Scan(&artifact.CreatedAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil
			}
			return nil, err
		}
		artifact.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		var result sql.Result
		result, err = tx.ExecContext(ctx, "UPDATE artifacts SET status=?,bytes=?,complete=?,error=?,finished_at=? WHERE id=? AND session_id=? AND run_id=? AND status='started'", artifact.Status, artifact.Bytes, artifact.Complete, artifact.Error, artifact.FinishedAt, artifact.ID, artifact.SessionID, artifact.RunID)
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
	if err := restoreOutputLink(ctx, tx, artifact.SessionID, "artifact", artifact.ID, &artifact.CallID, &artifact.TaskID, &artifact.ParentTaskID); err != nil {
		return nil, err
	}
	event, err := appendEvent(ctx, tx, artifact.SessionID, "artifact", artifact)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &event, nil
}

func (s *Store) GetArtifact(ctx context.Context, sessionID, artifactID string) (Artifact, error) {
	var item Artifact
	err := s.db.QueryRowContext(ctx, "SELECT id,session_id,run_id,output_path,status,bytes,complete,error,created_at,COALESCE(finished_at,'') FROM artifacts WHERE session_id=? AND id=?", sessionID, artifactID).Scan(&item.ID, &item.SessionID, &item.RunID, &item.OutputPath, &item.Status, &item.Bytes, &item.Complete, &item.Error, &item.CreatedAt, &item.FinishedAt)
	if err != nil {
		return item, err
	}
	links, err := s.outputLinks(ctx, sessionID, "artifact", []string{item.ID})
	link := links[item.ID]
	item.CallID, item.TaskID, item.ParentTaskID = link.CallID, link.TaskID, link.ParentTaskID
	return item, err
}

func (s *Store) Artifacts(ctx context.Context, sessionID string) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,session_id,run_id,output_path,status,bytes,complete,error,created_at,COALESCE(finished_at,'') FROM artifacts WHERE session_id=? ORDER BY created_at DESC,id DESC LIMIT 200", sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Artifact{}
	for rows.Next() {
		var item Artifact
		if err := rows.Scan(&item.ID, &item.SessionID, &item.RunID, &item.OutputPath, &item.Status, &item.Bytes, &item.Complete, &item.Error, &item.CreatedAt, &item.FinishedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	ids := make([]string, len(items))
	for i := range items {
		ids[i] = items[i].ID
	}
	links, err := s.outputLinks(ctx, sessionID, "artifact", ids)
	if err != nil {
		return nil, err
	}
	for i := range items {
		link := links[items[i].ID]
		items[i].CallID, items[i].TaskID, items[i].ParentTaskID = link.CallID, link.TaskID, link.ParentTaskID
	}
	return items, nil
}

func (s *Store) ArtifactPaths(ctx context.Context, sessionID string) ([]string, error) {
	paths := []string{}
	for _, table := range []string{"artifacts", "jobs"} {
		rows, err := s.db.QueryContext(ctx, "SELECT output_path FROM "+table+" WHERE session_id=?", sessionID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var path string
			if err := rows.Scan(&path); err != nil {
				rows.Close()
				return nil, err
			}
			paths = append(paths, path)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT CASE WHEN kind='context_notice' THEN json_extract(data,'$.checkpoint.path') ELSE json_extract(data,'$.history.path') END FROM events WHERE session_id=? AND ((kind='context_notice' AND json_type(data,'$.checkpoint.path')='text') OR (kind='model_history' AND json_type(data,'$.history.path')='text'))`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}

func (s *Store) InterruptArtifacts(ctx context.Context, sessionID, reason string) ([]Event, error) {
	if reason == "" {
		reason = "Worker stopped before output completeness was confirmed"
	}
	if len(reason) > 512 {
		reason = reason[:512]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	query := "SELECT id,session_id,run_id,output_path,bytes,created_at FROM artifacts WHERE status='started'"
	args := []any{}
	if sessionID != "" {
		query += " AND session_id=?"
		args = append(args, sessionID)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	items := []Artifact{}
	for rows.Next() {
		var item Artifact
		if err := rows.Scan(&item.ID, &item.SessionID, &item.RunID, &item.OutputPath, &item.Bytes, &item.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	events := make([]Event, 0, len(items))
	for _, item := range items {
		item.Status, item.Error = "failed", reason
		item.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if info, err := os.Lstat(item.OutputPath); err == nil && info.Mode().IsRegular() {
			item.Bytes = info.Size()
		}
		if _, err := tx.ExecContext(ctx, "UPDATE artifacts SET status='failed',bytes=?,error=?,finished_at=? WHERE id=?", item.Bytes, reason, item.FinishedAt, item.ID); err != nil {
			return nil, err
		}
		if err := restoreOutputLink(ctx, tx, item.SessionID, "artifact", item.ID, &item.CallID, &item.TaskID, &item.ParentTaskID); err != nil {
			return nil, err
		}
		event, err := appendEvent(ctx, tx, item.SessionID, "artifact", item)
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

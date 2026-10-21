package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Lifecycle events own task/call links and process lifetime/exit metadata.
// Keep that metadata there instead of duplicating it into the storage schema.
type outputLink struct {
	StartedAt         string `json:"startedAt"`
	ExpiresAt         string `json:"expiresAt"`
	Signal            string `json:"signal"`
	TerminationReason string `json:"terminationReason"`
	ID                string `json:"id"`
	CallID            string `json:"callId"`
	TaskID            string `json:"taskId"`
	ParentTaskID      string `json:"parentTaskId"`
}

func restoreOutputLink(ctx context.Context, tx *sql.Tx, sessionID, kind, id string, callID, taskID, parentTaskID *string) error {
	if *callID != "" && *taskID != "" && (*parentTaskID != "" || *taskID == "main") {
		return nil
	}
	var data []byte
	err := tx.QueryRowContext(ctx, `SELECT data FROM events WHERE session_id=? AND kind=? AND json_extract(data,'$.id')=?
  AND (COALESCE(json_extract(data,'$.callId'),'')!='' OR COALESCE(json_extract(data,'$.taskId'),'')!='')
  ORDER BY sequence DESC LIMIT 1`, sessionID, kind, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var link outputLink
	if err := json.Unmarshal(data, &link); err != nil {
		return err
	}
	if *callID == "" {
		*callID = link.CallID
	}
	if *taskID == "" {
		*taskID = link.TaskID
	}
	if *parentTaskID == "" {
		*parentTaskID = link.ParentTaskID
	}
	return nil
}

func (s *Store) outputLinks(ctx context.Context, sessionID, kind string, ids []string) (map[string]outputLink, error) {
	links := make(map[string]outputLink)
	if len(ids) == 0 {
		return links, nil
	}
	var events []Event
	if err := s.orm.WithContext(ctx).Table("events").Select("CAST(data AS BLOB) AS data").
		Where("session_id=? AND kind=? AND json_extract(data,'$.id') IN ?", sessionID, kind, ids).
		Order("sequence DESC").Find(&events).Error; err != nil {
		return nil, err
	}
	for _, event := range events {
		var link outputLink
		if err := json.Unmarshal(event.Data, &link); err != nil {
			return nil, err
		}
		if _, exists := links[link.ID]; !exists {
			links[link.ID] = link
		}
	}
	return links, nil
}

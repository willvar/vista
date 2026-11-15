package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

type ChecklistItem struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

type ChecklistSummary struct {
	Items           []ChecklistItem `json:"items"`
	Status          string          `json:"status"`
	UpdatedAt       string          `json:"updatedAt,omitempty"`
	UpdatedSequence int64           `json:"updatedSequence,omitempty"`
}

func ValidateChecklist(items []ChecklistItem) error {
	if len(items) > 16 {
		return errors.New("checklist exceeds 16 items")
	}
	for _, item := range items {
		if item.Text != strings.TrimSpace(item.Text) || item.Text == "" || len(item.Text) > 160 || !utf8.ValidString(item.Text) {
			return errors.New("checklist item text must be 1–160 UTF-8 bytes without outer whitespace")
		}
		switch item.Status {
		case "pending", "in_progress", "done", "blocked":
		default:
			return errors.New("invalid checklist item status")
		}
	}
	return nil
}

// RunChecklist reads the latest replacement from this run's event history.
// A run without a checklist never inherits another run's items.
func (s *Store) RunChecklist(ctx context.Context, sessionID, runID string) ([]ChecklistItem, error) {
	var raw []byte
	err := s.orm.WithContext(ctx).Raw(`SELECT CAST(data AS BLOB) FROM events
 WHERE session_id=? AND kind='checklist' AND json_extract(data,'$.runId')=?
 ORDER BY sequence DESC LIMIT 1`, sessionID, runID).Row().Scan(&raw)
	if errors.Is(err, ErrNotFound) {
		return []ChecklistItem{}, nil
	}
	if err != nil {
		return nil, err
	}
	var payload struct {
		Items []ChecklistItem `json:"items"`
	}
	err = json.Unmarshal(raw, &payload)
	return payload.Items, err
}

// ReplaceChecklist appends a complete replacement to the run's event history.
// A completed or interrupted run cannot modify the checklist.
func (s *Store) ReplaceChecklist(ctx context.Context, sessionID, runID string, items []ChecklistItem) (*Event, error) {
	if err := ValidateChecklist(items); err != nil {
		return nil, err
	}
	tx, err := s.begin(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var active int
	if err := tx.WithContext(ctx).Raw("SELECT COUNT(*) FROM runs WHERE id=? AND session_id=? AND status='running'", runID, sessionID).Row().Scan(&active); err != nil {
		return nil, err
	}
	if active == 0 {
		return nil, nil
	}
	event, err := appendEvent(ctx, tx, sessionID, "checklist", map[string]any{"runId": runID, "items": items})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}
	return &event, nil
}

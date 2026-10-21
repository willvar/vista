package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"vista/internal/workerproto"
)

const (
	MaxMemories      = 128
	MaxMemoryBytes   = 4096
	MaxMemoriesBytes = 32 << 10
)

var (
	ErrInvalidMemory = errors.New("invalid memory change")
	ErrMemoryChanged = errors.New("memory changed or was deleted; list current memories before editing again")
	ErrMemoryExists  = errors.New("this memory already exists")
	ErrMemoryLimit   = errors.New("saved preferences are full; shorten or remove existing memories")
)

type Memory struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

func (s *Store) Memories(ctx context.Context) ([]Memory, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,text,created_at,updated_at FROM memories ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Memory{}
	for rows.Next() {
		var item Memory
		if err := rows.Scan(&item.ID, &item.Text, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func ValidateMemoryChange(change workerproto.MemoryChange) error {
	if id, err := uuid.Parse(change.ID); err != nil || id.String() != change.ID {
		return fmt.Errorf("%w: invalid id", ErrInvalidMemory)
	}
	validText := func(text string) bool {
		return strings.TrimSpace(text) != "" && utf8.ValidString(text) && len(text) <= MaxMemoryBytes && !strings.ContainsRune(text, '\x00')
	}
	switch change.Action {
	case "add":
		if !validText(change.Text) || change.PreviousText != "" {
			return ErrInvalidMemory
		}
	case "update":
		if !validText(change.Text) || !validText(change.PreviousText) {
			return ErrInvalidMemory
		}
	case "remove":
		if change.Text != "" || !validText(change.PreviousText) {
			return ErrInvalidMemory
		}
	default:
		return ErrInvalidMemory
	}
	return nil
}

// ChangeMemory uses the previous text as a compare-and-swap guard. Editing one
// preference never replaces unrelated edits made by another session or the UI.
func (s *Store) ChangeMemory(ctx context.Context, change workerproto.MemoryChange) error {
	_, err := s.changeMemory(ctx, "", "", change)
	return err
}

func (s *Store) ChangeRunMemory(ctx context.Context, sessionID, runID string, change workerproto.MemoryChange) (*Event, error) {
	return s.changeMemory(ctx, sessionID, runID, change)
}

func (s *Store) changeMemory(ctx context.Context, sessionID, runID string, change workerproto.MemoryChange) (*Event, error) {
	if err := ValidateMemoryChange(change); err != nil {
		return nil, err
	}
	change.Text = strings.TrimSpace(change.Text)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if sessionID != "" {
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM runs WHERE id=? AND session_id=?`, runID, sessionID).Scan(&status); err != nil {
			return nil, err
		}
		if status != "running" {
			return nil, errors.New("memory change belongs to an inactive run")
		}
	}
	var previous string
	if change.Action != "add" {
		err := tx.QueryRowContext(ctx, `SELECT text FROM memories WHERE id=?`, change.ID).Scan(&previous)
		if errors.Is(err, sql.ErrNoRows) || err == nil && previous != change.PreviousText {
			return nil, ErrMemoryChanged
		}
		if err != nil {
			return nil, err
		}
	}
	if change.Action != "remove" {
		var duplicates, count, bytes int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories WHERE text=? AND id<>?`, change.Text, change.ID).Scan(&duplicates); err != nil {
			return nil, err
		}
		if duplicates != 0 {
			return nil, ErrMemoryExists
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(text AS BLOB))),0) FROM memories`).Scan(&count, &bytes); err != nil {
			return nil, err
		}
		if change.Action == "add" && count >= MaxMemories || bytes-len(previous)+len(change.Text) > MaxMemoriesBytes {
			return nil, ErrMemoryLimit
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	switch change.Action {
	case "add":
		_, err = tx.ExecContext(ctx, `INSERT INTO memories(id,text,created_at,updated_at) VALUES(?,?,?,?)`, change.ID, change.Text, now, now)
	case "update":
		_, err = tx.ExecContext(ctx, `UPDATE memories SET text=?,updated_at=? WHERE id=?`, change.Text, now, change.ID)
	case "remove":
		_, err = tx.ExecContext(ctx, `DELETE FROM memories WHERE id=?`, change.ID)
	}
	if err != nil {
		return nil, err
	}
	var event *Event
	if sessionID != "" {
		var saved Event
		saved, err = appendEvent(ctx, tx, sessionID, "memory_changed", map[string]any{"runId": runID, "change": change})
		if err != nil {
			return nil, err
		}
		event = &saved
	}
	return event, tx.Commit()
}

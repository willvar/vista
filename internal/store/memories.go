package store

import (
	"context"
	"database/sql"
	"encoding/json"
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
	ErrMemoryLimit   = errors.New("saved preferences are full; consolidate related entries or shorten their wording")
)

type Memory struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

func (s *Store) Memories(ctx context.Context) ([]Memory, error) {
	return readMemories(ctx, s.db)
}

func (s *Store) MemoryCount(ctx context.Context) (int, error) {
	var total int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM memories").Scan(&total)
	return total, err
}

type MemoryPage struct {
	Pagination
	Memories []Memory `json:"memories"`
}

func (s *Store) MemoryPage(ctx context.Context, page Pagination) (MemoryPage, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return MemoryPage{}, err
	}
	defer tx.Rollback()
	var total int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM memories").Scan(&total); err != nil {
		return MemoryPage{}, err
	}
	result := MemoryPage{Pagination: page.WithTotal(total)}
	result.Memories, err = readMemories(ctx, tx, result.PageSize, result.offset())
	return result, err
}

func readMemories(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, pagination ...int) ([]Memory, error) {
	query := `SELECT id,text,created_at,updated_at FROM memories ORDER BY created_at,id`
	var args []any
	if len(pagination) == 2 {
		query += " LIMIT ? OFFSET ?"
		args = []any{pagination[0], pagination[1]}
	}
	rows, err := db.QueryContext(ctx, query, args...)
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
	if change.Changes != nil && (change.Action != "update" || change.ID != "" || change.Text != "" || change.PreviousText != "" || len(change.Changes) == 0 || len(change.Changes) > MaxMemories) {
		return fmt.Errorf("%w: use either single-edit fields or 1-%d update/remove changes", ErrInvalidMemory, MaxMemories)
	}
	validText := func(text string) bool {
		return strings.TrimSpace(text) != "" && utf8.ValidString(text) && len(text) <= MaxMemoryBytes && !strings.ContainsRune(text, '\x00')
	}
	seen := make(map[string]bool)
	for _, edit := range memoryEdits(change) {
		if id, err := uuid.Parse(edit.ID); err != nil || id.String() != edit.ID {
			return fmt.Errorf("%w: invalid id", ErrInvalidMemory)
		}
		if seen[edit.ID] {
			return fmt.Errorf("%w: each memory may appear only once", ErrInvalidMemory)
		}
		seen[edit.ID] = true
		switch edit.Action {
		case "add":
			if change.Changes != nil || !validText(edit.Text) || edit.PreviousText != "" {
				return ErrInvalidMemory
			}
		case "update":
			if !validText(edit.Text) || !validText(edit.PreviousText) {
				return ErrInvalidMemory
			}
		case "remove":
			if edit.Text != "" || !validText(edit.PreviousText) {
				return ErrInvalidMemory
			}
		default:
			return ErrInvalidMemory
		}
	}
	// Leave room for run/task identity and the ACK in Worker IPC, and for the
	// audit wrapper. Oversized calls must fail before they can break the pipe.
	encoded, err := json.Marshal(change)
	if err != nil || len(encoded) > workerproto.MaxResultFrame-(4<<10) {
		return fmt.Errorf("%w: memory change exceeds the transfer size; shorten the replacement text or consolidate fewer preferences at once", ErrInvalidMemory)
	}
	return nil
}

func memoryEdits(change workerproto.MemoryChange) []workerproto.MemoryEdit {
	if change.Changes != nil {
		return change.Changes
	}
	return []workerproto.MemoryEdit{{Action: change.Action, ID: change.ID, Text: change.Text, PreviousText: change.PreviousText}}
}

// ChangeMemory uses the previous text as a compare-and-swap guard. Editing one
// or more preferences never replaces unrelated edits by another session or UI.
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
	edits := append([]workerproto.MemoryEdit(nil), memoryEdits(change)...)
	for i := range edits {
		edits[i].Text = strings.TrimSpace(edits[i].Text)
	}
	if change.Changes != nil {
		change.Changes = edits
	} else {
		change.Text = edits[0].Text
	}
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
	items, err := readMemories(ctx, tx)
	if err != nil {
		return nil, err
	}
	final := make(map[string]Memory, len(items))
	for _, item := range items {
		final[item.ID] = item
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, edit := range edits {
		item, exists := final[edit.ID]
		if edit.Action == "add" {
			if exists {
				return nil, ErrMemoryExists
			}
			item = Memory{ID: edit.ID, CreatedAt: now}
		} else if !exists || item.Text != edit.PreviousText {
			return nil, ErrMemoryChanged
		}
		if edit.Action == "remove" {
			delete(final, edit.ID)
		} else {
			item.Text, item.UpdatedAt = edit.Text, now
			final[edit.ID] = item
		}
	}
	texts := make(map[string]bool, len(final))
	bytes := 0
	for _, item := range final {
		if texts[item.Text] {
			return nil, ErrMemoryExists
		}
		texts[item.Text] = true
		bytes += len(item.Text)
	}
	if len(final) > MaxMemories || bytes > MaxMemoriesBytes {
		return nil, ErrMemoryLimit
	}
	// Clear all affected rows before reinserting their final values. This also
	// permits text swaps and retaining the text of a removed duplicate without
	// transient UNIQUE violations. IDs and creation times stay unchanged.
	for _, edit := range edits {
		if edit.Action != "add" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM memories WHERE id=?`, edit.ID); err != nil {
				return nil, err
			}
		}
	}
	for _, edit := range edits {
		if edit.Action != "remove" {
			item := final[edit.ID]
			if _, err := tx.ExecContext(ctx, `INSERT INTO memories(id,text,created_at,updated_at) VALUES(?,?,?,?)`, item.ID, item.Text, item.CreatedAt, item.UpdatedAt); err != nil {
				return nil, err
			}
		}
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
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return event, nil
}

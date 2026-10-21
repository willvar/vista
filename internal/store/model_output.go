package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"vista/internal/agentconfig"
)

const MaxModelOutputBytes = 256 << 10
const MaxModelOutputPageBytes = 32 << 10

var ErrModelOutputRange = errors.New("invalid model output offset")

type ModelOutputPage struct {
	RunID          string `json:"runId"`
	Turn           int32  `json:"turn"`
	Offset         int64  `json:"offset"`
	NextOffset     int64  `json:"nextOffset"`
	AvailableBytes int64  `json:"availableBytes"`
	LastSequence   int64  `json:"lastSequence"`
	Content        string `json:"content"`
	Complete       bool   `json:"complete"`
}

type ModelOutputRef struct {
	Turn  int32
	Bytes int
}

func (s *Store) AppendModelDelta(ctx context.Context, sessionID, runID string, turn int32, depth int, content string, taskIDs ...string) (*Event, error) {
	if len(taskIDs) > 2 || turn <= 0 || depth < 0 || depth > agentconfig.MaxDelegationDepth || content == "" || len(content) > 2048 || !utf8.ValidString(content) {
		return nil, errors.New("invalid model text fragment")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	taskID := ""
	if len(taskIDs) > 0 {
		taskID = taskIDs[0]
	}
	active, err := executionActive(ctx, tx, sessionID, runID, taskID)
	if err != nil || !active {
		return nil, err
	}
	var offset int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(offset+length),0) FROM model_output_chunks WHERE session_id=? AND run_id=? AND turn=?", sessionID, runID, turn).Scan(&offset); err != nil {
		return nil, err
	}
	if offset+int64(len(content)) > MaxModelOutputBytes {
		return nil, errors.New("model output exceeds 256 KiB")
	}
	data := map[string]any{"runId": runID, "turn": turn, "depth": depth, "content": content}
	if len(taskIDs) > 0 && taskIDs[0] != "" {
		data["taskId"] = taskIDs[0]
	}
	if len(taskIDs) > 1 && taskIDs[1] != "" {
		data["parentTaskId"] = taskIDs[1]
	}
	event, err := appendEvent(ctx, tx, sessionID, "model_delta", data)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO model_output_chunks(session_id,run_id,turn,offset,length,event_sequence) VALUES(?,?,?,?,?,?)", sessionID, runID, turn, offset, len(content), event.Sequence); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &event, nil
}

func (s *Store) ReadModelOutput(ctx context.Context, sessionID, runID string, turn int32, offset int64, limit int) (ModelOutputPage, error) {
	return s.readModelOutput(ctx, sessionID, runID, turn, offset, limit, false)
}

// ReadFullModelOutput returns the output currently saved for this turn in one
// read transaction. A running or interrupted turn remains marked incomplete.
func (s *Store) ReadFullModelOutput(ctx context.Context, sessionID, runID string, turn int32) (ModelOutputPage, error) {
	return s.readModelOutput(ctx, sessionID, runID, turn, 0, 4096, true)
}

func (s *Store) readModelOutput(ctx context.Context, sessionID, runID string, turn int32, offset int64, limit int, full bool) (ModelOutputPage, error) {
	if turn <= 0 || offset < 0 || limit < 1 || limit > MaxModelOutputPageBytes {
		return ModelOutputPage{}, ErrModelOutputRange
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ModelOutputPage{}, err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM runs WHERE id=? AND session_id=?)", runID, sessionID).Scan(&exists); err != nil {
		return ModelOutputPage{}, err
	}
	if !exists {
		return ModelOutputPage{}, ErrNotFound
	}
	page := ModelOutputPage{RunID: runID, Turn: turn, Offset: offset, NextOffset: offset}
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(offset+length),0),COALESCE(MAX(event_sequence),0) FROM model_output_chunks WHERE session_id=? AND run_id=? AND turn=?", sessionID, runID, turn).Scan(&page.AvailableBytes, &page.LastSequence); err != nil {
		return ModelOutputPage{}, err
	}
	if page.AvailableBytes == 0 {
		return ModelOutputPage{}, ErrNotFound
	}
	if full {
		limit = int(page.AvailableBytes)
	}
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM messages WHERE session_id=? AND run_id=? AND output_turn=? AND output_bytes=?)", sessionID, runID, turn, page.AvailableBytes).Scan(&page.Complete); err != nil {
		return ModelOutputPage{}, err
	}
	if !page.Complete {
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE session_id=? AND kind='agent_task'
   AND json_extract(data,'$.runId')=? AND json_extract(data,'$.task.status')='succeeded'
   AND json_extract(data,'$.task.report.runId')=? AND json_extract(data,'$.task.report.turn')=?
   AND json_extract(data,'$.task.report.bytes')=?)`, sessionID, runID, runID, turn, page.AvailableBytes).Scan(&page.Complete); err != nil {
			return ModelOutputPage{}, err
		}
	}
	if offset > page.AvailableBytes {
		return ModelOutputPage{}, ErrModelOutputRange
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.offset,c.length,e.data FROM model_output_chunks c
 JOIN events e ON e.session_id=c.session_id AND e.sequence=c.event_sequence
 WHERE c.session_id=? AND c.run_id=? AND c.turn=? AND c.offset+c.length>?
 ORDER BY c.offset`, sessionID, runID, turn, offset)
	if err != nil {
		return ModelOutputPage{}, err
	}
	defer rows.Close()
	output := make([]byte, 0, limit)
	for len(output) < limit && rows.Next() {
		var chunkOffset int64
		var chunkLength int
		var raw []byte
		if err := rows.Scan(&chunkOffset, &chunkLength, &raw); err != nil {
			return ModelOutputPage{}, err
		}
		var event struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(raw, &event); err != nil || len(event.Content) != chunkLength {
			return ModelOutputPage{}, errors.New("model output index is inconsistent")
		}
		if page.NextOffset < chunkOffset || page.NextOffset > chunkOffset+int64(chunkLength) {
			return ModelOutputPage{}, errors.New("model output has a gap")
		}
		part := event.Content[page.NextOffset-chunkOffset:]
		if !utf8.ValidString(part) {
			return ModelOutputPage{}, ErrModelOutputRange
		}
		n := min(limit-len(output), len(part))
		for n > 0 && !utf8.ValidString(part[:n]) {
			n--
		}
		if n == 0 {
			break
		}
		output = append(output, part[:n]...)
		page.NextOffset += int64(n)
		if n < len(part) {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return ModelOutputPage{}, err
	}
	if page.NextOffset < page.AvailableBytes && len(output) == 0 {
		return ModelOutputPage{}, fmt.Errorf("model output page could not advance from %d", offset)
	}
	page.Content = string(output)
	return page, nil
}

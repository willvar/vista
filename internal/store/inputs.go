package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrNotRunning = errors.New("session has no running execution")
var ErrInputConsumed = errors.New("input has already been delivered or removed")

func (s *Store) QueueInput(ctx context.Context, sessionID, inputID, mode, content string, modelKey ...string) (QueuedInput, Event, error) {
	return s.QueueInputWithImages(ctx, sessionID, inputID, mode, content, nil, modelKey...)
}

func (s *Store) QueueInputWithImages(ctx context.Context, sessionID, inputID, mode, content string, images []Image, modelKey ...string) (QueuedInput, Event, error) {
	unlock, lockErr := s.lockImages(ctx, images)
	if lockErr != nil {
		return QueuedInput{}, Event{}, lockErr
	}
	defer unlock()
	if err := ValidateImages(images); err != nil {
		return QueuedInput{}, Event{}, err
	}
	if mode != "now" && mode != "later" {
		return QueuedInput{}, Event{}, errors.New("invalid input delivery mode")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return QueuedInput{}, Event{}, err
	}
	defer tx.Rollback()
	var open int
	if err := tx.QueryRowContext(ctx, "SELECT is_open FROM sessions WHERE id=?", sessionID).Scan(&open); err != nil {
		return QueuedInput{}, Event{}, err
	}
	if open == 0 {
		return QueuedInput{}, Event{}, ErrClosed
	}
	var runID string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM runs WHERE session_id=? AND status='running'", sessionID).Scan(&runID); errors.Is(err, sql.ErrNoRows) {
		return QueuedInput{}, Event{}, ErrNotRunning
	} else if err != nil {
		return QueuedInput{}, Event{}, err
	}
	var compacting bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events e JOIN runs r ON r.id=json_extract(e.data,'$.runId') WHERE e.session_id=? AND e.kind='context_status' AND r.id=? AND r.status='running' AND json_extract(e.data,'$.manual')=1)`, sessionID, runID).Scan(&compacting); err != nil {
		return QueuedInput{}, Event{}, err
	}
	if compacting {
		return QueuedInput{}, Event{}, ErrBusy
	}
	input := QueuedInput{ID: inputID, SessionID: sessionID, RunID: runID, Mode: mode, Status: "queued", Content: content, ImageCount: len(images), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if len(modelKey) > 0 {
		input.ModelKey = modelKey[0]
	}
	if len(modelKey) > 1 {
		input.ReasoningEffort = modelKey[1]
	}
	if len(modelKey) > 2 {
		input.SelectionMode = modelKey[2]
	}
	if len(modelKey) > 3 {
		input.SelectionTier = modelKey[3]
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO queued_inputs(id,session_id,run_id,model_key,reasoning_effort,selection_mode,selection_tier,mode,status,content,image_count,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", input.ID, input.SessionID, input.RunID, input.ModelKey, input.ReasoningEffort, input.SelectionMode, input.SelectionTier, input.Mode, input.Status, input.Content, input.ImageCount, input.CreatedAt); err != nil {
		return QueuedInput{}, Event{}, err
	}
	if err := s.writeImages(ctx, tx, sessionID, "queued_input_resources", "input_id", input.ID, images); err != nil {
		return QueuedInput{}, Event{}, err
	}
	event, err := appendEvent(ctx, tx, sessionID, "input_queued", input)
	if err != nil {
		return QueuedInput{}, Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return QueuedInput{}, Event{}, err
	}
	return input, event, nil
}

type CompletedReply struct {
	ID      string
	Content string
	Output  ModelOutputRef
}

func (s *Store) DeliverInput(ctx context.Context, sessionID, runID, inputID, messageID string, replies ...CompletedReply) (*Message, error) {
	if len(replies) > 1 {
		return nil, errors.New("invalid completed reply count")
	}
	if len(replies) == 1 {
		reply := replies[0]
		if reply.ID == "" || strings.TrimSpace(reply.Content) == "" || len(reply.Content) > 8192 || !utf8.ValidString(reply.Content) || reply.Output.Turn <= 0 || reply.Output.Bytes <= 0 || reply.Output.Bytes > MaxModelOutputBytes {
			return nil, errors.New("invalid completed reply")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var input QueuedInput
	err = tx.QueryRowContext(ctx, "SELECT id,session_id,run_id,model_key,reasoning_effort,selection_mode,selection_tier,mode,status,content,image_count,created_at FROM queued_inputs WHERE id=? AND session_id=?", inputID, sessionID).Scan(&input.ID, &input.SessionID, &input.RunID, &input.ModelKey, &input.ReasoningEffort, &input.SelectionMode, &input.SelectionTier, &input.Mode, &input.Status, &input.Content, &input.ImageCount, &input.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (input.RunID != runID || input.Mode != "now" || input.Status != "queued")) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var active int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE id=? AND session_id=? AND status='running'", runID, sessionID).Scan(&active); err != nil {
		return nil, err
	}
	if active == 0 {
		return nil, nil
	}
	// The previous answer and its follow-up are one durable boundary. A
	// concurrent withdrawal cannot leave a spurious reply or reorder them.
	if len(replies) == 1 {
		if err := appendCompletedReply(ctx, tx, sessionID, runID, replies[0]); err != nil {
			return nil, err
		}
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0)+1 FROM events WHERE session_id=?", sessionID).Scan(&sequence); err != nil {
		return nil, err
	}
	message := Message{ID: messageID, SessionID: sessionID, RunID: runID, Sequence: sequence, Role: "user", Content: input.Content, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if _, err := tx.ExecContext(ctx, "INSERT INTO messages(id,session_id,run_id,sequence,role,content,created_at) VALUES(?,?,?,?,?,?,?)", message.ID, message.SessionID, message.RunID, message.Sequence, message.Role, message.Content, message.CreatedAt); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO message_resources(message_id,position,hash,name) SELECT ?,position,hash,name FROM queued_input_resources WHERE input_id=?", message.ID, input.ID); err != nil {
		return nil, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(r.bytes),0) FROM message_resources JOIN resources r USING(hash) WHERE message_id=?", message.ID).Scan(&message.ImageCount, &message.ImageBytes); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM queued_input_resources WHERE input_id=?", input.ID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE queued_inputs SET status='delivered' WHERE id=?", inputID); err != nil {
		return nil, err
	}
	if _, err := appendEvent(ctx, tx, sessionID, "message", message); err != nil {
		return nil, err
	}
	if _, err := appendEvent(ctx, tx, sessionID, "input_delivered", map[string]string{"id": inputID, "runId": runID}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &message, nil
}

func appendCompletedReply(ctx context.Context, tx *sql.Tx, sessionID, runID string, reply CompletedReply) error {
	var persisted int
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(offset+length),0) FROM model_output_chunks WHERE session_id=? AND run_id=? AND turn=?", sessionID, runID, reply.Output.Turn).Scan(&persisted); err != nil {
		return err
	}
	if persisted != reply.Output.Bytes {
		return errors.New("full model output is incomplete")
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM messages WHERE session_id=? AND run_id=? AND role='assistant' AND output_turn=?)", sessionID, runID, reply.Output.Turn).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	message := Message{ID: reply.ID, SessionID: sessionID, RunID: runID, Role: "assistant", Content: reply.Content,
		OutputTurn: reply.Output.Turn, OutputBytes: reply.Output.Bytes, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0)+1 FROM events WHERE session_id=?", sessionID).Scan(&message.Sequence); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM messages WHERE session_id=? AND run_id=? AND role='assistant'", sessionID, runID).Scan(&message.ResultsAfter); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, "SELECT provider_id,model_id,model_key FROM runs WHERE id=? AND session_id=?", runID, sessionID).Scan(&message.Provider, &message.ModelID, &message.ModelKey); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO messages(id,session_id,run_id,sequence,role,content,created_at,provider_id,model_id,model_key,output_turn,output_bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", message.ID, message.SessionID, message.RunID, message.Sequence, message.Role, message.Content, message.CreatedAt, message.Provider, message.ModelID, message.ModelKey, message.OutputTurn, message.OutputBytes); err != nil {
		return err
	}
	_, err := appendEvent(ctx, tx, sessionID, "message", message)
	return err
}

func (s *Store) NextQueuedInput(ctx context.Context, sessionID string) (*QueuedInput, error) {
	var input QueuedInput
	err := s.db.QueryRowContext(ctx, "SELECT q.id,q.session_id,q.run_id,q.model_key,q.reasoning_effort,q.selection_mode,q.selection_tier,q.mode,q.status,q.content,q.image_count,q.created_at FROM queued_inputs q JOIN sessions s ON s.id=q.session_id WHERE q.session_id=? AND q.status='queued' AND s.pending_kind<>'question' ORDER BY q.created_at,q.id LIMIT 1", sessionID).Scan(&input.ID, &input.SessionID, &input.RunID, &input.ModelKey, &input.ReasoningEffort, &input.SelectionMode, &input.SelectionTier, &input.Mode, &input.Status, &input.Content, &input.ImageCount, &input.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &input, nil
}

func (s *Store) BeginQueuedRun(ctx context.Context, sessionID, inputID, runID, messageID string, selectedModelKey ...string) (*Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var input QueuedInput
	err = tx.QueryRowContext(ctx, "SELECT id,session_id,run_id,model_key,reasoning_effort,selection_mode,selection_tier,mode,status,content,image_count,created_at FROM queued_inputs WHERE id=? AND session_id=?", inputID, sessionID).Scan(&input.ID, &input.SessionID, &input.RunID, &input.ModelKey, &input.ReasoningEffort, &input.SelectionMode, &input.SelectionTier, &input.Mode, &input.Status, &input.Content, &input.ImageCount, &input.CreatedAt)
	if err != nil {
		return nil, err
	}
	if input.Status != "queued" {
		return nil, ErrBusy
	}
	if len(selectedModelKey) > 0 {
		input.ModelKey = selectedModelKey[0]
	}
	var open, running int
	var pendingKind string
	if err := tx.QueryRowContext(ctx, "SELECT is_open,pending_kind FROM sessions WHERE id=?", sessionID).Scan(&open, &pendingKind); err != nil {
		return nil, err
	}
	if open == 0 {
		return nil, ErrClosed
	}
	if pendingKind == "question" {
		return nil, ErrBusy
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE session_id=? AND status='running'", sessionID).Scan(&running); err != nil {
		return nil, err
	}
	if running != 0 {
		return nil, ErrBusy
	}
	if _, err := tx.ExecContext(ctx, "UPDATE sessions SET pending_kind='',pending_content='',pending_run_id='' WHERE id=?", sessionID); err != nil {
		return nil, err
	}
	if input.ModelKey != "" {
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET model_key=? WHERE id=?", input.ModelKey, sessionID); err != nil {
			return nil, err
		}
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, "INSERT INTO runs(id,session_id,status,created_at) VALUES(?,?,'running',?)", runID, sessionID, at); err != nil {
		return nil, err
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0)+1 FROM events WHERE session_id=?", sessionID).Scan(&sequence); err != nil {
		return nil, err
	}
	message := Message{ID: messageID, SessionID: sessionID, RunID: runID, Sequence: sequence, Role: "user", Content: input.Content, CreatedAt: at}
	if _, err := tx.ExecContext(ctx, "INSERT INTO messages(id,session_id,run_id,sequence,role,content,created_at) VALUES(?,?,?,?,?,?,?)", message.ID, message.SessionID, message.RunID, message.Sequence, message.Role, message.Content, message.CreatedAt); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO message_resources(message_id,position,hash,name) SELECT ?,position,hash,name FROM queued_input_resources WHERE input_id=?", message.ID, input.ID); err != nil {
		return nil, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(r.bytes),0) FROM message_resources JOIN resources r USING(hash) WHERE message_id=?", message.ID).Scan(&message.ImageCount, &message.ImageBytes); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM queued_input_resources WHERE input_id=?", input.ID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE queued_inputs SET status='delivered',run_id=? WHERE id=?", runID, inputID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE queued_inputs SET run_id=? WHERE session_id=? AND status='queued' AND mode='now'", runID, sessionID); err != nil {
		return nil, err
	}
	if _, err := appendEvent(ctx, tx, sessionID, "message", message); err != nil {
		return nil, err
	}
	if _, err := appendEvent(ctx, tx, sessionID, "input_delivered", map[string]string{"id": inputID, "runId": runID}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &message, nil
}

func (s *Store) MarkQueuedInputs(ctx context.Context, sessionID, runID, status string) error {
	if status != "interrupted" && status != "capacity_rejected" {
		return errors.New("invalid queued input status")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := markQueuedInTx(ctx, tx, sessionID, runID, status); err != nil {
		return err
	}
	return tx.Commit()
}

func markQueuedInTx(ctx context.Context, tx *sql.Tx, sessionID, runID, status string) error {
	// Pause only the inputs already waiting at this boundary. New submissions
	// remain deliverable; text and attachments stay available for resumption.
	if status == "interrupted" || status == "capacity_rejected" {
		status = "paused"
	}
	query := "SELECT id FROM queued_inputs WHERE session_id=? AND status='queued'"
	args := []any{sessionID}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, "UPDATE queued_inputs SET status=? WHERE id=?", status, id); err != nil {
			return err
		}
		if _, err := appendEvent(ctx, tx, sessionID, "input_status", map[string]string{"id": id, "status": status}); err != nil {
			return err
		}
	}
	return syncInputQueueState(ctx, tx, sessionID)
}

// The session flag summarizes pending inputs for the UI; individual statuses
// decide delivery. An empty queue must not leave future submissions paused.
func syncInputQueueState(ctx context.Context, tx *sql.Tx, sessionID string) error {
	var paused bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM queued_inputs WHERE session_id=? AND status='paused')", sessionID).Scan(&paused); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE sessions SET input_queue_paused=? WHERE id=? AND input_queue_paused<>?", paused, sessionID, paused)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return err
	}
	_, err = appendEvent(ctx, tx, sessionID, "input_queue_state", map[string]bool{"paused": paused})
	return err
}

func (s *Store) RecentInputs(ctx context.Context, sessionID string) ([]QueuedInput, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,session_id,run_id,model_key,reasoning_effort,selection_mode,selection_tier,mode,status,content,image_count,created_at FROM queued_inputs WHERE session_id=? AND (status IN ('queued','paused') OR id IN (SELECT id FROM queued_inputs WHERE session_id=? AND status NOT IN ('queued','paused') ORDER BY created_at DESC,id DESC LIMIT 20)) ORDER BY created_at,id", sessionID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QueuedInput{}
	for rows.Next() {
		var input QueuedInput
		if err := rows.Scan(&input.ID, &input.SessionID, &input.RunID, &input.ModelKey, &input.ReasoningEffort, &input.SelectionMode, &input.SelectionTier, &input.Mode, &input.Status, &input.Content, &input.ImageCount, &input.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, input)
	}
	return out, rows.Err()
}

func (s *Store) NextInterjection(ctx context.Context, sessionID, runID string) (*QueuedInput, error) {
	var input QueuedInput
	err := s.db.QueryRowContext(ctx, `SELECT q.id,q.session_id,q.run_id,q.model_key,q.reasoning_effort,q.selection_mode,q.selection_tier,q.mode,q.status,q.content,q.image_count,q.created_at
 FROM queued_inputs q JOIN runs r ON r.id=q.run_id
 WHERE q.session_id=? AND q.run_id=? AND q.mode='now' AND q.status='queued' AND r.status='running'
 ORDER BY q.created_at,q.id LIMIT 1`, sessionID, runID).Scan(&input.ID, &input.SessionID, &input.RunID, &input.ModelKey, &input.ReasoningEffort, &input.SelectionMode, &input.SelectionTier, &input.Mode, &input.Status, &input.Content, &input.ImageCount, &input.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &input, err
}

func (s *Store) QueuedInputImages(ctx context.Context, sessionID, inputID string) ([]Image, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.hash,r.media_type,r.bytes,i.name FROM queued_input_resources i JOIN resources r ON r.hash=i.hash JOIN queued_inputs q ON q.id=i.input_id WHERE q.session_id=? AND q.id=? ORDER BY i.position`, sessionID, inputID)
	if err != nil {
		return nil, err
	}
	refs, err := readResourceRows(rows)
	if err != nil {
		return nil, err
	}
	return s.ResolveImages(ctx, refs)
}

// UpdateInput and delivery each own a database transaction, so an acknowledged
// edit or withdrawal cannot race a Worker into executing an older copy.
func (s *Store) UpdateInput(ctx context.Context, sessionID, inputID string, content, mode *string) (QueuedInput, Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return QueuedInput{}, Event{}, err
	}
	defer tx.Rollback()
	input, err := readInput(ctx, tx, sessionID, inputID)
	if err != nil {
		return QueuedInput{}, Event{}, err
	}
	if input.Status != "queued" && input.Status != "paused" {
		return QueuedInput{}, Event{}, ErrInputConsumed
	}
	if content != nil {
		input.Content = *content
	}
	if mode != nil {
		if *mode != "now" && *mode != "later" {
			return QueuedInput{}, Event{}, errors.New("invalid input delivery mode")
		}
		input.Mode = *mode
	}
	if input.Content == "" && input.ImageCount == 0 || len(input.Content) > 8<<10 {
		return QueuedInput{}, Event{}, errors.New("input must have text or an image; text is limited to 8192 bytes")
	}
	if input.Mode == "now" {
		var running string
		err := tx.QueryRowContext(ctx, "SELECT id FROM runs WHERE session_id=? AND status='running'", sessionID).Scan(&running)
		if err == nil {
			input.RunID = running
		} else if !errors.Is(err, sql.ErrNoRows) {
			return QueuedInput{}, Event{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE queued_inputs SET content=?,mode=?,run_id=? WHERE id=?", input.Content, input.Mode, input.RunID, input.ID); err != nil {
		return QueuedInput{}, Event{}, err
	}
	event, err := appendEvent(ctx, tx, sessionID, "input_updated", input)
	if err != nil {
		return QueuedInput{}, Event{}, err
	}
	return input, event, tx.Commit()
}

func readInput(ctx context.Context, tx *sql.Tx, sessionID, inputID string) (QueuedInput, error) {
	var input QueuedInput
	err := tx.QueryRowContext(ctx, "SELECT id,session_id,run_id,model_key,reasoning_effort,selection_mode,selection_tier,mode,status,content,image_count,created_at FROM queued_inputs WHERE id=? AND session_id=?", inputID, sessionID).Scan(&input.ID, &input.SessionID, &input.RunID, &input.ModelKey, &input.ReasoningEffort, &input.SelectionMode, &input.SelectionTier, &input.Mode, &input.Status, &input.Content, &input.ImageCount, &input.CreatedAt)
	return input, err
}

func (s *Store) RemoveInput(ctx context.Context, sessionID, inputID string, withdraw bool) (QueuedInput, []Image, Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return QueuedInput{}, nil, Event{}, err
	}
	defer tx.Rollback()
	input, err := readInput(ctx, tx, sessionID, inputID)
	if err != nil {
		return QueuedInput{}, nil, Event{}, err
	}
	if input.Status != "queued" && input.Status != "paused" {
		return QueuedInput{}, nil, Event{}, ErrInputConsumed
	}
	images := []Image{}
	if withdraw {
		rows, err := tx.QueryContext(ctx, "SELECT r.hash,r.media_type,r.bytes,i.name FROM queued_input_resources i JOIN resources r ON r.hash=i.hash WHERE input_id=? ORDER BY position", input.ID)
		if err != nil {
			return QueuedInput{}, nil, Event{}, err
		}
		for rows.Next() {
			var image Image
			if err := rows.Scan(&image.Hash, &image.MediaType, &image.Bytes, &image.Name); err != nil {
				rows.Close()
				return QueuedInput{}, nil, Event{}, err
			}
			images = append(images, image)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return QueuedInput{}, nil, Event{}, err
		}
	}
	input.Status = "deleted"
	if withdraw {
		input.Status = "withdrawn"
	}
	if _, err := tx.ExecContext(ctx, "UPDATE queued_inputs SET status=? WHERE id=?", input.Status, input.ID); err != nil {
		return QueuedInput{}, nil, Event{}, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM queued_input_resources WHERE input_id=?", input.ID); err != nil {
		return QueuedInput{}, nil, Event{}, err
	}
	event, err := appendEvent(ctx, tx, sessionID, "input_status", map[string]string{"id": input.ID, "status": input.Status})
	if err != nil {
		return QueuedInput{}, nil, Event{}, err
	}
	if err := syncInputQueueState(ctx, tx, sessionID); err != nil {
		return QueuedInput{}, nil, Event{}, err
	}
	return input, images, event, tx.Commit()
}

func (s *Store) ResumeInputs(ctx context.Context, sessionID string) (Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, err
	}
	defer tx.Rollback()
	var open int
	if err := tx.QueryRowContext(ctx, "SELECT is_open FROM sessions WHERE id=?", sessionID).Scan(&open); err != nil {
		return Event{}, err
	}
	if open == 0 {
		return Event{}, ErrClosed
	}
	if _, err := tx.ExecContext(ctx, "UPDATE sessions SET input_queue_paused=0 WHERE id=?", sessionID); err != nil {
		return Event{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE queued_inputs SET status='queued' WHERE session_id=? AND status='paused'", sessionID); err != nil {
		return Event{}, err
	}
	// Rebind interjections when a resumed queue joins an execution started later.
	if _, err := tx.ExecContext(ctx, "UPDATE queued_inputs SET run_id=(SELECT id FROM runs WHERE session_id=? AND status='running') WHERE session_id=? AND status='queued' AND mode='now' AND EXISTS(SELECT 1 FROM runs WHERE session_id=? AND status='running')", sessionID, sessionID, sessionID); err != nil {
		return Event{}, err
	}
	event, err := appendEvent(ctx, tx, sessionID, "input_queue_state", map[string]bool{"paused": false})
	if err != nil {
		return Event{}, err
	}
	return event, tx.Commit()
}

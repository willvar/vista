package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"vista/internal/agentconfig"
	"vista/internal/question"
	"vista/internal/workerproto"
)

var ErrBusy = errors.New("session already has a running execution")
var ErrClosed = errors.New("session is closed")

func appendEvent(ctx context.Context, tx *sql.Tx, sessionID, kind string, data any) (Event, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return Event{}, err
	}
	limit := 32 << 10
	if kind == "message" || kind == "input_queued" || kind == "job" {
		limit = 64 << 10
	}
	if len(encoded) > limit {
		return Event{}, fmt.Errorf("%s event exceeds %d KiB", kind, limit>>10)
	}
	var sequence int64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0)+1 FROM events WHERE session_id=?", sessionID).Scan(&sequence); err != nil {
		return Event{}, err
	}
	e := Event{SessionID: sessionID, Sequence: sequence, Kind: kind, Data: encoded, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	_, err = tx.ExecContext(ctx, "INSERT INTO events(session_id,sequence,kind,data,created_at) VALUES(?,?,?,?,?)", e.SessionID, e.Sequence, e.Kind, []byte(e.Data), e.CreatedAt)
	return e, err
}
func (s *Store) AppendRunEvent(ctx context.Context, sessionID, runID, kind string, data any, taskIDs ...string) (*Event, error) {
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
	e, err := appendEvent(ctx, tx, sessionID, kind, data)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &e, nil
}
func (s *Store) RecordModelRequest(ctx context.Context, sessionID, runID, provider, modelID string, details ...string) (*Event, error) {
	if len(details) > 3 {
		return nil, errors.New("invalid model request identity")
	}
	use := &workerproto.ModelUse{Provider: provider, ID: modelID}
	if len(details) > 0 {
		use.Key = details[0]
	}
	if len(details) > 1 {
		use.Purpose = details[1]
	}
	if len(details) > 2 {
		use.ReasoningEffort = details[2]
	}
	return s.RecordModelRequestEvent(ctx, sessionID, workerproto.Result{RunID: runID, ModelUse: use})
}

// Child requests are recorded without replacing the parent run's chosen model.
func (s *Store) RecordModelRequestEvent(ctx context.Context, sessionID string, request workerproto.Result) (*Event, error) {
	use := request.ModelUse
	if use == nil || use.Provider == "" || use.ID == "" || len(use.Provider) > 64 || len(use.ID) > 200 || len(use.Key) > 64 || len(use.ReasoningEffort) > 64 || (use.Purpose != "" && use.Purpose != "summary") || request.Depth < 0 || request.Depth > agentconfig.MaxDelegationDepth {
		return nil, errors.New("invalid model request identity")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	active, err := executionActive(ctx, tx, sessionID, request.RunID, request.TaskID)
	if err != nil || !active {
		return nil, err
	}
	if request.Depth == 0 && (request.TaskID == "" || request.TaskID == "main") && use.Purpose == "" {
		if _, err := tx.ExecContext(ctx, "UPDATE runs SET provider_id=?,model_id=?,model_key=? WHERE id=? AND session_id=?", use.Provider, use.ID, use.Key, request.RunID, sessionID); err != nil {
			return nil, err
		}
	}
	data := map[string]any{"runId": request.RunID, "provider": use.Provider, "id": use.ID}
	if use.Key != "" {
		data["key"] = use.Key
	}
	if use.Purpose != "" {
		data["purpose"] = use.Purpose
	}
	if use.ReasoningEffort != "" {
		data["reasoningEffort"] = use.ReasoningEffort
	}
	if request.Turn > 0 {
		data["turn"] = request.Turn
	}
	if request.Depth > 0 {
		data["depth"] = request.Depth
	}
	if request.TaskID != "" {
		data["taskId"] = request.TaskID
	}
	if request.ParentTaskID != "" {
		data["parentTaskId"] = request.ParentTaskID
	}
	event, err := appendEvent(ctx, tx, sessionID, "model_request", data)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &event, nil
}
func (s *Store) RunningRunID(ctx context.Context, sessionID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM runs WHERE session_id=? AND status='running'", sessionID).Scan(&id)
	return id, err
}
func (s *Store) BeginRun(ctx context.Context, sessionID, runID, messageID, content string, modelKey ...string) (Message, Event, error) {
	return s.BeginRunWithImages(ctx, sessionID, runID, messageID, content, nil, modelKey...)
}

func (s *Store) BeginRunWithImages(ctx context.Context, sessionID, runID, messageID, content string, images []Image, modelKey ...string) (Message, Event, error) {
	return s.beginRun(ctx, sessionID, runID, messageID, content, images, nil, modelKey...)
}

// BeginQuestionRun checks and consumes the pending request in the same
// transaction as the new user message, so stale tabs cannot answer it twice.
func (s *Store) BeginQuestionRun(ctx context.Context, sessionID, runID, messageID string, reply question.Reply, modelKey ...string) (Message, Event, error) {
	return s.beginRun(ctx, sessionID, runID, messageID, "", nil, &reply, modelKey...)
}

func (s *Store) beginRun(ctx context.Context, sessionID, runID, messageID, content string, images []Image, reply *question.Reply, modelKey ...string) (Message, Event, error) {
	unlock, lockErr := s.lockImages(ctx, images)
	if lockErr != nil {
		return Message{}, Event{}, lockErr
	}
	defer unlock()
	if err := ValidateImages(images); err != nil {
		return Message{}, Event{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, Event{}, err
	}
	defer tx.Rollback()
	var open int
	var pendingKind, pendingRunID, pendingContent string
	if err = tx.QueryRowContext(ctx, "SELECT is_open,pending_kind,pending_run_id,pending_content FROM sessions WHERE id=?", sessionID).Scan(&open, &pendingKind, &pendingRunID, &pendingContent); err != nil {
		return Message{}, Event{}, err
	}
	if open == 0 {
		return Message{}, Event{}, ErrClosed
	}
	var questionCallID string
	if reply != nil {
		if pendingKind != "question" || reply.RunID == "" || pendingRunID != reply.RunID {
			return Message{}, Event{}, ErrNoPendingQuestion
		}
		pending := pendingFromStorage(pendingKind, pendingRunID, pendingContent)
		if err := ValidatePendingReply(pending); err != nil {
			return Message{}, Event{}, err
		}
		questionCallID = pending.CallID
		content, err = question.FormatAnswers(pending.Questions, reply.Answers)
		if err != nil {
			return Message{}, Event{}, fmt.Errorf("%w: %v", ErrInvalidQuestionReply, err)
		}
		if len(content) > workerproto.MaxPromptBytes {
			return Message{}, Event{}, fmt.Errorf("%w: answer exceeds 8192 bytes", ErrInvalidQuestionReply)
		}
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE session_id=? AND status='running'", sessionID).Scan(&count); err != nil {
		return Message{}, Event{}, err
	}
	if count > 0 {
		return Message{}, Event{}, ErrBusy
	}
	if _, err := tx.ExecContext(ctx, "UPDATE sessions SET pending_kind='',pending_content='',pending_run_id='' WHERE id=?", sessionID); err != nil {
		return Message{}, Event{}, err
	}
	if len(modelKey) > 0 && modelKey[0] != "" {
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET model_key=? WHERE id=?", modelKey[0], sessionID); err != nil {
			return Message{}, Event{}, err
		}
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, "INSERT INTO runs(id,session_id,status,created_at) VALUES(?,?,'running',?)", runID, sessionID, at); err != nil {
		return Message{}, Event{}, err
	}
	var next int64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0)+1 FROM events WHERE session_id=?", sessionID).Scan(&next); err != nil {
		return Message{}, Event{}, err
	}
	m := Message{ID: messageID, SessionID: sessionID, RunID: runID, Sequence: next, Role: "user", Content: content, QuestionCallID: questionCallID, ImageCount: len(images), CreatedAt: at}
	for _, image := range images {
		m.ImageBytes += len(image.Data)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO messages(id,session_id,run_id,sequence,role,content,created_at) VALUES(?,?,?,?,?,?,?)", m.ID, m.SessionID, m.RunID, m.Sequence, m.Role, m.Content, m.CreatedAt); err != nil {
		return Message{}, Event{}, err
	}
	if err := s.writeImages(ctx, tx, sessionID, "message_resources", "message_id", m.ID, images); err != nil {
		return Message{}, Event{}, err
	}
	e, err := appendEvent(ctx, tx, sessionID, "message", m)
	if err != nil {
		return Message{}, Event{}, err
	}
	if e.Sequence != m.Sequence {
		return Message{}, Event{}, fmt.Errorf("message/event sequence mismatch")
	}
	if err = tx.Commit(); err != nil {
		return Message{}, Event{}, err
	}
	return m, e, nil
}

// FirstMessageTitleCandidate is available as soon as the first user message
// is stored. A manual title makes the session ineligible for automatic naming.
func (s *Store) FirstMessageTitleCandidate(ctx context.Context, sessionID, messageID string) (string, bool, error) {
	var prompt string
	err := s.db.QueryRowContext(ctx, `SELECT m.content FROM messages m JOIN sessions s ON s.id=m.session_id
 WHERE m.session_id=? AND m.id=? AND m.role='user' AND s.title='New session'
 AND NOT EXISTS(SELECT 1 FROM events e WHERE e.session_id=s.id AND e.kind='session_renamed')
 AND m.sequence=(SELECT MIN(sequence) FROM messages WHERE session_id=s.id AND role='user')
 LIMIT 1`, sessionID, messageID).Scan(&prompt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return prompt, err == nil, err
}

// SetGeneratedTitle only replaces the untouched placeholder and records an
// event so an open conversation can refresh its sidebar immediately.
func (s *Store) SetGeneratedTitle(ctx context.Context, sessionID, messageID, title string) (*Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET title=? WHERE id=? AND title='New session' AND NOT EXISTS(SELECT 1 FROM events WHERE session_id=? AND kind='session_renamed') AND EXISTS(SELECT 1 FROM messages WHERE session_id=? AND id=? AND role='user' AND sequence=(SELECT MIN(sequence) FROM messages WHERE session_id=? AND role='user'))`, title, sessionID, sessionID, sessionID, messageID, sessionID)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return nil, err
	}
	event, err := appendEvent(ctx, tx, sessionID, "session_title", map[string]string{"title": title})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &event, nil
}
func (s *Store) FinishRun(ctx context.Context, sessionID, runID, messageID, status, content string, output ...ModelOutputRef) (*Event, error) {
	return s.FinishRunWithPending(ctx, sessionID, runID, messageID, status, content, nil, output...)
}

func (s *Store) FinishRunWithPending(ctx context.Context, sessionID, runID, messageID, status, content string, pending *PendingReply, output ...ModelOutputRef) (*Event, error) {
	if status != "succeeded" && status != "failed" && status != "interrupted" {
		return nil, errors.New("invalid run result")
	}
	if pending != nil {
		if status != "succeeded" {
			return nil, errors.New("invalid pending reply")
		}
		if err := ValidatePendingReply(pending); err != nil {
			return nil, err
		}
	}
	if len(output) > 1 || (len(output) == 1 && (status != "succeeded" || output[0].Turn <= 0 || output[0].Bytes <= 0 || output[0].Bytes > MaxModelOutputBytes)) {
		return nil, errors.New("invalid full model output reference")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	manual, err := isCompaction(ctx, tx, sessionID, runID)
	if err != nil {
		return nil, err
	}
	if manual {
		event, err := finishCompactionInTx(ctx, tx, sessionID, runID, status, content)
		if err != nil {
			return nil, err
		}
		return event, tx.Commit()
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, "UPDATE runs SET status=?,finished_at=? WHERE id=? AND session_id=? AND status='running'", status, at, runID, sessionID)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed == 0 {
		return nil, nil
	}
	unknown, err := appendUnknownTools(ctx, tx, sessionID, runID)
	if err != nil {
		return nil, err
	}
	if unknown > 0 && status == "succeeded" {
		status = "failed"
		pending = nil
		content = "Run ended without confirmed results for one or more tools"
		if _, err := tx.ExecContext(ctx, "UPDATE runs SET status='failed' WHERE id=?", runID); err != nil {
			return nil, err
		}
	}
	if status == "succeeded" {
		if pending != nil {
			copy := *pending
			copy.RunID = runID
			pending = &copy
			storedContent, err := pendingStorageContent(pending)
			if err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE sessions SET pending_kind=?,pending_content=?,pending_run_id=? WHERE id=?", pending.Kind, storedContent, pending.RunID, sessionID); err != nil {
				return nil, err
			}
		}
	}
	if len(output) == 1 && status == "succeeded" {
		var persisted int
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(offset+length),0) FROM model_output_chunks WHERE session_id=? AND run_id=? AND turn=?", sessionID, runID, output[0].Turn).Scan(&persisted); err != nil {
			return nil, err
		}
		if persisted != output[0].Bytes {
			return nil, errors.New("full model output is incomplete")
		}
	}
	var message *Message
	if content != "" && status != "interrupted" {
		var next int64
		if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0)+1 FROM events WHERE session_id=?", sessionID).Scan(&next); err != nil {
			return nil, err
		}
		m := Message{ID: messageID, SessionID: sessionID, RunID: runID, Sequence: next, Role: "assistant", Content: content, CreatedAt: at, Failure: status == "failed"}
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM messages WHERE session_id=? AND run_id=? AND role='assistant'", sessionID, runID).Scan(&m.ResultsAfter); err != nil {
			return nil, err
		}
		if status == "succeeded" && len(output) == 1 {
			m.OutputTurn, m.OutputBytes = output[0].Turn, output[0].Bytes
		}
		if err = tx.QueryRowContext(ctx, "SELECT provider_id,model_id,model_key FROM runs WHERE id=?", runID).Scan(&m.Provider, &m.ModelID, &m.ModelKey); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO messages(id,session_id,run_id,sequence,role,content,created_at,provider_id,model_id,model_key,output_turn,output_bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", m.ID, m.SessionID, m.RunID, m.Sequence, m.Role, m.Content, m.CreatedAt, m.Provider, m.ModelID, m.ModelKey, m.OutputTurn, m.OutputBytes); err != nil {
			return nil, err
		}
		message = &m
	}
	resultData := map[string]any{"runId": runID, "status": status, "message": message, "pendingReply": pending}
	if unknown > 0 {
		resultData["unknownTools"] = unknown
	}
	if status == "interrupted" {
		resultData["reason"] = content
	}
	e, err := appendEvent(ctx, tx, sessionID, "run_finished", resultData)
	if err != nil {
		return nil, err
	}
	if status == "interrupted" || status == "failed" || unknown > 0 {
		if err := markQueuedInTx(ctx, tx, sessionID, runID, "interrupted"); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *Store) InterruptRunning(ctx context.Context, sessionID string, reason ...string) ([]Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	query := "SELECT id,session_id FROM runs WHERE status='running'"
	args := []any{}
	if sessionID != "" {
		query += " AND session_id=?"
		args = append(args, sessionID)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	type run struct{ id, session string }
	runs := []run{}
	for rows.Next() {
		var x run
		if err := rows.Scan(&x.id, &x.session); err != nil {
			rows.Close()
			return nil, err
		}
		runs = append(runs, x)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	events, err := interruptAgentTasks(ctx, tx, sessionID)
	if err != nil {
		return nil, err
	}
	for _, x := range runs {
		manual, err := isCompaction(ctx, tx, x.session, x.id)
		if err != nil {
			return nil, err
		}
		if manual {
			cause := "Compaction interrupted"
			if len(reason) > 0 && reason[0] != "" {
				cause = reason[0]
			}
			event, err := finishCompactionInTx(ctx, tx, x.session, x.id, "interrupted", cause)
			if err != nil {
				return nil, err
			}
			if event != nil {
				events = append(events, *event)
			}
			continue
		}
		at := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err = tx.ExecContext(ctx, "UPDATE runs SET status='interrupted',finished_at=? WHERE id=?", at, x.id); err != nil {
			return nil, err
		}
		unknown, err := appendUnknownTools(ctx, tx, x.session, x.id)
		if err != nil {
			return nil, err
		}
		cause := "Execution interrupted"
		if len(reason) > 0 && reason[0] != "" {
			cause = reason[0]
		}
		e, err := appendEvent(ctx, tx, x.session, "run_finished", map[string]any{"runId": x.id, "status": "interrupted", "reason": cause, "unknownTools": unknown, "message": nil})
		if err != nil {
			return nil, err
		}
		events = append(events, e)
		if err := markQueuedInTx(ctx, tx, x.session, x.id, "interrupted"); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return events, nil
}

func appendUnknownTools(ctx context.Context, tx *sql.Tx, sessionID, runID string, onlyTask ...string) (int, error) {
	var start int64
	if err := tx.QueryRowContext(ctx, "SELECT MIN(sequence) FROM messages WHERE session_id=? AND run_id=? AND role='user'", sessionID, runID).Scan(&start); err != nil {
		return 0, err
	}
	query := `SELECT sequence,data FROM events e WHERE session_id=? AND kind='tool' AND sequence>=?
 AND NOT EXISTS(SELECT 1 FROM agent_tasks t WHERE t.run_id=? AND t.task_id=json_extract(e.data,'$.taskId') AND t.background=1 AND t.status='started')`
	args := []any{sessionID, start, runID}
	if len(onlyTask) > 0 {
		query += " AND json_extract(e.data,'$.taskId')=?"
		args = append(args, onlyTask[0])
	}
	rows, err := tx.QueryContext(ctx, query+" ORDER BY sequence", args...)
	if err != nil {
		return 0, err
	}
	type progress struct {
		RunID        string `json:"runId"`
		TaskID       string `json:"taskId"`
		ParentTaskID string `json:"parentTaskId"`
		Depth        int    `json:"depth"`
		Status       string `json:"status"`
		Tool         struct {
			CallID  string `json:"callId"`
			Name    string `json:"name"`
			Summary string `json:"summary"`
		} `json:"tool"`
	}
	type started struct {
		sequence int64
		value    progress
	}
	pending := map[string]started{}
	for rows.Next() {
		var sequence int64
		var raw []byte
		if err := rows.Scan(&sequence, &raw); err != nil {
			rows.Close()
			return 0, err
		}
		var item progress
		if err := json.Unmarshal(raw, &item); err != nil {
			rows.Close()
			return 0, err
		}
		if item.RunID != runID || item.Tool.CallID == "" {
			continue
		}
		switch item.Status {
		case "started":
			pending[item.Tool.CallID] = started{sequence, item}
		case "succeeded", "failed", "unknown":
			delete(pending, item.Tool.CallID)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	ordered := make([]started, 0, len(pending))
	for _, item := range pending {
		ordered = append(ordered, item)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].sequence < ordered[j].sequence })
	for _, item := range ordered {
		_, err := appendEvent(ctx, tx, sessionID, "tool", map[string]any{
			"runId": runID, "status": "unknown", "content": "Worker stopped before the tool result was confirmed",
			"taskId": item.value.TaskID, "parentTaskId": item.value.ParentTaskID, "depth": item.value.Depth,
			"tool": item.value.Tool,
		})
		if err != nil {
			return 0, err
		}
	}
	return len(ordered), nil
}
func (s *Store) EventsAfter(ctx context.Context, sessionID string, after int64, limit int) ([]Event, error) {
	return s.EventsThrough(ctx, sessionID, after, 0, limit)
}

// EventsThrough fixes a replay target while new events continue to arrive.
func (s *Store) EventsThrough(ctx context.Context, sessionID string, after, through int64, limit int) ([]Event, error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("invalid event page size")
	}
	// SQLite JSON functions return TEXT; normalize data before scanning it into json.RawMessage.
	query := "SELECT sequence,kind,CAST(data AS BLOB) AS data,created_at FROM events WHERE session_id=? AND sequence>?"
	args := []any{sessionID, after}
	if through > 0 {
		query += " AND sequence<=?"
		args = append(args, through)
	}
	query += " ORDER BY sequence LIMIT ?"
	rows, err := s.reader().QueryContext(ctx, query, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		e := Event{SessionID: sessionID}
		if err := rows.Scan(&e.Sequence, &e.Kind, &e.Data, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Store) EventsBefore(ctx context.Context, sessionID string, before int64, limit int) ([]Event, error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("invalid event page size")
	}
	query := "SELECT sequence,kind,CAST(data AS BLOB) AS data,created_at FROM events WHERE session_id=?"
	args := []any{sessionID}
	if before > 0 {
		query += " AND sequence<?"
		args = append(args, before)
	}
	query += " ORDER BY sequence DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		e := Event{SessionID: sessionID}
		if err := rows.Scan(&e.Sequence, &e.Kind, &e.Data, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// ModelResultEvents returns persisted model calls for the runs on one message page.
func (s *Store) ModelResultEvents(ctx context.Context, sessionID string, runIDs []string) ([]Event, error) {
	if len(runIDs) == 0 {
		return []Event{}, nil
	}
	if len(runIDs) > 100 {
		return nil, errors.New("too many runs")
	}
	query := "SELECT sequence,kind,CAST(data AS BLOB) AS data,created_at FROM events WHERE session_id=? AND kind='model_result' AND json_extract(data,'$.runId') IN (" + strings.TrimRight(strings.Repeat("?,", len(runIDs)), ",") + ") ORDER BY sequence"
	args := make([]any, 0, len(runIDs)+1)
	args = append(args, sessionID)
	for _, id := range runIDs {
		args = append(args, id)
	}
	rows, err := s.reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		e := Event{SessionID: sessionID}
		if err := rows.Scan(&e.Sequence, &e.Kind, &e.Data, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Store) MessagesAfter(ctx context.Context, sessionID string, after int64, limit int) ([]Message, error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("invalid message page size")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,run_id,sequence,role,content,created_at,provider_id,model_id,model_key,output_turn,output_bytes,(SELECT COUNT(*) FROM message_resources WHERE message_id=messages.id),(SELECT COALESCE(SUM(r.bytes),0) FROM message_resources JOIN resources r USING(hash) WHERE message_id=messages.id),EXISTS(SELECT 1 FROM events e WHERE e.session_id=messages.session_id AND e.sequence=messages.sequence AND e.kind='run_finished' AND json_extract(e.data,'$.status')='failed') FROM messages WHERE session_id=? AND sequence>? ORDER BY sequence LIMIT ?", sessionID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		m := Message{SessionID: sessionID}
		if err := rows.Scan(&m.ID, &m.RunID, &m.Sequence, &m.Role, &m.Content, &m.CreatedAt, &m.Provider, &m.ModelID, &m.ModelKey, &m.OutputTurn, &m.OutputBytes, &m.ImageCount, &m.ImageBytes, &m.Failure); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) MessagesBefore(ctx context.Context, sessionID string, before int64, limit int) ([]Message, error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("invalid message page size")
	}
	query := "SELECT id,run_id,sequence,role,content,created_at,provider_id,model_id,model_key,output_turn,output_bytes,(SELECT COUNT(*) FROM message_resources WHERE message_id=messages.id),(SELECT COALESCE(SUM(r.bytes),0) FROM message_resources JOIN resources r USING(hash) WHERE message_id=messages.id),EXISTS(SELECT 1 FROM events e WHERE e.session_id=messages.session_id AND e.sequence=messages.sequence AND e.kind='run_finished' AND json_extract(e.data,'$.status')='failed') FROM messages WHERE session_id=?"
	args := []any{sessionID}
	if before > 0 {
		query += " AND sequence<?"
		args = append(args, before)
	}
	query += " ORDER BY sequence DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		m := Message{SessionID: sessionID}
		if err := rows.Scan(&m.ID, &m.RunID, &m.Sequence, &m.Role, &m.Content, &m.CreatedAt, &m.Provider, &m.ModelID, &m.ModelKey, &m.OutputTurn, &m.OutputBytes, &m.ImageCount, &m.ImageBytes, &m.Failure); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

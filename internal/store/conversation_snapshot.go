package store

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"
)

// SyncCheckpoint describes committed state at one event boundary. It is not an
// event and must never advance a client's successfully applied event cursor.
type SyncCheckpoint struct {
	ThroughSequence  int64   `json:"throughSequence"`
	RevertedSequence int64   `json:"revertedSequence"`
	Session          Session `json:"session"`
}

type ConversationSnapshot struct {
	SyncCheckpoint
	Messages       []Message         `json:"messages"`
	Events         []Event           `json:"events"`
	HasOlder       bool              `json:"hasOlder"`
	HasOlderEvents bool              `json:"hasOlderEvents"`
	EventsBefore   int64             `json:"eventsBefore"`
	Results        []Event           `json:"-"`
	Boundaries     map[string]int64  `json:"-"`
	Efforts        map[string]string `json:"-"`
}

func (s *Store) checkpoint(ctx context.Context, id string) (SyncCheckpoint, error) {
	session, err := s.Get(ctx, id)
	if err != nil {
		return SyncCheckpoint{}, err
	}
	var state struct {
		ThroughSequence     int64
		RevertedSequence    int64
		LatestReplySequence int64
		LatestRunStatus     string
		PendingInputCount   int
		Executing           bool
		Background          bool
	}
	err = s.orm.WithContext(ctx).Raw(`SELECT
 COALESCE((SELECT MAX(sequence) FROM events WHERE session_id=?),0) AS through_sequence,
 COALESCE((SELECT MAX(sequence) FROM events WHERE session_id=? AND kind='conversation_reverted'),0) AS reverted_sequence,
 COALESCE((SELECT MAX(sequence) FROM messages WHERE session_id=? AND role='assistant'),0) AS latest_reply_sequence,
 COALESCE((SELECT status FROM runs WHERE session_id=? ORDER BY created_at DESC,id DESC LIMIT 1),'') AS latest_run_status,
 (SELECT COUNT(*) FROM queued_inputs WHERE session_id=? AND status IN ('queued','paused')) AS pending_input_count,
 EXISTS(SELECT 1 FROM runs WHERE session_id=? AND status='running') AS executing,
 (EXISTS(SELECT 1 FROM jobs WHERE session_id=? AND status IN ('starting','running')) OR
 EXISTS(SELECT 1 FROM agent_tasks WHERE session_id=? AND status='started')) AS background`, id, id, id, id, id, id, id, id).Scan(&state).Error
	session.LatestReplySequence, session.LatestRunStatus = state.LatestReplySequence, state.LatestRunStatus
	session.PendingInputCount, session.Executing = state.PendingInputCount, state.Executing
	session.Running = state.Executing || state.Background
	return SyncCheckpoint{ThroughSequence: state.ThroughSequence, RevertedSequence: state.RevertedSequence, Session: session}, err
}

func (s *Store) readView(ctx context.Context, fn func(*Store) error) error {
	return s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		view := &Store{db: s.db, orm: tx, readTx: tx.Statement.ConnPool}
		return fn(view)
	})
}

func (s *Store) Checkpoint(ctx context.Context, id string) (SyncCheckpoint, error) {
	var point SyncCheckpoint
	err := s.readView(ctx, func(view *Store) (err error) {
		point, err = view.checkpoint(ctx, id)
		return err
	})
	return point, err
}

// ConversationSnapshot keeps the core view and its replay boundary in the same
// SQLite read transaction. Auxiliary UI reads cannot block its establishment.
func (s *Store) ConversationSnapshot(ctx context.Context, id string) (ConversationSnapshot, error) {
	var snapshot ConversationSnapshot
	err := s.readView(ctx, func(view *Store) (err error) {
		if snapshot.SyncCheckpoint, err = view.checkpoint(ctx, id); err != nil {
			return err
		}
		if snapshot.Messages, err = view.MessagesBefore(ctx, id, 0, 100); err != nil {
			return err
		}
		if snapshot.Events, err = view.EventsBefore(ctx, id, 0, 100); err != nil {
			return err
		}
		snapshot.HasOlder, snapshot.HasOlderEvents = len(snapshot.Messages) == 100, len(snapshot.Events) == 100
		if len(snapshot.Events) > 0 {
			snapshot.EventsBefore = snapshot.Events[0].Sequence
		}
		var runIDs, messageIDs []string
		seen := map[string]bool{}
		var activeRun string
		for _, message := range snapshot.Messages {
			if message.Role == "user" {
				activeRun = message.RunID
			}
			if message.Role != "assistant" {
				continue
			}
			messageIDs = append(messageIDs, message.ID)
			if !seen[message.RunID] && message.RunID != "" {
				runIDs = append(runIDs, message.RunID)
				seen[message.RunID] = true
			}
		}
		if activeRun == "" && len(snapshot.Messages) > 0 {
			activeRun = snapshot.Messages[len(snapshot.Messages)-1].RunID
		}
		if snapshot.Boundaries, err = view.MessageResultBoundaries(ctx, id, messageIDs); err != nil {
			return err
		}
		if snapshot.Results, err = view.ModelResultEvents(ctx, id, runIDs); err != nil {
			return err
		}
		if snapshot.Efforts, err = view.RunReasoningEfforts(ctx, runIDs); err != nil {
			return err
		}
		// Child traffic can push the latest root progress off the recent page.
		// Keep its model identity and latest progress without a full run overview.
		if activeRun != "" {
			var root []struct {
				Sequence  int64
				Kind      string
				Data      json.RawMessage
				CreatedAt string
			}
			if err = view.orm.Raw(`SELECT sequence,kind,CAST(data AS BLOB) AS data,created_at FROM events WHERE sequence IN (
 SELECT MAX(sequence) FROM events WHERE session_id=? AND json_extract(data,'$.runId')=?
 AND COALESCE(json_extract(data,'$.depth'),0)=0 AND COALESCE(json_extract(data,'$.taskId'),'main') IN ('','main')
 AND COALESCE(json_extract(data,'$.purpose'),'')=''
 AND kind IN ('model_request','model_delta','model_reasoning_delta','model_result','tool','run_finished')
 GROUP BY kind='model_request') AND session_id=? ORDER BY sequence`, id, activeRun, id).Scan(&root).Error; err != nil {
				return err
			}
			var prefix []Event
			for _, r := range root {
				if len(snapshot.Events) == 0 || r.Sequence < snapshot.Events[0].Sequence {
					prefix = append(prefix, Event{SessionID: id, Sequence: r.Sequence, Kind: r.Kind, Data: r.Data, CreatedAt: r.CreatedAt})
				}
			}
			snapshot.Events = append(prefix, snapshot.Events...)
		}
		return nil
	})
	return snapshot, err
}

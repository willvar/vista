package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// AgentSession is a metadata-only view; pending prompts and message previews
// must not be added to it. The same activity predicates guard closing.
type AgentSession struct {
	ID                  string `json:"id"`
	Title               string `json:"title"`
	Open                bool   `json:"open"`
	Pinned              bool   `json:"pinned"`
	Unread              bool   `json:"unread"`
	CreatedAt           string `json:"createdAt"`
	UpdatedAt           string `json:"updatedAt"`
	Running             bool   `json:"running"`
	PendingInputs       bool   `json:"pendingInputs"`
	AwaitingReply       bool   `json:"awaitingReply"`
	PendingContinuation bool   `json:"pendingContinuation"`
}

type SessionActionState struct {
	AgentSession `gorm:"embedded"`
	Sequence     int64
	OpenRevision int64
}

const agentSessionColumns = `s.id, s.title, s.is_open AS open, s.pinned, s.created_at,
 EXISTS(SELECT 1 FROM history_messages WHERE session_id=s.id AND role='assistant' AND sequence>s.read_sequence) AS unread,
 COALESCE((SELECT created_at FROM history_events WHERE session_id=s.id ORDER BY sequence DESC LIMIT 1),s.created_at) AS updated_at,
 (EXISTS(SELECT 1 FROM runs WHERE session_id=s.id AND status='running') OR
  EXISTS(SELECT 1 FROM jobs WHERE session_id=s.id AND status IN ('starting','running')) OR
  EXISTS(SELECT 1 FROM agent_tasks WHERE session_id=s.id AND status='started')) AS running,
 EXISTS(SELECT 1 FROM queued_inputs WHERE session_id=s.id AND status IN ('queued','paused')) AS pending_inputs,
 (s.pending_kind<>'') AS awaiting_reply,
 EXISTS(SELECT 1 FROM runtime_events WHERE session_id=s.id AND status IN ('pending','restart')) AS pending_continuation`

func (s *Store) AgentSessions(ctx context.Context, source, title string, open *bool, updatedBefore time.Time, idleOnly bool, offset, limit int) ([]AgentSession, error) {
	query := s.orm.WithContext(ctx).Table("sessions AS s").Select(agentSessionColumns).Where("NOT EXISTS(SELECT 1 FROM conversation_branches WHERE branch_id=s.id)")
	if title != "" {
		query = query.Where("vista_contains(s.title, ?)", title)
	}
	if open != nil {
		query = query.Where("s.is_open=?", *open)
	}
	if idleOnly {
		query = query.Where("s.id<>? AND s.is_open=1 AND s.pinned=0", source)
	}
	query = s.orm.WithContext(ctx).Table("(?) AS candidates", query)
	if idleOnly {
		query = query.Where("NOT (unread OR running OR pending_inputs OR awaiting_reply OR pending_continuation)")
	}
	if !updatedBefore.IsZero() {
		query = query.Where("julianday(updated_at)<julianday(?)", updatedBefore.UTC().Format(time.RFC3339Nano))
	}
	items := []AgentSession{}
	err := query.Order("julianday(updated_at) DESC, id DESC").Offset(offset).Limit(limit).Find(&items).Error
	return items, err
}

func (s *Store) SessionActionState(ctx context.Context, id string) (SessionActionState, error) {
	var state SessionActionState
	err := s.orm.WithContext(ctx).Table("sessions AS s").Select(agentSessionColumns+`,
 COALESCE((SELECT MAX(sequence) FROM events WHERE session_id=s.id),0) AS sequence, s.open_revision`).Where("s.id=?", id).Take(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrNotFound
	}
	return state, err
}

func (s *Store) AgentRunActive(ctx context.Context, id, runID string) (bool, error) {
	var active bool
	err := s.orm.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM runs r JOIN sessions s ON s.id=r.session_id
 WHERE r.session_id=? AND r.id=? AND r.status='running' AND s.is_open=1
 AND NOT EXISTS(SELECT 1 FROM events e WHERE e.session_id=r.session_id AND e.kind='run_cancel_requested'
 AND json_extract(e.data,'$.runId')=r.id))`, id, runID).Row().Scan(&active)
	return active, err
}

// ActionBlocker does not treat the idle resident Worker as active work. Closed
// sessions can retain old inputs and questions: deletion removes that history.
func (state SessionActionState) ActionBlocker(kind string) string {
	if kind == "delete" && state.Open {
		return "must_be_closed"
	}
	if kind == "close" && !state.Open {
		return "already_closed"
	}
	if state.Running {
		return "active_work"
	}
	if kind == "close" {
		switch {
		case state.PendingInputs:
			return "pending_inputs"
		case state.AwaitingReply:
			return "awaiting_reply"
		case state.PendingContinuation:
			return "pending_continuation"
		}
	}
	return ""
}

// ApplyAgentSessionAction rechecks the source and exact preview inside the same
// write transaction as the mutation. The caller serializes session/Worker
// lifecycle changes and cleans up the stopped Worker or deleted output files.
func (s *Store) ApplyAgentSessionAction(ctx context.Context, source, runID, kind string, preview SessionActionState) (reason string, paths []string, err error) {
	if preview.ID == source || (kind != "close" && kind != "delete") {
		return "invalid_target", nil, nil
	}
	err = s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		view := &Store{orm: tx, library: s.library}
		active, err := view.AgentRunActive(ctx, source, runID)
		if err != nil {
			return err
		}
		if !active {
			reason = "source_run_ended"
			return nil
		}
		state, err := view.SessionActionState(ctx, preview.ID)
		if errors.Is(err, ErrNotFound) {
			reason = "not_found"
			return nil
		}
		if err != nil {
			return err
		}
		if reason = state.ActionBlocker(kind); reason != "" {
			return nil
		}
		if state.Sequence != preview.Sequence || state.OpenRevision != preview.OpenRevision || state.Title != preview.Title {
			reason = "changed_since_preview"
			return nil
		}
		if kind == "close" {
			return setSessionOpen(tx, preview.ID, false)
		}
		paths, err = view.ArtifactPaths(ctx, preview.ID)
		if err != nil {
			return err
		}
		return deleteSessionRows(tx, preview.ID)
	})
	return
}

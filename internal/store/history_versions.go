package store

import (
	"context"
	"errors"
	"math"

	"vista/internal/resources"

	"gorm.io/gorm"
)

var ErrHistoryChanged = errors.New("the conversation history or branching preference changed")
var ErrHistoryBusy = errors.New("finish pending work before replacing conversation history")

// HistoryChange carries the behavior the client actually presented. An old
// client can use the original fork operation only while branching is enabled.
// A same-session replacement must also match the displayed durable cursor.
type HistoryChange struct {
	Branching       *bool  `json:"branching"`
	ThroughSequence *int64 `json:"throughSequence"`
}

// HistoryDraft captures the proposed operation and attachments in one read view.
func (s *Store) HistoryDraft(ctx context.Context, sessionID, messageID string) (Message, []resources.Ref, HistoryChange, error) {
	var message Message
	var images []resources.Ref
	var change HistoryChange
	err := s.readView(ctx, func(view *Store) error {
		var err error
		message, err = view.RevertTarget(ctx, sessionID, messageID)
		if err != nil {
			return err
		}
		images, err = view.MessageResources(ctx, sessionID, messageID)
		if err != nil {
			return err
		}
		mode, err := view.Branching(ctx)
		if err != nil {
			return err
		}
		var through int64
		if err := view.orm.WithContext(ctx).Raw(`SELECT COALESCE(MAX(sequence),0) FROM events WHERE session_id=?`, sessionID).Row().Scan(&through); err != nil {
			return err
		}
		change = HistoryChange{Branching: &mode, ThroughSequence: &through}
		return nil
	})
	return message, images, change, err
}

func (change HistoryChange) WantsBranch() bool { return change.Branching == nil || *change.Branching }

func checkBranching(ctx context.Context, tx *gorm.DB, change HistoryChange) error {
	enabled, err := branching(ctx, tx)
	if err != nil {
		return err
	}
	if enabled != change.WantsBranch() {
		return ErrHistoryChanged
	}
	return nil
}

// Only the active read view changes. Nothing is copied out of its canonical
// stream, and existing branches always keep their own frozen intervals.
const activeHistoryRanges = `SELECT source_id,from_sequence,before_sequence FROM frozen_history_ranges WHERE branch_id=?
 UNION ALL SELECT ?,COALESCE((SELECT from_sequence FROM live_history_starts WHERE session_id=?),1),` + openHistoryEnd

func freezeForkRanges(ctx context.Context, tx *gorm.DB, source, child string, before int64) error {
	var hasRanges bool
	if err := tx.WithContext(ctx).Raw("SELECT EXISTS(SELECT 1 FROM live_history_starts WHERE session_id=?)", source).Row().Scan(&hasRanges); err != nil {
		return err
	}
	if !hasRanges {
		_, err := execSQL(ctx, tx, `INSERT INTO branch_history_spans(branch_id,source_id,before_sequence)
 SELECT ?,source_id,MIN(before_sequence,?) FROM branch_history_spans WHERE branch_id=? AND ?>1
 UNION ALL SELECT ?,?,? WHERE EXISTS(SELECT 1 FROM events WHERE session_id=? AND sequence<?)`, child, before, source, before, child, source, before, source, before)
		return err
	}
	_, err := execSQL(ctx, tx, `INSERT INTO conversation_history_ranges(session_id,version,source_id,from_sequence,before_sequence)
 SELECT ?,0,source_id,from_sequence,MIN(before_sequence,?) FROM (`+activeHistoryRanges+`) WHERE from_sequence<?
 UNION ALL SELECT ?,0,?,?,`+openHistoryEnd, child, before, source, source, source, before, child, child, before)
	return err
}

func checkHistoryIdle(ctx context.Context, tx *gorm.DB, sessionID string, expected *int64) error {
	var open, busy bool
	var through int64
	err := tx.WithContext(ctx).Raw(`SELECT s.is_open,
 COALESCE((SELECT MAX(sequence) FROM events WHERE session_id=s.id),0),
 s.pending_kind<>'' OR EXISTS(SELECT 1 FROM runs WHERE session_id=s.id AND status='running')
 OR EXISTS(SELECT 1 FROM queued_inputs WHERE session_id=s.id AND status IN ('queued','paused'))
 OR EXISTS(SELECT 1 FROM agent_tasks WHERE session_id=s.id AND status='started')
 OR EXISTS(SELECT 1 FROM jobs WHERE session_id=s.id AND status IN ('starting','running'))
 OR EXISTS(SELECT 1 FROM artifacts WHERE session_id=s.id AND status='started')
 OR EXISTS(SELECT 1 FROM runtime_events WHERE session_id=s.id AND status='pending')
 OR EXISTS(SELECT 1 FROM schedule_runs WHERE session_id=s.id AND (state IN ('queued','running','waiting_reply') OR close_pending=1))
 FROM sessions s WHERE s.id=?`, sessionID).Row().Scan(&open, &through, &busy)
	if err != nil {
		return err
	}
	if !open {
		return ErrClosed
	}
	if expected == nil || *expected != through {
		return ErrHistoryChanged
	}
	if busy {
		return ErrHistoryBusy
	}
	return nil
}

func saveHistoryVersion(ctx context.Context, tx *gorm.DB, sessionID string, marker Event, reason string) error {
	if _, err := execSQL(ctx, tx, `INSERT INTO conversation_history_versions(session_id,version,created_at,reason) VALUES(?,?,?,?)`, sessionID, marker.Sequence, marker.CreatedAt, reason); err != nil {
		return err
	}
	_, err := execSQL(ctx, tx, `INSERT INTO conversation_history_ranges(session_id,version,source_id,from_sequence,before_sequence)
 SELECT ?,?,source_id,from_sequence,MIN(before_sequence,?) FROM (`+activeHistoryRanges+`) WHERE from_sequence<?`,
		sessionID, marker.Sequence, marker.Sequence, sessionID, sessionID, sessionID, marker.Sequence)
	return err
}

func selectHistoryVersion(ctx context.Context, tx *gorm.DB, sessionID string, version, before, newStart int64) error {
	if _, err := execSQL(ctx, tx, `DELETE FROM conversation_history_ranges WHERE session_id=? AND version=0`, sessionID); err != nil {
		return err
	}
	if _, err := execSQL(ctx, tx, `INSERT INTO conversation_history_ranges(session_id,version,source_id,from_sequence,before_sequence)
 SELECT session_id,0,source_id,from_sequence,MIN(before_sequence,?) FROM conversation_history_ranges
 WHERE session_id=? AND version=? AND from_sequence<?
 UNION ALL SELECT ?,0,?,?,`+openHistoryEnd, before, sessionID, version, before, sessionID, sessionID, newStart); err != nil {
		return err
	}
	_, err := execSQL(ctx, tx, `UPDATE sessions SET read_sequence=MIN(read_sequence,
 COALESCE((SELECT MAX(sequence) FROM history_messages WHERE session_id=? AND role='assistant'),0)) WHERE id=?`, sessionID, sessionID)
	return err
}

func replaceHistory(ctx context.Context, tx *gorm.DB, sessionID, messageID, reason string, change HistoryChange) (Event, error) {
	if change.WantsBranch() {
		return Event{}, ErrHistoryChanged
	}
	if err := checkBranching(ctx, tx, change); err != nil {
		return Event{}, err
	}
	if err := checkHistoryIdle(ctx, tx, sessionID, change.ThroughSequence); err != nil {
		return Event{}, err
	}
	var before int64
	var runID string
	if err := tx.WithContext(ctx).Raw("SELECT sequence,run_id FROM history_messages WHERE session_id=? AND id=? AND role='user'", sessionID, messageID).Row().Scan(&before, &runID); err != nil {
		return Event{}, err
	}
	marker, err := appendEvent(ctx, tx, sessionID, "conversation_reverted", map[string]any{"messageId": messageID, "beforeSequence": before})
	if err != nil {
		return Event{}, err
	}
	if err := saveHistoryVersion(ctx, tx, sessionID, marker, reason); err != nil {
		return Event{}, err
	}
	if err := selectHistoryVersion(ctx, tx, sessionID, marker.Sequence, before, marker.Sequence); err != nil {
		return Event{}, err
	}
	// An interjection can cut through an earlier execution in the same run.
	// Record its terminal projection, not changes to its canonical run row.
	if runID != "" {
		var partial bool
		if err := tx.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM history_runs WHERE session_id=? AND id=? AND status='interrupted')`, sessionID, runID).Row().Scan(&partial); err != nil {
			return Event{}, err
		}
		if partial {
			if _, err := appendUnknownTools(ctx, tx, sessionID, runID); err != nil {
				return Event{}, err
			}
			if _, err := appendEvent(ctx, tx, sessionID, "run_finished", map[string]any{"runId": runID, "status": "interrupted", "reason": "History replaced before the next user input"}); err != nil {
				return Event{}, err
			}
		}
	}
	return marker, nil
}

func (s *Store) WithdrawInPlace(ctx context.Context, sessionID, messageID string, change HistoryChange) (Event, error) {
	tx, err := s.begin(ctx, nil)
	if err != nil {
		return Event{}, err
	}
	defer tx.Rollback()
	event, err := replaceHistory(ctx, tx, sessionID, messageID, "withdraw", change)
	if err != nil {
		return Event{}, err
	}
	return event, tx.Commit().Error
}

func (s *Store) RestoreHistoryVersion(ctx context.Context, sessionID string, version int64, expected *int64) (Event, error) {
	tx, err := s.begin(ctx, nil)
	if err != nil {
		return Event{}, err
	}
	defer tx.Rollback()
	if err := checkHistoryIdle(ctx, tx, sessionID, expected); err != nil {
		return Event{}, err
	}
	var exists bool
	if err := tx.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM conversation_history_versions WHERE session_id=? AND version=?)`, sessionID, version).Row().Scan(&exists); err != nil {
		return Event{}, err
	}
	if !exists {
		return Event{}, ErrNotFound
	}
	marker, err := appendEvent(ctx, tx, sessionID, "conversation_reverted", map[string]any{"restoredVersion": version})
	if err != nil {
		return Event{}, err
	}
	if err := saveHistoryVersion(ctx, tx, sessionID, marker, "restore"); err != nil {
		return Event{}, err
	}
	if err := selectHistoryVersion(ctx, tx, sessionID, version, math.MaxInt64, marker.Sequence); err != nil {
		return Event{}, err
	}
	return marker, tx.Commit().Error
}

type HistoryVersion struct {
	Version   int64  `json:"version"`
	CreatedAt string `json:"createdAt"`
	Reason    string `json:"reason"`
}

type HistoryVersionList struct {
	Items      []HistoryVersion `json:"items"`
	NextBefore int64            `json:"nextBefore,omitempty"`
}

func (s *Store) HistoryVersions(ctx context.Context, sessionID string, before int64) (HistoryVersionList, error) {
	page := HistoryVersionList{Items: []HistoryVersion{}}
	const pageSize = 20
	err := s.orm.WithContext(ctx).Raw(`SELECT version,created_at,reason FROM conversation_history_versions
 WHERE session_id=? AND (?=0 OR version<?) ORDER BY version DESC LIMIT ?`, sessionID, before, before, pageSize+1).Scan(&page.Items).Error
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		page.NextBefore = page.Items[pageSize-1].Version
	}
	return page, err
}

type HistoryVersionPreview struct {
	ThroughSequence int64 `json:"throughSequence"`
	Messages        []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

func (s *Store) PreviewHistoryVersion(ctx context.Context, sessionID string, version int64) (HistoryVersionPreview, error) {
	var out HistoryVersionPreview
	err := s.readView(ctx, func(view *Store) error {
		if err := view.orm.WithContext(ctx).Raw(`SELECT COALESCE((SELECT MAX(sequence) FROM events WHERE session_id=v.session_id),0)
 FROM conversation_history_versions v WHERE v.session_id=? AND v.version=?`, sessionID, version).Row().Scan(&out.ThroughSequence); err != nil {
			return err
		}
		return view.orm.WithContext(ctx).Raw(`SELECT role,content FROM (
 SELECT m.role,substr(m.content,1,1000) AS content,m.sequence FROM conversation_history_ranges r
 JOIN messages m ON m.session_id=r.source_id AND m.sequence>=r.from_sequence AND m.sequence<r.before_sequence
 WHERE r.session_id=? AND r.version=? ORDER BY m.sequence DESC LIMIT 8
 ) ORDER BY sequence`, sessionID, version).Scan(&out.Messages).Error
	})
	return out, err
}

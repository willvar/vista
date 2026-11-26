package store

import (
	"context"

	"gorm.io/gorm"
)

// SessionBranchGroup is a list projection, not the state of the root Worker.
// Each badge names one open branch. It never exposes other branches' histories
// or compares sequence numbers across branches.
type SessionBranchGroup struct {
	EntryID     string `json:"entryId"`
	ExecutingID string `json:"executingId,omitempty"`
	RunningID   string `json:"runningId,omitempty"`
	AwaitingID  string `json:"awaitingId,omitempty"`
	UnreadID    string `json:"unreadId,omitempty"`
	PausedID    string `json:"pausedId,omitempty"`
	FailedID    string `json:"failedId,omitempty"`
}

// PreferBranch stores only an explicit selection in this Profile's store.
// Reading or falling back to another branch never writes this preference.
func (s *Store) PreferBranch(ctx context.Context, id string) (changed bool, err error) {
	err = s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root string
		var open bool
		if err := tx.Raw(`SELECT COALESCE(b.root_id,s.id),s.is_open FROM sessions s
 LEFT JOIN conversation_branches b ON b.branch_id=s.id WHERE s.id=?`, id).Row().Scan(&root, &open); err != nil {
			return err
		}
		if !open {
			return ErrClosed
		}
		result, err := execSQL(ctx, tx, `INSERT INTO conversation_branch_preferences(root_id,branch_id) VALUES(?,?)
 ON CONFLICT(root_id) DO UPDATE SET branch_id=excluded.branch_id
 WHERE conversation_branch_preferences.branch_id<>excluded.branch_id`, root, id)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		changed = rows != 0
		return err
	})
	return changed, err
}

// Match the same open-group rule as SessionCounts before applying limits or
// pagination. A closed root can still represent an open child.
func sessionGroupQuery(db *gorm.DB, open []bool) *gorm.DB {
	query := db.Table("sessions").Where("NOT EXISTS(SELECT 1 FROM conversation_branches WHERE branch_id=sessions.id)")
	if len(open) > 0 {
		query = query.Where(`(sessions.is_open=1 OR EXISTS(
 SELECT 1 FROM conversation_branches b JOIN sessions child ON child.id=b.branch_id
 WHERE b.root_id=sessions.id AND child.is_open=1)) = ?`, open[0])
	}
	return query
}

func readSessionGroups(db, query *gorm.DB, limit, offset int, search SessionSearch) ([]Session, error) {
	items, err := readSessions(db, query, limit, offset, search)
	if err != nil || len(items) == 0 {
		return items, err
	}
	ids := make([]string, 0, len(items))
	roots := make(map[string]*Session, len(items))
	for i := range items {
		ids = append(ids, items[i].ID)
		roots[items[i].ID] = &items[i]
	}
	var branches []struct {
		RootID, ID, PreferredID                              string
		Executing, Running, Awaiting, Unread, Paused, Failed bool
	}
	// Collect only open children of the returned page. `messages` and `runs`
	// are the owned records; inherited replies must not create unread badges.
	if err := db.Raw(`SELECT b.root_id,s.id,COALESCE(pref.branch_id,'') AS preferred_id,
 EXISTS(SELECT 1 FROM runs r WHERE r.session_id=s.id AND r.status='running') AS executing,
 (EXISTS(SELECT 1 FROM jobs j WHERE j.session_id=s.id AND j.status IN ('starting','running'))
 OR EXISTS(SELECT 1 FROM agent_tasks t WHERE t.session_id=s.id AND t.status='started')) AS running,
 s.pending_kind<>'' AS awaiting,
 COALESCE((SELECT sequence FROM messages m WHERE m.session_id=s.id AND m.role='assistant'
   AND (NOT EXISTS(SELECT 1 FROM live_history_starts l WHERE l.session_id=s.id)
     OR EXISTS(SELECT 1 FROM history_messages h WHERE h.session_id=s.id AND h.id=m.id))
   ORDER BY sequence DESC LIMIT 1),0)>s.read_sequence AS unread,
 (s.input_queue_paused=1 AND EXISTS(SELECT 1 FROM queued_inputs q WHERE q.session_id=s.id AND q.status IN ('queued','paused'))) AS paused,
 COALESCE((SELECT h.status FROM history_runs h JOIN runs r ON h.id=r.id
   WHERE h.session_id=s.id AND r.session_id=s.id ORDER BY h.created_at DESC,h.id DESC LIMIT 1),'')='failed' AS failed
 FROM conversation_branches b JOIN sessions s ON s.id=b.branch_id AND s.is_open=1
 LEFT JOIN conversation_branch_preferences pref ON pref.root_id=b.root_id
 WHERE b.root_id IN ? ORDER BY b.root_id,julianday(s.created_at) DESC,s.id DESC`, ids).Scan(&branches).Error; err != nil {
		return nil, err
	}
	for _, branch := range branches {
		root := roots[branch.RootID]
		if root.BranchGroup == nil {
			group := &SessionBranchGroup{EntryID: branch.ID}
			if root.Open {
				group.EntryID = root.ID
				group.add(root.ID, root.Executing, root.Running, root.Pending != nil, root.LatestReplySequence > root.ReadSequence,
					root.InputQueuePaused && root.PendingInputCount > 0, root.LatestRunStatus == "failed")
			}
			root.BranchGroup = group
		}
		if branch.ID == branch.PreferredID {
			root.BranchGroup.EntryID = branch.ID
		}
		root.BranchGroup.add(branch.ID, branch.Executing, branch.Running, branch.Awaiting, branch.Unread, branch.Paused, branch.Failed)
	}
	return items, nil
}

func (group *SessionBranchGroup) add(id string, executing, running, awaiting, unread, paused, failed bool) {
	if executing {
		group.ExecutingID = id
	}
	if (executing || running) && group.RunningID == "" {
		group.RunningID = id
	}
	if awaiting && group.AwaitingID == "" {
		group.AwaitingID = id
	}
	if unread && group.UnreadID == "" {
		group.UnreadID = id
	}
	if paused && group.PausedID == "" {
		group.PausedID = id
	}
	if failed && group.FailedID == "" {
		group.FailedID = id
	}
}

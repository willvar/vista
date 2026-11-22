package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"vista/internal/resources"
	"vista/internal/workerproto"
)

type Branch struct {
	ID                   string `json:"id"`
	RootID               string `json:"rootId"`
	ParentID             string `json:"parentId,omitempty"`
	ForkMessageID        string `json:"forkMessageId,omitempty"`
	ForkBefore           int64  `json:"forkBefore,omitempty"`
	FirstMessageID       string `json:"firstMessageId,omitempty"`
	FirstMessageSequence int64  `json:"firstMessageSequence,omitempty"`
	CreatedAt            string `json:"createdAt"`
	Open                 bool   `json:"open"`
}

func branchRoot(ctx context.Context, tx *gorm.DB, id string) (string, error) {
	var root string
	err := tx.WithContext(ctx).Raw(`SELECT COALESCE((SELECT root_id FROM conversation_branches WHERE branch_id=s.id),s.id) FROM sessions s WHERE id=?`, id).Row().Scan(&root)
	return root, err
}

func (s *Store) BranchOutputReferenced(ctx context.Context, path string) (bool, error) {
	var used bool
	err := s.orm.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM jobs WHERE output_path=?) OR EXISTS(SELECT 1 FROM artifacts WHERE output_path=?)`, path, path).Row().Scan(&used)
	return used, err
}

func (s *Store) BranchRoot(ctx context.Context, id string) (string, error) {
	return branchRoot(ctx, s.orm, id)
}

func (s *Store) Branches(ctx context.Context, id string) ([]Branch, error) {
	out := []Branch{}
	err := s.readView(ctx, func(view *Store) error {
		root, err := branchRoot(ctx, view.orm, id)
		if err != nil {
			return err
		}
		return view.orm.WithContext(ctx).Raw(`SELECT s.id,? AS root_id,COALESCE(b.parent_id,'') AS parent_id,COALESCE(b.fork_message_id,'') AS fork_message_id,COALESCE(b.fork_before,0) AS fork_before,s.created_at,s.is_open AS open,
 COALESCE(m.id,'') AS first_message_id,COALESCE(m.sequence,0) AS first_message_sequence
 FROM sessions s LEFT JOIN conversation_branches b ON b.branch_id=s.id
 LEFT JOIN messages m ON m.id=(SELECT first.id FROM messages first
 WHERE first.session_id=b.branch_id AND first.role='user' ORDER BY first.sequence LIMIT 1)
 WHERE s.id=? OR b.root_id=? ORDER BY s.id<>?,s.created_at,s.id`, root, root, root, root).Scan(&out).Error
	})
	return out, err
}

// A branch borrows bounded slices from canonical streams. There is no copied
// message, event body, run, job, artifact or output chunk. The new stream only
// owns its own writes and the states needed to close inherited work at the cut.
func (s *Store) Fork(ctx context.Context, source, child, messageID string, retain ...resources.Ref) (Branch, error) {
	branch := Branch{ID: child, ParentID: source, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), ForkMessageID: messageID}
	if _, err := uuid.Parse(child); err != nil {
		return branch, err
	}
	tx, err := s.begin(ctx, nil)
	if err != nil {
		return branch, err
	}
	defer tx.Rollback()
	branch.RootID, err = branchRoot(ctx, tx, source)
	if err != nil {
		return branch, err
	}
	var affectedRun string
	err = tx.WithContext(ctx).Raw(`SELECT m.sequence,m.run_id,s.is_open FROM history_messages m JOIN sessions s ON s.id=m.session_id
 WHERE m.session_id=? AND m.id=? AND m.role='user'`, source, messageID).Row().Scan(&branch.ForkBefore, &affectedRun, &branch.Open)
	if err != nil {
		return branch, err
	}
	for _, r := range retain {
		var owned bool
		if err = tx.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM session_resources sr JOIN resources r ON r.hash=sr.hash WHERE sr.session_id=? AND r.hash=? AND r.bytes=? AND r.media_type=?)`, source, r.Hash, r.Bytes, r.MediaType).Row().Scan(&owned); err != nil {
			return branch, err
		}
		if !owned {
			return branch, ErrNotFound
		}
	}
	if _, err = execSQL(ctx, tx, `INSERT INTO sessions(id,title,is_open,model_key,manual_model_key,manual_reasoning_effort,processing_tier,created_at)
 SELECT ?,root.title,s.is_open,s.model_key,s.manual_model_key,s.manual_reasoning_effort,s.processing_tier,? FROM sessions s,sessions root WHERE s.id=? AND root.id=?`, child, branch.CreatedAt, source, branch.RootID); err != nil {
		return branch, err
	}
	if _, err = execSQL(ctx, tx, `INSERT INTO conversation_branches(branch_id,root_id,parent_id,fork_message_id,fork_before) VALUES(?,?,?,?,?)`, child, branch.RootID, source, messageID, branch.ForkBefore); err != nil {
		return branch, err
	}
	// Flatten only the ancestry ranges. A nested fork at an inherited message
	// may shorten an ancestor and omit the immediate parent's entire own tail.
	cut := branch.ForkBefore
	if _, err = execSQL(ctx, tx, `INSERT INTO branch_history_spans(branch_id,source_id,before_sequence)
 SELECT ?,source_id, MIN(before_sequence,?) FROM branch_history_spans WHERE branch_id=? AND ?>1
 UNION ALL SELECT ?,?,? WHERE EXISTS(SELECT 1 FROM events WHERE session_id=? AND sequence<?)`, child, cut, source, cut, child, source, cut, source, cut); err != nil {
		return branch, err
	}
	// Start the local SSE cursor at the fork, even with an empty prefix or
	// filtered history at the boundary. Checkpoints can seek only the owned
	// event index instead of scanning all inherited events for the head.
	if _, err = appendEvent(ctx, tx, child, "branch_forked", map[string]int64{"beforeSequence": cut}); err != nil {
		return branch, err
	}
	// The owning profile already holds these originals. Transfer only scoped
	// authorization, not message-resource rows or physical resource data.
	if _, err = execSQL(ctx, tx, `INSERT INTO session_resources(session_id,hash,name)
 SELECT ?,sr.hash,sr.name FROM session_resources sr WHERE sr.session_id=? AND (
 EXISTS(SELECT 1 FROM message_resources mr JOIN history_messages m ON m.id=mr.message_id WHERE m.session_id=? AND mr.hash=sr.hash)
 OR EXISTS(SELECT 1 FROM history_events e WHERE e.session_id=? AND e.kind='resource_import' AND json_extract(e.data,'$.resource.hash')=sr.hash))`, child, source, child, child); err != nil {
		return branch, err
	}
	for _, r := range retain {
		if _, err = execSQL(ctx, tx, `INSERT INTO session_resources(session_id,hash,name) VALUES(?,?,?) ON CONFLICT DO NOTHING`, child, r.Hash, r.Name); err != nil {
			return branch, err
		}
	}
	var prior struct{ Data []byte }
	if err = tx.WithContext(ctx).Raw(`SELECT data FROM history_events WHERE session_id=? AND kind='run_finished' AND sequence=(SELECT MAX(sequence) FROM history_messages WHERE session_id=?)`, child, child).Scan(&prior).Error; err != nil {
		return branch, err
	}
	if len(prior.Data) > 0 {
		var terminal struct {
			PendingReply *PendingReply `json:"pendingReply"`
		}
		if err = json.Unmarshal(prior.Data, &terminal); err != nil {
			return branch, err
		}
		if p := terminal.PendingReply; p != nil {
			payload, err := pendingStorageContent(p)
			if err != nil {
				return branch, err
			}
			if _, err = execSQL(ctx, tx, `UPDATE sessions SET pending_kind=?,pending_run_id=?,pending_content=? WHERE id=?`, p.Kind, p.RunID, payload, child); err != nil {
				return branch, err
			}
		}
	}
	// Scheduled work never migrates. Settle inherited task projections with
	// new terminal events instead of rewriting anyone else's saved events.
	var tasks []struct{ Data []byte }
	if err = tx.WithContext(ctx).Raw(`WITH latest AS (
 SELECT data,ROW_NUMBER() OVER(PARTITION BY json_extract(data,'$.runId'),json_extract(data,'$.task.id') ORDER BY sequence DESC) AS n
 FROM history_events WHERE session_id=? AND kind='agent_task'
 ) SELECT data FROM latest WHERE n=1 AND json_extract(data,'$.task.status')='started'`, child).Scan(&tasks).Error; err != nil {
		return branch, err
	}
	for _, item := range tasks {
		var result workerproto.Result
		if err = json.Unmarshal(item.Data, &result); err != nil {
			return branch, err
		}
		result.Task.Status = "interrupted"
		if _, err = appendEvent(ctx, tx, child, "agent_task", result); err != nil {
			return branch, err
		}
	}
	var partial bool
	if err = tx.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM history_runs WHERE session_id=? AND id=? AND status='interrupted')`, child, affectedRun).Row().Scan(&partial); err != nil {
		return branch, err
	}
	if partial {
		if _, err = appendUnknownTools(ctx, tx, child, affectedRun); err != nil {
			return branch, err
		}
		if _, err = appendEvent(ctx, tx, child, "run_finished", map[string]any{"runId": affectedRun, "status": "interrupted", "reason": "Forked before the next user input"}); err != nil {
			return branch, err
		}
	}
	return branch, tx.Commit().Error
}

var ErrBranchHasChildren = errors.New("remove derived branches before deleting their source conversation")

var ErrBranchRunning = errors.New("another branch of this conversation is executing")

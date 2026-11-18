package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentSessionIdleFilterAndClosureUseSameWorkGuards(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, tc := range []struct{ id, update, blocker string }{
		{"idle", "", ""},
		{"source", "", ""},
		{"pinned", `UPDATE sessions SET pinned=1 WHERE id=?`, ""},
		{"unread", "", ""},
		{"running", `UPDATE runs SET status='running' WHERE session_id=?`, "active_work"},
		{"job", `INSERT INTO jobs(id,session_id,run_id,directory,command,output_path,status,created_at) SELECT id,session_id,id,'','','','starting',created_at FROM runs WHERE session_id=?`, "active_work"},
		{"task", `INSERT INTO agent_tasks(session_id,run_id,task_id,parent_task_id,join_policy,status,background,data) SELECT session_id,id,'child','main','detached','started',1,'{}' FROM runs WHERE session_id=?`, "active_work"},
		{"paused", `INSERT INTO queued_inputs(id,session_id,run_id,mode,status,content,created_at) SELECT id,session_id,id,'now','paused','queued user text',created_at FROM runs WHERE session_id=?`, "pending_inputs"},
		{"answer", `UPDATE sessions SET pending_kind='question' WHERE id=?`, "awaiting_reply"},
		{"resume", `INSERT INTO runtime_events(session_id,run_id,event_id,recipient,sequence,status,data) SELECT session_id,id,id,'main',1,'restart','{}' FROM runs WHERE session_id=?`, "pending_continuation"},
	} {
		if _, err := s.Create(ctx, tc.id, tc.id); err != nil {
			t.Fatal(err)
		}
		if tc.id != "idle" && tc.id != "source" && tc.id != "pinned" {
			run := "run-" + tc.id
			if _, _, err := s.BeginRun(ctx, tc.id, run, "user-"+tc.id, "user text"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.FinishRun(ctx, tc.id, run, "result-"+tc.id, "succeeded", "answer text"); err != nil {
				t.Fatal(err)
			}
			if tc.id != "unread" {
				if err := s.orm.Exec(`UPDATE sessions SET read_sequence=(SELECT MAX(sequence) FROM messages WHERE session_id=?) WHERE id=?`, tc.id, tc.id).Error; err != nil {
					t.Fatal(err)
				}
			}
		}
		if tc.update != "" {
			if err := s.orm.Exec(tc.update, tc.id).Error; err != nil {
				t.Fatal(err)
			}
		}
		state, err := s.SessionActionState(ctx, tc.id)
		if err != nil || state.ActionBlocker("close") != tc.blocker {
			t.Fatalf("%s: %+v, blocker %q, error %v", tc.id, state, state.ActionBlocker("close"), err)
		}
	}
	items, err := s.AgentSessions(ctx, "source", "", nil, time.Time{}, true, 0, 10)
	if err != nil || len(items) != 1 || items[0].ID != "idle" {
		t.Fatalf("idle results: %+v %v", items, err)
	}
}

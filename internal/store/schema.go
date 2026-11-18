package store

import (
	"context"
	"fmt"
	"time"
	"vista/internal/resources"

	"gorm.io/gorm"
)

// Fresh databases start at the current structure. Existing databases retain
// their data; the guarded compatibility steps only fill genuinely missing state.
func (s *Store) initializeSchema() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS sessions (
   id TEXT PRIMARY KEY, title TEXT NOT NULL, is_open INTEGER NOT NULL, model_key TEXT NOT NULL DEFAULT '',
   pending_kind TEXT NOT NULL DEFAULT '', pending_content TEXT NOT NULL DEFAULT '', pending_run_id TEXT NOT NULL DEFAULT '',
   pinned INTEGER NOT NULL DEFAULT 0,
   manual_model_key TEXT NOT NULL DEFAULT '', manual_reasoning_effort TEXT NOT NULL DEFAULT '', processing_tier TEXT NOT NULL DEFAULT '',
   input_queue_paused INTEGER NOT NULL DEFAULT 0, read_sequence INTEGER NOT NULL DEFAULT 0,
   open_revision INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL
  )`,
		`CREATE TABLE IF NOT EXISTS runs (
   id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
   provider_id TEXT NOT NULL DEFAULT '', model_id TEXT NOT NULL DEFAULT '', model_key TEXT NOT NULL DEFAULT '',
   status TEXT NOT NULL, created_at TEXT NOT NULL, finished_at TEXT
  )`,
		`CREATE UNIQUE INDEX IF NOT EXISTS one_running_run_per_session ON runs(session_id) WHERE status='running'`,
		`CREATE INDEX IF NOT EXISTS runs_by_session ON runs(session_id,created_at DESC,id DESC)`,
		`CREATE INDEX IF NOT EXISTS runs_by_session_row ON runs(session_id)`,
		`CREATE TABLE IF NOT EXISTS agent_tasks (
   session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
   run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
   task_id TEXT NOT NULL, parent_task_id TEXT NOT NULL, join_policy TEXT NOT NULL,
   status TEXT NOT NULL, background INTEGER NOT NULL, data BLOB NOT NULL,
   PRIMARY KEY(run_id,task_id)
  )`,
		`CREATE INDEX IF NOT EXISTS active_tasks_by_session ON agent_tasks(session_id) WHERE status='started'`,
		`CREATE TABLE IF NOT EXISTS runtime_events (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
 event_id TEXT NOT NULL, recipient TEXT NOT NULL, sequence INTEGER NOT NULL,
 status TEXT NOT NULL, delivered_run_id TEXT NOT NULL DEFAULT '', data BLOB NOT NULL,
 PRIMARY KEY(session_id,event_id)
 )`,
		`CREATE INDEX IF NOT EXISTS pending_runtime_events ON runtime_events(session_id,recipient,sequence) WHERE status='pending'`,
		`CREATE TABLE IF NOT EXISTS messages (
   id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
   provider_id TEXT NOT NULL DEFAULT '', model_id TEXT NOT NULL DEFAULT '', model_key TEXT NOT NULL DEFAULT '',
   run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE, sequence INTEGER NOT NULL,
   role TEXT NOT NULL, content TEXT NOT NULL, created_at TEXT NOT NULL,
   output_turn INTEGER NOT NULL DEFAULT 0, output_bytes INTEGER NOT NULL DEFAULT 0
  )`,
		`CREATE INDEX IF NOT EXISTS messages_by_session ON messages(session_id, sequence)`,
		`CREATE INDEX IF NOT EXISTS messages_by_run_role ON messages(session_id,run_id,role,sequence)`,
		`CREATE TABLE IF NOT EXISTS resources (
   hash TEXT PRIMARY KEY, media_type TEXT NOT NULL, bytes INTEGER NOT NULL, created_at TEXT NOT NULL
  )`,
		`CREATE TABLE IF NOT EXISTS session_resources (
   session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
   hash TEXT NOT NULL REFERENCES resources(hash), name TEXT NOT NULL DEFAULT '', PRIMARY KEY(session_id,hash)
  )`,
		`CREATE INDEX IF NOT EXISTS resource_sessions ON session_resources(hash)`,
		`CREATE TABLE IF NOT EXISTS message_resources (
   message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
   position INTEGER NOT NULL, hash TEXT NOT NULL REFERENCES resources(hash), name TEXT NOT NULL DEFAULT '',
   PRIMARY KEY(message_id,position)
  )`,
		`CREATE INDEX IF NOT EXISTS resource_messages ON message_resources(hash)`,
		`CREATE TABLE IF NOT EXISTS queued_inputs (
   id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
   run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE, model_key TEXT NOT NULL DEFAULT '', reasoning_effort TEXT NOT NULL DEFAULT '',
   debug INTEGER NOT NULL DEFAULT 0,
   mode TEXT NOT NULL, status TEXT NOT NULL, content TEXT NOT NULL, image_count INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL
  )`,
		`CREATE INDEX IF NOT EXISTS queued_inputs_by_session ON queued_inputs(session_id,status,created_at)`,
		`CREATE TABLE IF NOT EXISTS queued_input_resources (
   input_id TEXT NOT NULL REFERENCES queued_inputs(id) ON DELETE CASCADE,
   position INTEGER NOT NULL, hash TEXT NOT NULL REFERENCES resources(hash), name TEXT NOT NULL DEFAULT '',
   PRIMARY KEY(input_id,position)
  )`,
		`CREATE INDEX IF NOT EXISTS resource_queued_inputs ON queued_input_resources(hash)`,
		`CREATE TABLE IF NOT EXISTS events (
   session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
   sequence INTEGER NOT NULL, kind TEXT NOT NULL, data BLOB NOT NULL,
   created_at TEXT NOT NULL, PRIMARY KEY(session_id, sequence)
  )`,
		`CREATE INDEX IF NOT EXISTS events_by_run ON events(session_id,json_extract(data,'$.runId'),sequence)`,
		`CREATE INDEX IF NOT EXISTS events_model_results_by_run ON events(session_id,json_extract(data,'$.runId'),sequence) WHERE kind='model_result'`,
		`DROP INDEX IF EXISTS events_model_results`,
		`CREATE INDEX IF NOT EXISTS events_reverts ON events(session_id,sequence) WHERE kind='conversation_reverted'`,
		`CREATE INDEX IF NOT EXISTS events_run_starts ON events(session_id,json_extract(data,'$.runId')) WHERE kind='run_started'`,
		`CREATE INDEX IF NOT EXISTS events_context_notices ON events(session_id,sequence) WHERE kind='context_notice'`,
		`CREATE INDEX IF NOT EXISTS events_checklists ON events(session_id,json_extract(data,'$.runId'),sequence) WHERE kind='checklist'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS events_model_history ON events(session_id,json_extract(data,'$.runId'),json_extract(data,'$.turn'),json_extract(data,'$.status'),COALESCE(json_extract(data,'$.history.index'),0)) WHERE kind='model_history'`,
		`CREATE TABLE IF NOT EXISTS model_output_chunks (
   session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
   run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
   turn INTEGER NOT NULL, offset INTEGER NOT NULL, length INTEGER NOT NULL,
   event_sequence INTEGER NOT NULL,
   PRIMARY KEY(session_id,run_id,turn,offset)
  )`,
		`CREATE TABLE IF NOT EXISTS jobs (
   id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
   run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
   directory TEXT NOT NULL, command TEXT NOT NULL, output_path TEXT NOT NULL,
   status TEXT NOT NULL, bytes INTEGER NOT NULL DEFAULT 0, exit_code INTEGER NOT NULL DEFAULT -1,
   complete INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '',
   created_at TEXT NOT NULL, finished_at TEXT
  )`,
		`CREATE INDEX IF NOT EXISTS jobs_by_session ON jobs(session_id,created_at)`,
		`CREATE INDEX IF NOT EXISTS active_jobs_by_session ON jobs(session_id) WHERE status IN ('starting','running')`,
		`CREATE TABLE IF NOT EXISTS worker_owners (
   session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
   pid INTEGER NOT NULL, start_time TEXT NOT NULL
  )`,
		`CREATE TABLE IF NOT EXISTS artifacts (
   id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
   run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
   output_path TEXT NOT NULL, status TEXT NOT NULL, bytes INTEGER NOT NULL DEFAULT 0,
   complete INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '',
   created_at TEXT NOT NULL, finished_at TEXT
  )`,
		`CREATE INDEX IF NOT EXISTS artifacts_by_session ON artifacts(session_id,created_at)`,
	} {
		if err := s.orm.Exec(q).Error; err != nil {
			return fmt.Errorf("initialize database: %w", err)
		}
	}
	if err := resources.InitSchema(s.orm); err != nil {
		return err
	}
	if err := s.initModelSelectionSchema(); err != nil {
		return fmt.Errorf("initialize model selection: %w", err)
	}
	if !s.orm.Migrator().HasColumn(&sessionRow{}, "OpenRevision") {
		if err := s.orm.Migrator().AddColumn(&sessionRow{}, "OpenRevision"); err != nil {
			return fmt.Errorf("initialize session lifecycle revision: %w", err)
		}
	}
	return s.initStatistics()
}

// Reconciliation and retention run on every startup, independently of whether
// the database needed any structural change or one-time statistics backfill.
func (s *Store) recoverStartupState(ctx context.Context) error {
	return s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return pruneStatistics(ctx, tx, time.Now().UTC())
	})
}

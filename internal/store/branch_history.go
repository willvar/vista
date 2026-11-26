package store

import "gorm.io/gorm"

// The canonical streams own writes; views select immutable intervals from
// those streams. A normal branch still uses its original prefix spans. A
// rewritten timeline (or a branch derived from one) can reference disjoint
// intervals and always has one open interval for its own future writes.
const openHistoryEnd = "9223372036854775807"

var branchHistorySchema = []string{
	`CREATE TABLE IF NOT EXISTS branch_history_spans (
  branch_id TEXT NOT NULL REFERENCES conversation_branches(branch_id) ON DELETE CASCADE,
  source_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE RESTRICT,
  before_sequence INTEGER NOT NULL CHECK(before_sequence>1),
  CHECK(branch_id<>source_id), PRIMARY KEY(branch_id,source_id)
 ) WITHOUT ROWID`,
	`CREATE INDEX IF NOT EXISTS branch_history_by_source ON branch_history_spans(source_id)`,
	`CREATE TABLE IF NOT EXISTS conversation_history_versions (
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  version INTEGER NOT NULL, created_at TEXT NOT NULL, reason TEXT NOT NULL,
  PRIMARY KEY(session_id,version)
 ) WITHOUT ROWID`,
	`CREATE TABLE IF NOT EXISTS conversation_history_ranges (
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  version INTEGER NOT NULL, source_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  from_sequence INTEGER NOT NULL CHECK(from_sequence>0), before_sequence INTEGER NOT NULL CHECK(before_sequence>from_sequence),
  PRIMARY KEY(session_id,version,source_id,from_sequence)
 ) WITHOUT ROWID`,
	`CREATE INDEX IF NOT EXISTS events_branch_output ON events(session_id,kind,json_extract(data,'$.id'),sequence)
 WHERE kind IN ('job','artifact')`,
	`CREATE INDEX IF NOT EXISTS events_branch_terminal ON events(session_id,json_extract(data,'$.runId'),sequence)
 WHERE kind='run_finished'`,
	`CREATE VIEW IF NOT EXISTS live_history_starts AS
 SELECT session_id,from_sequence FROM conversation_history_ranges WHERE version=0 AND before_sequence=` + openHistoryEnd,
	`CREATE VIEW IF NOT EXISTS frozen_history_ranges AS
 SELECT branch_id,source_id,1 AS from_sequence,before_sequence FROM branch_history_spans b
 WHERE NOT EXISTS(SELECT 1 FROM live_history_starts l WHERE l.session_id=b.branch_id)
 UNION ALL
 SELECT session_id,source_id,from_sequence,before_sequence FROM conversation_history_ranges
 WHERE version=0 AND before_sequence<` + openHistoryEnd,
	`CREATE VIEW IF NOT EXISTS history_messages AS
 SELECT m.session_id,m.id,m.provider_id,m.model_id,m.model_key,m.run_id,m.sequence,m.role,m.content,m.created_at,m.output_turn,m.output_bytes FROM messages m
 WHERE m.sequence>=COALESCE((SELECT l.from_sequence FROM live_history_starts l WHERE l.session_id=m.session_id),1)
 UNION ALL
 SELECT b.branch_id,m.id,m.provider_id,m.model_id,m.model_key,m.run_id,m.sequence,m.role,m.content,m.created_at,m.output_turn,m.output_bytes
 FROM frozen_history_ranges b JOIN messages m ON m.session_id=b.source_id AND m.sequence>=b.from_sequence AND m.sequence<b.before_sequence`,
	`CREATE VIEW IF NOT EXISTS history_terminal_outputs AS
 SELECT DISTINCT b.branch_id AS session_id,e.session_id AS source_id,e.kind,json_extract(e.data,'$.id') AS id
 FROM frozen_history_ranges b JOIN events e ON e.session_id=b.source_id AND e.sequence>=b.from_sequence AND e.sequence<b.before_sequence
 WHERE e.kind IN ('job','artifact') AND json_extract(e.data,'$.status') NOT IN ('starting','running','started')
 AND json_type(e.data,'$.id')='text'`,
	`CREATE VIEW IF NOT EXISTS history_events AS
 SELECT e.session_id,e.sequence,e.kind,e.data,e.created_at FROM events e
 WHERE e.sequence>=COALESCE((SELECT l.from_sequence FROM live_history_starts l WHERE l.session_id=e.session_id),1)
 UNION ALL
 SELECT b.branch_id,e.sequence,e.kind,e.data,e.created_at
 FROM frozen_history_ranges b JOIN events e ON e.session_id=b.source_id AND e.sequence>=b.from_sequence AND e.sequence<b.before_sequence
 WHERE e.kind NOT GLOB 'input_*'
 AND e.kind NOT IN ('conversation_reverted','runtime_event','runtime_delivered','run_cancel_requested','run_restart_pending','session_renamed','session_title','session_selection')
 AND (e.kind NOT IN ('job','job_output','artifact') OR EXISTS(SELECT 1 FROM history_terminal_outputs o
   WHERE o.session_id=b.branch_id AND o.source_id=e.session_id AND o.kind=CASE WHEN e.kind='job_output' THEN 'job' ELSE e.kind END
   AND o.id=json_extract(e.data,'$.id')))
 AND (e.kind<>'context_notice' OR COALESCE(json_extract(e.data,'$.checkpoint.throughSequence'),0)<b.before_sequence)`,
	`CREATE VIEW IF NOT EXISTS history_output_chunks AS
 SELECT c.session_id,c.run_id,c.turn,c.offset,c.length,c.event_sequence FROM model_output_chunks c
 WHERE c.event_sequence>=COALESCE((SELECT l.from_sequence FROM live_history_starts l WHERE l.session_id=c.session_id),1)
 UNION ALL
 SELECT b.branch_id,c.run_id,c.turn,c.offset,c.length,c.event_sequence
 FROM frozen_history_ranges b JOIN model_output_chunks c ON c.session_id=b.source_id AND c.event_sequence>=b.from_sequence AND c.event_sequence<b.before_sequence`,
	`CREATE VIEW IF NOT EXISTS history_runs AS
 SELECT r.session_id,r.id,r.created_at,r.finished_at,r.status,r.rowid AS run_order FROM runs r
 LEFT JOIN live_history_starts l ON l.session_id=r.session_id
 WHERE l.from_sequence IS NULL
   OR EXISTS(SELECT 1 FROM messages m WHERE m.session_id=r.session_id AND m.run_id=r.id AND m.sequence>=l.from_sequence)
   OR EXISTS(SELECT 1 FROM events e WHERE e.session_id=r.session_id AND e.kind='run_started' AND e.sequence>=l.from_sequence AND json_extract(e.data,'$.runId')=+r.id)
 UNION ALL
 SELECT b.branch_id,r.id,r.created_at,
 COALESCE((SELECT e.created_at FROM history_events e WHERE e.session_id=b.branch_id AND e.kind='run_finished'
    AND json_extract(e.data,'$.runId')=+r.id ORDER BY e.sequence DESC LIMIT 1),s.created_at),
 COALESCE((SELECT json_extract(e.data,'$.status') FROM history_events e WHERE e.session_id=b.branch_id AND e.kind='run_finished'
    AND json_extract(e.data,'$.runId')=+r.id ORDER BY e.sequence DESC LIMIT 1),'interrupted'),r.rowid
 FROM frozen_history_ranges b JOIN runs r ON r.session_id=b.source_id JOIN sessions s ON s.id=b.branch_id
 WHERE EXISTS(SELECT 1 FROM messages m WHERE m.session_id=r.session_id AND m.run_id=r.id AND m.sequence>=b.from_sequence AND m.sequence<b.before_sequence)
    OR EXISTS(SELECT 1 FROM events e WHERE e.session_id=r.session_id AND json_extract(e.data,'$.runId')=+r.id AND e.sequence>=b.from_sequence AND e.sequence<b.before_sequence)
 GROUP BY b.branch_id,r.id`,
	`CREATE VIEW IF NOT EXISTS history_jobs AS
 SELECT j.id,j.session_id,j.run_id,j.directory,j.command,j.output_path,j.status,j.bytes,j.exit_code,j.complete,j.error,j.created_at,j.finished_at FROM jobs j
 LEFT JOIN live_history_starts l ON l.session_id=j.session_id
 WHERE l.from_sequence IS NULL OR EXISTS(SELECT 1 FROM events e WHERE e.session_id=j.session_id AND e.kind='job'
   AND json_extract(e.data,'$.id')=+j.id AND e.sequence>=l.from_sequence)
 UNION ALL
 SELECT j.id,o.session_id,j.run_id,j.directory,j.command,j.output_path,j.status,j.bytes,j.exit_code,j.complete,j.error,j.created_at,j.finished_at
 FROM history_terminal_outputs o JOIN jobs j ON j.id=o.id AND j.session_id=o.source_id AND j.status NOT IN ('starting','running') WHERE o.kind='job'`,
	`CREATE VIEW IF NOT EXISTS history_artifacts AS
 SELECT a.id,a.session_id,a.run_id,a.output_path,a.status,a.bytes,a.complete,a.error,a.created_at,a.finished_at FROM artifacts a
 LEFT JOIN live_history_starts l ON l.session_id=a.session_id
 WHERE l.from_sequence IS NULL OR EXISTS(SELECT 1 FROM events e WHERE e.session_id=a.session_id AND e.kind='artifact'
   AND json_extract(e.data,'$.id')=+a.id AND e.sequence>=l.from_sequence)
 UNION ALL
 SELECT a.id,o.session_id,a.run_id,a.output_path,a.status,a.bytes,a.complete,a.error,a.created_at,a.finished_at
 FROM history_terminal_outputs o JOIN artifacts a ON a.id=o.id AND a.session_id=o.source_id AND a.status<>'started' WHERE o.kind='artifact'`,
}

func (s *Store) initBranchHistory() error {
	return s.orm.Transaction(func(tx *gorm.DB) error {
		var current bool
		if err := tx.Raw("SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='view' AND name='live_history_starts')").Scan(&current).Error; err != nil {
			return err
		}
		if !current {
			for _, view := range []string{"history_artifacts", "history_jobs", "history_runs", "history_output_chunks", "history_events", "history_terminal_outputs", "history_messages"} {
				if err := tx.Exec("DROP VIEW IF EXISTS " + view).Error; err != nil {
					return err
				}
			}
		}
		for _, definition := range branchHistorySchema {
			if err := tx.Exec(definition).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

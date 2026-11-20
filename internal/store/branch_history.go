package store

// The canonical tables own writes and records. These views are only for
// conversation readers, not execution admission or Worker result handling.
// Inherited intervals keep canonical message/run IDs and bound all reads by
// the original send position instead of copying their payloads at a fork.
var branchHistorySchema = []string{
	`CREATE TABLE IF NOT EXISTS branch_history_spans (
  branch_id TEXT NOT NULL REFERENCES conversation_branches(branch_id) ON DELETE CASCADE,
  source_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE RESTRICT,
  before_sequence INTEGER NOT NULL CHECK(before_sequence>1),
  CHECK(branch_id<>source_id), PRIMARY KEY(branch_id,source_id)
 ) WITHOUT ROWID`,
	`CREATE INDEX IF NOT EXISTS branch_history_by_source ON branch_history_spans(source_id)`,
	`CREATE INDEX IF NOT EXISTS events_branch_output ON events(session_id,kind,json_extract(data,'$.id'),sequence)
 WHERE kind IN ('job','artifact')`,
	`CREATE INDEX IF NOT EXISTS events_branch_terminal ON events(session_id,json_extract(data,'$.runId'),sequence)
 WHERE kind='run_finished'`,
	`CREATE VIEW IF NOT EXISTS history_messages AS
 SELECT session_id,id,provider_id,model_id,model_key,run_id,sequence,role,content,created_at,output_turn,output_bytes FROM messages
 UNION ALL
 SELECT b.branch_id,m.id,m.provider_id,m.model_id,m.model_key,m.run_id,m.sequence,m.role,m.content,m.created_at,m.output_turn,m.output_bytes
 FROM branch_history_spans b JOIN messages m ON m.session_id=b.source_id AND m.sequence<b.before_sequence`,
	`CREATE VIEW IF NOT EXISTS history_terminal_outputs AS
 SELECT b.branch_id AS session_id,e.session_id AS source_id,e.kind,json_extract(e.data,'$.id') AS id,e.sequence
 FROM branch_history_spans b JOIN events e ON e.session_id=b.source_id AND e.sequence<b.before_sequence
 WHERE e.kind IN ('job','artifact') AND json_extract(e.data,'$.status') NOT IN ('starting','running','started')
 AND json_type(e.data,'$.id')='text'`,
	`CREATE VIEW IF NOT EXISTS history_events AS
 SELECT session_id,sequence,kind,data,created_at FROM events
 UNION ALL
 SELECT b.branch_id,e.sequence,e.kind,e.data,e.created_at
 FROM branch_history_spans b JOIN events e ON e.session_id=b.source_id AND e.sequence<b.before_sequence
 WHERE e.kind NOT GLOB 'input_*'
 AND e.kind NOT IN ('conversation_reverted','runtime_event','runtime_delivered','run_cancel_requested','run_restart_pending','session_renamed','session_title','session_selection')
 AND (e.kind NOT IN ('job','job_output','artifact') OR EXISTS(SELECT 1 FROM history_terminal_outputs o
   WHERE o.session_id=b.branch_id AND o.source_id=e.session_id AND o.kind=CASE WHEN e.kind='job_output' THEN 'job' ELSE e.kind END
   AND o.id=json_extract(e.data,'$.id')))
 AND (e.kind<>'context_notice' OR COALESCE(json_extract(e.data,'$.checkpoint.throughSequence'),0)<b.before_sequence)`,
	`CREATE VIEW IF NOT EXISTS history_output_chunks AS
 SELECT session_id,run_id,turn,offset,length,event_sequence FROM model_output_chunks
 UNION ALL
 SELECT b.branch_id,c.run_id,c.turn,c.offset,c.length,c.event_sequence
 FROM branch_history_spans b JOIN model_output_chunks c ON c.session_id=b.source_id AND c.event_sequence<b.before_sequence`,
	`CREATE VIEW IF NOT EXISTS history_runs AS
 SELECT session_id,id,created_at,finished_at,status,rowid AS run_order FROM runs
 UNION ALL
 SELECT b.branch_id,r.id,r.created_at,
 COALESCE((SELECT e.created_at FROM events e WHERE e.session_id=b.source_id AND e.kind='run_finished'
         AND json_extract(e.data,'$.runId')=+r.id AND e.sequence<b.before_sequence ORDER BY e.sequence DESC LIMIT 1),s.created_at),
 COALESCE((SELECT json_extract(e.data,'$.status') FROM events e WHERE e.session_id=b.source_id AND e.kind='run_finished'
         AND json_extract(e.data,'$.runId')=+r.id AND e.sequence<b.before_sequence ORDER BY e.sequence DESC LIMIT 1),'interrupted'),r.rowid
 FROM branch_history_spans b JOIN runs r ON r.session_id=b.source_id JOIN sessions s ON s.id=b.branch_id
 WHERE EXISTS(SELECT 1 FROM messages m WHERE m.session_id=r.session_id AND m.run_id=r.id AND m.sequence<b.before_sequence)
    OR EXISTS(SELECT 1 FROM events e WHERE e.session_id=r.session_id AND json_extract(e.data,'$.runId')=+r.id AND e.sequence<b.before_sequence)`,
	`CREATE VIEW IF NOT EXISTS history_jobs AS
 SELECT id,session_id,run_id,directory,command,output_path,status,bytes,exit_code,complete,error,created_at,finished_at FROM jobs
 UNION ALL
 SELECT j.id,b.branch_id,j.run_id,j.directory,j.command,j.output_path,j.status,j.bytes,j.exit_code,j.complete,j.error,j.created_at,j.finished_at
 FROM branch_history_spans b JOIN jobs j ON j.session_id=b.source_id AND j.status NOT IN ('starting','running')
 WHERE EXISTS(SELECT 1 FROM history_terminal_outputs o WHERE o.session_id=b.branch_id AND o.source_id=j.session_id AND o.kind='job' AND o.id=j.id)`,
	`CREATE VIEW IF NOT EXISTS history_artifacts AS
 SELECT id,session_id,run_id,output_path,status,bytes,complete,error,created_at,finished_at FROM artifacts
 UNION ALL
 SELECT a.id,b.branch_id,a.run_id,a.output_path,a.status,a.bytes,a.complete,a.error,a.created_at,a.finished_at
 FROM branch_history_spans b JOIN artifacts a ON a.session_id=b.source_id AND a.status<>'started'
 WHERE EXISTS(SELECT 1 FROM history_terminal_outputs o WHERE o.session_id=b.branch_id AND o.source_id=a.session_id AND o.kind='artifact' AND o.id=a.id)`,
}

import { cpSync, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

// This text/tool fixture has no uploaded resources or background jobs. Copy
// only durable history and its managed tool outputs after the producer stops.
export function saveHistoryState(source: string, destination: string) {
  mkdirSync(destination, { recursive: true, mode: 0o700 })
  cpSync(join(source, 'vista.db'), join(destination, 'vista.db'))
  if (existsSync(join(source, 'outputs'))) cpSync(join(source, 'outputs'), join(destination, 'outputs'), { recursive: true })
  const db = new DatabaseSync(join(destination, 'vista.db'))
  try {
    if ((db.prepare("SELECT COUNT(*) AS count FROM runs WHERE status='running'").get()!).count !== 0) throw new Error('History fixture is still executing')
    if ((db.prepare('SELECT COUNT(*) AS count FROM jobs').get()!).count !== 0) throw new Error('History fixture unexpectedly contains background jobs')
    // Server shutdown owns process cleanup. Never restore process identities.
    db.exec('DELETE FROM worker_owners; PRAGMA wal_checkpoint(TRUNCATE)')
  } finally { db.close() }
  writeFileSync(join(destination, 'source.json'), JSON.stringify({ directory: source }))
}

export function restoreHistoryState(source: string, destination: string) {
  mkdirSync(destination, { recursive: true, mode: 0o700 })
  cpSync(join(source, 'vista.db'), join(destination, 'vista.db'))
  if (existsSync(join(source, 'outputs'))) cpSync(join(source, 'outputs'), join(destination, 'outputs'), { recursive: true })
  const original = JSON.parse(readFileSync(join(source, 'source.json'), 'utf8')).directory as string
  const db = new DatabaseSync(join(destination, 'vista.db'))
  try {
    db.exec('BEGIN')
    db.prepare('UPDATE artifacts SET output_path=replace(output_path,?,?)').run(original, destination)
    // Stored JSON can also contain managed output paths. Preserve BLOB storage
    // and JSON escaping; canonical message text and model chunks stay intact.
    for (const table of ['events', 'agent_tasks', 'runtime_events']) {
      db.prepare(`UPDATE ${table} SET data=CAST(replace(CAST(data AS TEXT),?,?) AS BLOB) WHERE instr(CAST(data AS TEXT),?)>0`)
        .run(JSON.stringify(original).slice(1, -1), JSON.stringify(destination).slice(1, -1), JSON.stringify(original).slice(1, -1))
    }
    db.exec('COMMIT; PRAGMA wal_checkpoint(TRUNCATE)')
  } finally { db.close() }
}

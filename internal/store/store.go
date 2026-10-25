package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"vista/internal/question"
	"vista/internal/resources"

	gosqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type Session struct {
	ID                       string        `json:"id"`
	Title                    string        `json:"title"`
	ModelKey                 string        `json:"modelKey"`
	Pending                  *PendingReply `json:"pendingReply,omitempty"`
	Open                     bool          `json:"open"`
	Running                  bool          `json:"running"`
	Executing                bool          `json:"executing"`
	CreatedAt                string        `json:"createdAt"`
	SelectionMode            string        `json:"selectionMode"`
	SelectionTier            string        `json:"selectionTier"`
	ManualModelKey           string        `json:"manualModelKey"`
	ManualReasoningEffort    string        `json:"manualReasoningEffort"`
	SelectionReasoningEffort string        `json:"selectionReasoningEffort"`
	InputQueuePaused         bool          `json:"inputQueuePaused"`
	ReadSequence             int64         `json:"readSequence"`
	LatestReplySequence      int64         `json:"latestReplySequence"`
	LatestRunStatus          string        `json:"latestRunStatus"`
	PendingInputCount        int           `json:"pendingInputCount"`
}

type PendingReply struct {
	CallID    string              `json:"callId"`
	RunID     string              `json:"runId"`
	Kind      string              `json:"kind"`
	Content   string              `json:"content"`
	Questions []question.Question `json:"questions,omitempty"`
}

type Message struct {
	Failure        bool   `json:"failure,omitempty"`
	ID             string `json:"id"`
	SessionID      string `json:"sessionId"`
	RunID          string `json:"runId"`
	Provider       string `json:"provider,omitempty"`
	ModelID        string `json:"modelId,omitempty"`
	ModelKey       string `json:"modelKey,omitempty"`
	OutputTurn     int32  `json:"outputTurn,omitempty"`
	OutputBytes    int    `json:"outputBytes,omitempty"`
	ResultsAfter   int64  `json:"resultsAfter,omitempty"`
	Sequence       int64  `json:"sequence"`
	Role           string `json:"role"`
	Content        string `json:"content"`
	QuestionCallID string `json:"questionCallId,omitempty"` // Persisted in the message event; display role stays user.
	ImageCount     int    `json:"imageCount,omitempty"`
	ImageBytes     int    `json:"-"`
	CreatedAt      string `json:"createdAt"`
}

type Image struct {
	Hash      string
	Name      string
	Bytes     int64
	MediaType string
	Data      []byte
}

type Event struct {
	SessionID string          `json:"sessionId"`
	Sequence  int64           `json:"sequence"`
	Kind      string          `json:"kind"`
	Data      json.RawMessage `json:"data"`
	CreatedAt string          `json:"createdAt"`
}

type QueuedInput struct {
	Debug           bool   `json:"debug,omitempty"`
	ID              string `json:"id"`
	SessionID       string `json:"sessionId"`
	RunID           string `json:"runId"`
	ModelKey        string `json:"modelKey"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	SelectionMode   string `json:"selectionMode,omitempty"`
	SelectionTier   string `json:"selectionTier,omitempty"`
	Mode            string `json:"mode"`
	Status          string `json:"status"`
	Content         string `json:"content"`
	ImageCount      int    `json:"imageCount,omitempty"`
	CreatedAt       string `json:"createdAt"`
}

type Store struct {
	library resources.Library
	db      *sql.DB
	orm     *gorm.DB
	readTx  gorm.ConnPool // Set only on a transaction-local, read-only snapshot view.
}

func (s *Store) reader() gorm.ConnPool {
	if s.readTx != nil {
		return s.readTx
	}
	return s.db
}

type sessionRow struct {
	ID                       string `gorm:"column:id;primaryKey"`
	Title                    string `gorm:"column:title"`
	ModelKey                 string `gorm:"column:model_key"`
	IsOpen                   int    `gorm:"column:is_open"`
	PendingKind              string `gorm:"column:pending_kind"`
	PendingContent           string `gorm:"column:pending_content"`
	PendingRunID             string `gorm:"column:pending_run_id"`
	CreatedAt                string `gorm:"column:created_at"`
	SelectionMode            string `gorm:"column:selection_mode;not null;default:manual"`
	SelectionTier            string `gorm:"column:selection_tier;not null;default:speed"`
	ManualModelKey           string `gorm:"column:manual_model_key;not null;default:''"`
	ManualReasoningEffort    string `gorm:"column:manual_reasoning_effort;not null;default:''"`
	SelectionReasoningEffort string `gorm:"column:selection_reasoning_effort;not null;default:''"`
	InputQueuePaused         bool   `gorm:"column:input_queue_paused;not null;default:false"`
	ReadSequence             int64  `gorm:"column:read_sequence;not null;default:0"`
}

func (sessionRow) TableName() string { return "sessions" }

func sessionFromRow(row sessionRow) Session {
	item := Session{ID: row.ID, Title: row.Title, ModelKey: row.ModelKey, Open: row.IsOpen == 1, CreatedAt: row.CreatedAt}
	item.SelectionMode, item.SelectionTier = row.SelectionMode, row.SelectionTier
	item.ManualModelKey, item.ManualReasoningEffort = row.ManualModelKey, row.ManualReasoningEffort
	item.SelectionReasoningEffort, item.InputQueuePaused = row.SelectionReasoningEffort, row.InputQueuePaused
	item.ReadSequence = row.ReadSequence
	if item.SelectionMode == "" {
		item.SelectionMode = "manual"
	}
	if item.SelectionTier == "" {
		item.SelectionTier = "speed"
	}
	if item.ManualModelKey == "" && item.SelectionMode == "manual" {
		item.ManualModelKey = item.ModelKey
	}
	if row.PendingKind != "" {
		item.Pending = pendingFromStorage(row.PendingKind, row.PendingRunID, row.PendingContent)
	}
	return item
}

func sessionRowsAffected(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func openORM(db *sql.DB) (*gorm.DB, error) {
	return gorm.Open(gosqlite.Dialector{Conn: db}, &gorm.Config{SkipDefaultTransaction: true, DisableAutomaticPing: true})
}

type Counts struct {
	Total   int
	Open    int
	Running int
	Jobs    int
}

func (s *Store) SessionCounts(ctx context.Context) (Counts, error) {
	var c Counts
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(is_open),0),COALESCE(SUM(CASE WHEN EXISTS(SELECT 1 FROM runs r WHERE r.session_id=s.id AND r.status='running') OR EXISTS(SELECT 1 FROM jobs j WHERE j.session_id=s.id AND j.status IN ('starting','running')) OR EXISTS(SELECT 1 FROM agent_tasks t WHERE t.session_id=s.id AND t.status='started') THEN 1 ELSE 0 END),0) FROM sessions s`).Scan(&c.Total, &c.Open, &c.Running)
	if err == nil {
		err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM jobs WHERE status IN ('starting','running')").Scan(&c.Jobs)
	}
	return c, err
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA journal_mode=WAL",
		`CREATE TABLE IF NOT EXISTS memories (
   id TEXT PRIMARY KEY, text TEXT NOT NULL UNIQUE,
   created_at TEXT NOT NULL, updated_at TEXT NOT NULL
  )`,
		`CREATE TABLE IF NOT EXISTS sessions (
   id TEXT PRIMARY KEY, title TEXT NOT NULL, is_open INTEGER NOT NULL, model_key TEXT NOT NULL DEFAULT '',
   pending_kind TEXT NOT NULL DEFAULT '', pending_content TEXT NOT NULL DEFAULT '', pending_run_id TEXT NOT NULL DEFAULT '',
   created_at TEXT NOT NULL
  )`,
		`CREATE TABLE IF NOT EXISTS runs (
   id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
   provider_id TEXT NOT NULL DEFAULT '', model_id TEXT NOT NULL DEFAULT '', model_key TEXT NOT NULL DEFAULT '',
   status TEXT NOT NULL, created_at TEXT NOT NULL, finished_at TEXT
  )`,
		`CREATE UNIQUE INDEX IF NOT EXISTS one_running_run_per_session ON runs(session_id) WHERE status='running'`,
		`CREATE INDEX IF NOT EXISTS runs_by_session ON runs(session_id,created_at DESC,id DESC)`,
		`CREATE TABLE IF NOT EXISTS agent_tasks (
   session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
   run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
   task_id TEXT NOT NULL, parent_task_id TEXT NOT NULL, join_policy TEXT NOT NULL,
   status TEXT NOT NULL, background INTEGER NOT NULL, data BLOB NOT NULL,
   PRIMARY KEY(run_id,task_id)
  )`,
		`CREATE INDEX IF NOT EXISTS active_tasks_by_session ON agent_tasks(session_id) WHERE status='started'`,
		`CREATE TABLE IF NOT EXISTS messages (
   id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
   provider_id TEXT NOT NULL DEFAULT '', model_id TEXT NOT NULL DEFAULT '', model_key TEXT NOT NULL DEFAULT '',
   run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE, sequence INTEGER NOT NULL,
   role TEXT NOT NULL, content TEXT NOT NULL, created_at TEXT NOT NULL,
   output_turn INTEGER NOT NULL DEFAULT 0, output_bytes INTEGER NOT NULL DEFAULT 0
  )`,
		`CREATE INDEX IF NOT EXISTS messages_by_session ON messages(session_id, sequence)`,
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
		`CREATE INDEX IF NOT EXISTS events_model_results ON events(session_id) WHERE kind='model_result'`,
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
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, fmt.Errorf("initialize database: %w", err)
		}
	}
	orm, err := openORM(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	result := &Store{db: db, orm: orm, library: resources.Library{StateDir: filepath.Dir(path)}}
	if err := result.initModelSelectionSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize model selection: %w", err)
	}
	return result, nil
}

// OpenReadOnly is for Worker history and output reads. Only the Server owns
// schema initialization and writes; a missing database must not be created.
func OpenReadOnly(path string) (*Store, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	absolute = filepath.ToSlash(absolute)
	if !strings.HasPrefix(absolute, "/") {
		absolute = "/" + absolute
	}
	dsn := (&url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA busy_timeout=5000", "PRAGMA cache_size=-512", "PRAGMA query_only=ON"} {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			return nil, fmt.Errorf("open read-only database: %w", err)
		}
	}
	orm, err := openORM(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, orm: orm, library: resources.Library{StateDir: filepath.Dir(path)}}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Create(ctx context.Context, id, title string) (Session, error) {
	item := Session{ID: id, Title: title, Open: true, CreatedAt: time.Now().UTC().Format(time.RFC3339), SelectionMode: "manual", SelectionTier: "speed"}
	err := s.orm.WithContext(ctx).Create(&sessionRow{ID: id, Title: title, IsOpen: 1, CreatedAt: item.CreatedAt}).Error
	return item, err
}
func (s *Store) Rename(ctx context.Context, id, title string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE sessions SET title=? WHERE id=?", title, id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return sql.ErrNoRows
	}
	if _, err := appendEvent(ctx, tx, id, "session_renamed", map[string]string{"title": title}); err != nil {
		return err
	}
	return tx.Commit()
}

// List optionally scopes summaries to open or closed conversations before the
// limit is applied. An omitted filter preserves the administrative API's list.
func (s *Store) List(ctx context.Context, open ...bool) ([]Session, error) {
	var rows []struct {
		Session             sessionRow `gorm:"embedded"`
		LatestReplySequence int64
		LatestRunStatus     string
		PendingInputCount   int
	}
	query := s.orm.WithContext(ctx).Table("sessions")
	if len(open) > 0 {
		query = query.Where("sessions.is_open = ?", open[0])
	}
	if err := query.Select(`sessions.*,
  COALESCE((SELECT MAX(sequence) FROM messages WHERE session_id=sessions.id AND role='assistant'),0) AS latest_reply_sequence,
  COALESCE((SELECT status FROM runs WHERE session_id=sessions.id ORDER BY created_at DESC,id DESC LIMIT 1),'') AS latest_run_status,
  (SELECT COUNT(*) FROM queued_inputs WHERE session_id=sessions.id AND status IN ('queued','paused')) AS pending_input_count`).
		Order("created_at DESC, id DESC").Limit(1000).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]Session, 0, len(rows))
	if len(rows) == 0 {
		return result, nil
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.Session.ID)
	}
	var runningRuns, runningJobs, runningTasks []string
	if err := s.orm.WithContext(ctx).Table("runs").Distinct("session_id").Where("session_id IN ? AND status = ?", ids, "running").Pluck("session_id", &runningRuns).Error; err != nil {
		return nil, err
	}
	if err := s.orm.WithContext(ctx).Table("jobs").Distinct("session_id").Where("session_id IN ? AND status IN ?", ids, []string{"starting", "running"}).Pluck("session_id", &runningJobs).Error; err != nil {
		return nil, err
	}
	if err := s.orm.WithContext(ctx).Table("agent_tasks").Distinct("session_id").Where("session_id IN ? AND status = ?", ids, "started").Pluck("session_id", &runningTasks).Error; err != nil {
		return nil, err
	}
	executing := make(map[string]bool, len(runningRuns))
	running := make(map[string]bool, len(runningRuns)+len(runningJobs))
	for _, id := range runningRuns {
		executing[id], running[id] = true, true
	}
	for _, id := range append(runningJobs, runningTasks...) {
		running[id] = true
	}
	for _, row := range rows {
		item := sessionFromRow(row.Session)
		item.Running, item.Executing = running[item.ID], executing[item.ID]
		item.LatestReplySequence, item.LatestRunStatus, item.PendingInputCount = row.LatestReplySequence, row.LatestRunStatus, row.PendingInputCount
		result = append(result, item)
	}
	return result, nil
}
func (s *Store) SetOpen(ctx context.Context, id string, open bool) error {
	value := 0
	if open {
		value = 1
	}
	return sessionRowsAffected(s.orm.WithContext(ctx).Model(&sessionRow{}).Where("id = ?", id).Update("is_open", value))
}
func (s *Store) Delete(ctx context.Context, id string) error {
	return s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := sessionRowsAffected(tx.Where("id=?", id).Delete(&sessionRow{})); err != nil {
			return err
		}
		return tx.Where("session_id=?", id).Delete(&sessionTierSelectionRow{}).Error
	})
}
func (s *Store) Get(ctx context.Context, id string) (Session, error) {
	var row sessionRow
	err := s.orm.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Session{}, sql.ErrNoRows
		}
		return Session{}, err
	}
	return sessionFromRow(row), nil
}

var ErrNotFound = errors.New("session not found")

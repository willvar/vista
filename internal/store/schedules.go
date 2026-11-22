package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vista/internal/schedule"
)

var ErrScheduleChanged = errors.New("schedule changed; reopen it before editing")
var ErrScheduleBusy = errors.New("this schedule already has an unfinished execution")
var ErrSchedulePast = errors.New("choose a future time")

type Schedule struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Prompt   string        `json:"prompt,omitempty"`
	ModelKey string        `json:"modelKey"`
	RuleJSON string        `json:"-" gorm:"column:rule"`
	Rule     schedule.Rule `json:"rule" gorm:"-"`
	Enabled  bool          `json:"enabled"`
	Blocked  string        `json:"blocked,omitempty"`
	Revision int64         `json:"revision"`
	NextAt   int64         `json:"nextAt"`
	AnchorAt int64         `json:"-"`
	Latest   *ScheduleRun  `json:"latest,omitempty" gorm:"-"`
}

const scheduleColumns = "id,name,model_key,rule,enabled,blocked,revision,next_at,anchor_at"
const scheduleActive = "(state IN ('queued','running','waiting_reply') OR close_pending=1)"

type ScheduleRun struct {
	ID           string `json:"id"`
	ScheduleID   string `json:"-"`
	SessionID    string `json:"sessionId,omitempty"`
	RunID        string `json:"-"`
	Slot         string `json:"-"`
	Manual       bool   `json:"manual"`
	DueAt        int64  `json:"dueAt"`
	StartedAt    int64  `json:"startedAt,omitempty"`
	FinishedAt   int64  `json:"finishedAt,omitempty"`
	State        string `json:"state"`
	Reason       string `json:"reason,omitempty"`
	StopReason   string `json:"-"`
	ClosePending bool   `json:"-"`
	DeadlineAt   int64  `json:"-"`
	Sequence     int64  `json:"-" gorm:"column:rowid;->"`
}

const scheduleRunColumns = "id,schedule_id,session_id,run_id,slot,manual,due_at,started_at,finished_at,state,reason,stop_reason,close_pending,deadline_at"

func (s *Store) ScheduleList(ctx context.Context) ([]Schedule, error) {
	items := []Schedule{}
	err := s.readView(ctx, func(view *Store) error {
		db := view.orm.WithContext(ctx)
		if err := db.Table("schedules").Select(scheduleColumns).Order("name,id").Find(&items).Error; err != nil {
			return err
		}
		var latest []ScheduleRun
		// The active occurrence takes precedence over a newer skipped slot.
		if err := db.Raw(`SELECT r.* FROM schedules s JOIN schedule_runs r ON r.rowid=COALESCE(
            (SELECT rowid FROM schedule_runs WHERE schedule_id=s.id AND ` + scheduleActive + ` LIMIT 1),
            (SELECT rowid FROM schedule_runs WHERE schedule_id=s.id ORDER BY rowid DESC LIMIT 1))`).Scan(&latest).Error; err != nil {
			return err
		}
		byID := make(map[string]*ScheduleRun, len(latest))
		for i := range latest {
			byID[latest[i].ScheduleID] = &latest[i]
		}
		for i := range items {
			if err := json.Unmarshal([]byte(items[i].RuleJSON), &items[i].Rule); err != nil {
				return err
			}
			items[i].Latest = byID[items[i].ID]
		}
		return nil
	})
	return items, err
}

func readSchedule(db *gorm.DB, id string, withPrompt bool) (Schedule, error) {
	var item Schedule
	cols := scheduleColumns
	if withPrompt {
		cols += ",prompt"
	}
	err := db.Table("schedules").Select(cols).Where("id=?", id).Take(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return item, ErrNotFound
	}
	if err != nil {
		return item, err
	}
	return item, json.Unmarshal([]byte(item.RuleJSON), &item.Rule)
}

func (s *Store) Schedule(ctx context.Context, id string) (Schedule, error) {
	return readSchedule(s.orm.WithContext(ctx), id, true)
}

func (s *Store) SaveSchedule(ctx context.Context, item Schedule, now int64) (Schedule, error) {
	cal, err := item.Rule.Parse()
	if err != nil {
		return item, err
	}
	rule, err := json.Marshal(item.Rule)
	if err != nil {
		return item, err
	}
	create := item.ID == ""
	err = s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if create {
			item.ID, item.AnchorAt, item.Enabled, item.Revision = uuid.NewString(), now, true, 1
			item.NextAt = cal.Next(now, item.AnchorAt)
		} else {
			old, err := readSchedule(tx, item.ID, false)
			if err != nil {
				return err
			}
			if old.Revision != item.Revision {
				return ErrScheduleChanged
			}
			item.Enabled, item.Blocked, item.Revision = old.Enabled, old.Blocked, old.Revision+1
			item.AnchorAt, item.NextAt = old.AnchorAt, old.NextAt
			if old.RuleJSON != string(rule) {
				item.AnchorAt = now
				item.NextAt = cal.Next(now, item.AnchorAt)
				if item.NextAt == 0 {
					return ErrSchedulePast
				}
				if err := tx.Table("schedule_runs").Where("schedule_id=? AND state='queued' AND manual=0", item.ID).
					Updates(map[string]any{"state": "cancelled", "reason": "edited", "finished_at": now}).Error; err != nil {
					return err
				}
			}
		}
		if create && item.NextAt == 0 {
			return ErrSchedulePast
		}
		item.RuleJSON = string(rule)
		if create {
			return tx.Create(&item).Error
		}
		return tx.Table("schedules").Where("id=?", item.ID).Updates(map[string]any{
			"name": item.Name, "prompt": item.Prompt, "model_key": item.ModelKey, "rule": item.RuleJSON,
			"revision": item.Revision, "anchor_at": item.AnchorAt, "next_at": item.NextAt,
		}).Error
	})
	return item, err
}

func (s *Store) SetScheduleEnabled(ctx context.Context, id string, revision int64, enabled bool, now int64) error {
	return s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		item, err := readSchedule(tx, id, false)
		if err != nil {
			return err
		}
		if item.Revision != revision {
			return ErrScheduleChanged
		}
		if enabled {
			if _, err := findActiveScheduleRun(tx, id); err == nil {
				return ErrScheduleBusy
			} else if !errors.Is(err, ErrNotFound) {
				return err
			}
			cal, err := item.Rule.Parse()
			if err != nil {
				return err
			}
			// A consumed one-shot does not acquire a second automatic run.
			if item.Rule.Kind != "once" || item.NextAt != 0 {
				if next := cal.Next(now, item.AnchorAt); next != 0 {
					item.NextAt = next
				}
			}
		} else if err := tx.Table("schedule_runs").Where("schedule_id=? AND state='queued' AND manual=0", id).
			Updates(map[string]any{"state": "cancelled", "reason": "paused", "finished_at": now}).Error; err != nil {
			return err
		}
		return tx.Table("schedules").Where("id=?", id).Updates(map[string]any{
			"enabled": enabled, "blocked": "", "next_at": item.NextAt, "revision": item.Revision + 1,
		}).Error
	})
}

func findActiveScheduleRun(db *gorm.DB, id string) (ScheduleRun, error) {
	var run ScheduleRun
	err := db.Where("schedule_id=? AND "+scheduleActive, id).Take(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrNotFound
	}
	return run, err
}

// QueueScheduleNow makes a command retry idempotent without changing the calendar.
func (s *Store) QueueScheduleNow(ctx context.Context, id, requestID string, now int64) error {
	return s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		item, err := readSchedule(tx, id, false)
		if err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&ScheduleRun{}).Where("schedule_id=? AND slot=?", id, "manual/"+requestID).Count(&count).Error; err != nil || count != 0 {
			return err
		}
		if _, err := findActiveScheduleRun(tx, id); err == nil {
			return ErrScheduleBusy
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if item.Enabled && item.NextAt > 0 && item.NextAt <= now {
			cal, err := item.Rule.Parse()
			if err != nil {
				return err
			}
			_, _, expired := cal.Due(item.NextAt, item.AnchorAt, now)
			if !expired {
				return ErrScheduleBusy
			}
		}
		return tx.Create(&ScheduleRun{ID: uuid.NewString(), ScheduleID: id, Slot: "manual/" + requestID, Manual: true, DueAt: now, State: "queued"}).Error
	})
}

// StageSchedule atomically advances the calendar and records the latest due
// slot. One row per wall-clock slot also fences retries and clock rollbacks.
func (s *Store) StageSchedule(ctx context.Context, id string, now int64) (bool, error) {
	changed := false
	err := s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		item, err := readSchedule(tx, id, false)
		if err != nil || !item.Enabled || item.NextAt == 0 || item.NextAt > now {
			return err
		}
		cal, err := item.Rule.Parse()
		if err != nil {
			return err
		}
		slot, next, expired := cal.Due(item.NextAt, item.AnchorAt, now)
		active, err := findActiveScheduleRun(tx, id)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if active.ID != "" && !active.Manual && active.State != "queued" {
			return nil
		}
		state, why, finished := "queued", "", int64(0)
		if expired {
			state, why, finished = "missed", "expired", now
		}
		if active.Manual {
			state, why, finished = "missed", "busy", now
		}
		if active.ID == "" && state == "queued" {
			// A fast manual completion can occur between the timer wake and
			// this transaction. Do not dispatch its overlapping natural slot.
			var covered int64
			if err := tx.Model(&ScheduleRun{}).Where("schedule_id=? AND manual=1 AND due_at<=? AND finished_at>=? AND started_at>0", id, slot, slot).Count(&covered).Error; err != nil {
				return err
			}
			if covered != 0 {
				state, why, finished = "missed", "busy", now
			}
		}
		if active.ID != "" && !active.Manual && active.State == "queued" && state == "queued" && active.Slot != cal.Slot(slot) {
			if err := tx.Model(&ScheduleRun{}).Where("id=?", active.ID).Updates(map[string]any{"state": "missed", "reason": "superseded", "finished_at": now}).Error; err != nil {
				return err
			}
		}
		if _, err := execSQL(ctx, tx, `INSERT INTO schedule_runs(id,schedule_id,slot,due_at,state,reason,finished_at)
		 VALUES(?,?,?,?,?,?,?) ON CONFLICT(schedule_id,slot) DO NOTHING`, uuid.NewString(), id, cal.Slot(slot), slot, state, why, finished); err != nil {
			return err
		}
		changed = true
		return tx.Table("schedules").Where("id=?", id).Update("next_at", next).Error
	})
	return changed && err == nil, err
}

func (s *Store) ScheduleWork(ctx context.Context, now int64) (due []string, runs []ScheduleRun, next int64, err error) {
	db := s.orm.WithContext(ctx)
	available := `enabled=1 AND next_at>0 AND NOT EXISTS(SELECT 1 FROM schedule_runs r
	 WHERE r.schedule_id=schedules.id AND r.manual=0 AND (r.state IN ('running','waiting_reply') OR r.close_pending=1))`
	if err = db.Table("schedules").Where(available+" AND next_at<=?", now).Order("next_at,id").Limit(32).Pluck("id", &due).Error; err != nil {
		return
	}
	if err = db.Table("schedules").Where(available).Select("COALESCE(MIN(next_at),0)").Row().Scan(&next); err != nil {
		return
	}
	err = db.Where("state='queued' OR close_pending=1 OR (state='running' AND (deadline_at<=? OR stop_reason<>''))", now).
		Order("CASE WHEN state='queued' THEN 1 ELSE 0 END, due_at,id").Limit(32).Find(&runs).Error
	if err != nil {
		return
	}
	var deadline int64
	err = db.Model(&ScheduleRun{}).Where("state='running'").Select("COALESCE(MIN(deadline_at),0)").Row().Scan(&deadline)
	if deadline > 0 && (next == 0 || deadline < next) {
		next = deadline
	}
	return
}

// ProbeScheduleWork does not migrate or reconcile unopened Profile databases.
func (s *Store) HasScheduleWork(ctx context.Context) (bool, error) {
	var exists bool
	db := s.orm.WithContext(ctx)
	if !db.Migrator().HasTable("schedules") {
		return false, ctx.Err()
	}
	err := db.Raw("SELECT EXISTS(SELECT 1 FROM schedules WHERE enabled=1 AND next_at>0) OR EXISTS(SELECT 1 FROM schedule_runs WHERE " + scheduleActive + ")").Row().Scan(&exists)
	return exists, err
}

func (s *Store) ScheduleHistory(ctx context.Context, id string, before int64) ([]ScheduleRun, int64, error) {
	items := []ScheduleRun{}
	err := s.readView(ctx, func(view *Store) error {
		db := view.orm.WithContext(ctx)
		if _, err := readSchedule(db, id, false); err != nil {
			return err
		}
		page := db.Model(&ScheduleRun{}).Select("rowid,"+scheduleRunColumns).Where("schedule_id=?", id)
		if before > 0 {
			page = page.Where("rowid<?", before)
		}
		return page.Order("rowid DESC").Limit(21).Find(&items).Error
	})
	var next int64
	if len(items) > 20 {
		items = items[:20]
		next = items[19].Sequence
	}
	return items, next, err
}

// BeginScheduledRun reuses exactly the ordinary user-message transaction.
// The durable binding must commit before any Worker can receive the command.
func (s *Store) BeginScheduledRun(ctx context.Context, item Schedule, occurrence ScheduleRun, sessionID, runID, messageID, modelKey string, now int64) (Message, error) {
	var message Message
	err := s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := readSchedule(tx, item.ID, true)
		if err != nil {
			return err
		}
		if current.Revision != item.Revision || !current.Enabled && !occurrence.Manual || occurrence.DueAt+schedule.Grace.Milliseconds() < now {
			return ErrScheduleChanged
		}
		updated := tx.Model(&ScheduleRun{}).Where("id=? AND schedule_id=? AND state='queued'", occurrence.ID, item.ID).Updates(map[string]any{
			"state": "running", "session_id": sessionID, "run_id": runID, "started_at": now, "deadline_at": now + schedule.RunLimit.Milliseconds(),
		})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrScheduleChanged
		}
		if err := tx.Create(&sessionRow{ID: sessionID, Title: current.Name, IsOpen: 1, CreatedAt: time.UnixMilli(now).UTC().Format(time.RFC3339), ManualModelKey: modelKey}).Error; err != nil {
			return err
		}
		message, _, err = s.beginRunInTx(ctx, tx, sessionID, runID, messageID, current.Prompt, nil, nil, nil, modelKey)
		return err
	})
	return message, err
}

func (s *Store) ExpireScheduleQueue(ctx context.Context, id, reason string, now int64) error {
	return s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run ScheduleRun
		if err := tx.Where("id=? AND state='queued'", id).Take(&run).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		state := "missed"
		if reason == "model" {
			state = "failed"
			if err := blockSchedule(tx, run.ScheduleID, reason); err != nil {
				return err
			}
		}
		return tx.Model(&run).Updates(map[string]any{"state": state, "reason": reason, "finished_at": now}).Error
	})
}

func blockSchedule(tx *gorm.DB, id, reason string) error {
	return tx.Table("schedules").Where("id=?", id).Updates(map[string]any{
		"enabled": false, "blocked": reason, "revision": gorm.Expr("revision+1"),
	}).Error
}

func (s *Store) StopScheduledRun(ctx context.Context, id, runID string, now int64) error {
	return s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run ScheduleRun
		if err := tx.Where("id=? AND schedule_id=?", runID, id).Take(&run).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		switch run.State {
		case "queued":
			return tx.Model(&run).Updates(map[string]any{"state": "cancelled", "finished_at": now}).Error
		case "running":
			return tx.Model(&run).Where("stop_reason='' AND state='running'").Update("stop_reason", "stopped").Error
		case "waiting_reply":
			return setSessionOpen(tx, run.SessionID, false)
		default:
			return ErrScheduleChanged
		}
	})
}

// BeginScheduleCleanup fences a late dispatch/reply at the hard deadline, or
// identifies a durably closed Session whose owner still needs to be drained.
func (s *Store) BeginScheduleCleanup(ctx context.Context, id string, now int64) (bool, error) {
	close := false
	err := s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run ScheduleRun
		if err := tx.Where("id=?", id).Take(&run).Error; err != nil {
			return err
		}
		if run.ClosePending {
			close = true
			return nil
		}
		if run.State != "running" || run.StopReason == "" && run.DeadlineAt > now {
			return nil
		}
		if run.StopReason == "" {
			if err := tx.Model(&run).Update("stop_reason", "timeout").Error; err != nil {
				return err
			}
		}
		cause := "Stopped by user"
		if run.StopReason == "" || run.StopReason == "timeout" {
			cause = "Execution reached its time limit"
		}
		if _, err := interruptRunning(ctx, tx, run.SessionID, cause); err != nil {
			return err
		}
		close = true
		return nil
	})
	return close && err == nil, err
}

func (s *Store) CompleteScheduleCleanup(ctx context.Context, id string) error {
	return s.orm.WithContext(ctx).Model(&ScheduleRun{}).Where("id=?", id).Update("close_pending", false).Error
}

func finishScheduledRun(ctx context.Context, tx *gorm.DB, runID, status string, pending bool, unknown int, now int64) error {
	var run ScheduleRun
	if err := tx.Where("run_id=? AND state='running'", runID).Take(&run).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	reason, blocked := "", ""
	switch {
	case run.StopReason == "timeout" || now >= run.DeadlineAt:
		status, reason, blocked = "interrupted", "timeout", "interrupted"
	case run.StopReason == "stopped":
		status = "cancelled"
	case status == "interrupted" && unknown == 0:
		var requested bool
		if err := tx.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM events WHERE session_id=? AND kind='run_cancel_requested' AND json_extract(data,'$.runId')=?)`, run.SessionID, runID).Row().Scan(&requested); err != nil {
			return err
		}
		if requested {
			status = "cancelled"
		} else {
			blocked = "interrupted"
		}
	case unknown > 0 || status == "interrupted":
		status, blocked = "interrupted", "interrupted"
	case pending:
		status, blocked = "waiting_reply", "question"
	}
	if blocked != "" {
		if err := blockSchedule(tx, run.ScheduleID, blocked); err != nil {
			return err
		}
	}
	if status == "waiting_reply" {
		return tx.Model(&run).Update("state", status).Error
	}
	if err := tx.Model(&run).Updates(map[string]any{"state": status, "reason": reason, "finished_at": now, "close_pending": true}).Error; err != nil {
		return err
	}
	if pending {
		if _, err := execSQL(ctx, tx, "UPDATE sessions SET pending_kind='',pending_content='',pending_run_id='' WHERE id=? AND pending_run_id=?", run.SessionID, runID); err != nil {
			return err
		}
	}
	return setSessionOpen(tx, run.SessionID, false)
}

func rebindScheduleAnswer(tx *gorm.DB, sessionID, pendingRunID, newRunID string, now int64) error {
	if pendingRunID == "" {
		return nil
	}
	return tx.Model(&ScheduleRun{}).Where("session_id=? AND run_id=? AND state='waiting_reply'", sessionID, pendingRunID).
		Updates(map[string]any{"state": "running", "run_id": newRunID, "deadline_at": now + schedule.RunLimit.Milliseconds()}).Error
}

// Closing an unanswered run abandons the occurrence, not the future plan.
func closeScheduleQuestion(tx *gorm.DB, id string) error {
	return tx.Model(&ScheduleRun{}).Where("session_id=? AND state='waiting_reply'", id).Updates(map[string]any{
		"state": "cancelled", "finished_at": time.Now().UnixMilli(), "close_pending": true,
	}).Error
}

func checkScheduleClosing(tx *gorm.DB, id string) error {
	var count int64
	err := tx.Model(&ScheduleRun{}).Where("session_id=? AND close_pending=1", id).Count(&count).Error
	if count != 0 {
		return fmt.Errorf("scheduled execution is closing: %w", ErrBusy)
	}
	return err
}

// Scheduling uses the persisted absolute deadline, including during context
// preparation and when a human answer creates a new ordinary Run.
func (s *Store) ScheduledDeadline(ctx context.Context, runID string) (int64, error) {
	var deadline int64
	err := s.orm.WithContext(ctx).Model(&ScheduleRun{}).Where("run_id=? AND state='running'", runID).Select("CASE WHEN stop_reason='' THEN deadline_at ELSE -1 END").Row().Scan(&deadline)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return deadline, err
}

func (s *Store) ActiveScheduledSessions(ctx context.Context) ([]string, error) {
	var ids []string
	err := s.orm.WithContext(ctx).Model(&ScheduleRun{}).Where("session_id<>'' AND "+scheduleActive).Pluck("session_id", &ids).Error
	return ids, err
}

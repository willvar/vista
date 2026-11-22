package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"vista/internal/platform"
	"vista/internal/schedule"
	"vista/internal/store"
	"vista/internal/workerproto"
)

type scheduleSnapshot struct {
	Items   []store.Schedule `json:"items"`
	Version string           `json:"version"`
}

func (a *App) scheduleSnapshot(ctx context.Context, version string) (scheduleSnapshot, error) {
	items, err := a.store.ScheduleList(ctx)
	return scheduleSnapshot{Items: items, Version: version}, err
}

func scheduleDecode(w http.ResponseWriter, r *http.Request, dst any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid schedule request", http.StatusBadRequest)
		return false
	}
	return true
}

func scheduleHTTPError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, store.ErrScheduleChanged), errors.Is(err, store.ErrScheduleBusy):
		code = http.StatusConflict
	case errors.Is(err, schedule.ErrRule), errors.Is(err, store.ErrSchedulePast):
		code = http.StatusBadRequest
	default:
		log.Printf("scheduled tasks: %v", err)
		http.Error(w, "could not update scheduled tasks", code)
		return
	}
	http.Error(w, err.Error(), code)
}

func (a *App) saveSchedule(w http.ResponseWriter, r *http.Request, id string) {
	var input struct {
		Name     string        `json:"name"`
		Prompt   string        `json:"prompt"`
		ModelKey string        `json:"modelKey"`
		Rule     schedule.Rule `json:"rule"`
		Revision int64         `json:"revision"`
	}
	if !scheduleDecode(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 200 || strings.TrimSpace(input.Prompt) == "" || len(input.Prompt) > workerproto.MaxPromptBytes || len(input.ModelKey) > 256 {
		http.Error(w, "provide a name and a message within the usual conversation limits", http.StatusBadRequest)
		return
	}
	item, err := a.store.SaveSchedule(r.Context(), store.Schedule{ID: id, Name: input.Name, Prompt: input.Prompt, ModelKey: input.ModelKey, Rule: input.Rule, Revision: input.Revision}, time.Now().UnixMilli())
	if err != nil {
		scheduleHTTPError(w, err)
		return
	}
	a.schedulesChanged()
	if id == "" {
		w.WriteHeader(http.StatusCreated)
	}
	// The editing form already owns the prompt; it is not part of list state.
	item.Prompt = ""
	jsonOut(w, item)
}

func (a *App) schedulesHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx := r.Context()
	path := strings.TrimPrefix(r.URL.Path, "/api/schedules")
	if path == "" {
		switch r.Method {
		case http.MethodGet:
			snapshot, err := a.scheduleSnapshot(ctx, a.stateVersion("schedules"))
			if err != nil {
				scheduleHTTPError(w, err)
				return
			}
			jsonOut(w, snapshot)
		case http.MethodPost:
			a.saveSchedule(w, r, "")
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
		return
	}
	if path == "/preview" && r.Method == http.MethodPost {
		var input struct {
			ID   string        `json:"id"`
			Rule schedule.Rule `json:"rule"`
		}
		if !scheduleDecode(w, r, &input) {
			return
		}
		cal, err := input.Rule.Parse()
		if err != nil {
			scheduleHTTPError(w, err)
			return
		}
		now := time.Now().UnixMilli()
		next := cal.Next(now, now)
		unchanged := false
		if input.ID != "" {
			item, err := a.store.Schedule(ctx, input.ID)
			if err != nil {
				scheduleHTTPError(w, err)
				return
			}
			if item.Rule == input.Rule {
				unchanged = true
				next = item.NextAt
			}
		}
		if next == 0 && !unchanged {
			scheduleHTTPError(w, store.ErrSchedulePast)
			return
		}
		jsonOut(w, map[string]int64{"nextAt": next})
		return
	}
	id, action, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	if _, err := uuid.Parse(id); err != nil {
		http.NotFound(w, r)
		return
	}
	if action == "" {
		switch r.Method {
		case http.MethodGet:
			item, err := a.store.Schedule(ctx, id)
			if err != nil {
				scheduleHTTPError(w, err)
				return
			}
			jsonOut(w, item)
		case http.MethodPut:
			a.saveSchedule(w, r, id)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
		return
	}
	if action == "history" && r.Method == http.MethodGet {
		var before int64
		if raw := r.URL.Query().Get("before"); raw != "" {
			var err error
			before, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || before < 1 {
				http.Error(w, "invalid history cursor", http.StatusBadRequest)
				return
			}
		}
		items, next, err := a.store.ScheduleHistory(ctx, id, before)
		if err != nil {
			scheduleHTTPError(w, err)
			return
		}
		jsonOut(w, map[string]any{"items": items, "nextBefore": next})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Revision     int64  `json:"revision"`
		RequestID    string `json:"requestId"`
		OccurrenceID string `json:"occurrenceId"`
	}
	if !scheduleDecode(w, r, &input) {
		return
	}
	now := time.Now().UnixMilli()
	var err error
	switch action {
	case "pause", "resume":
		err = a.store.SetScheduleEnabled(ctx, id, input.Revision, action == "resume", now)
	case "run-now":
		if _, err = uuid.Parse(input.RequestID); err != nil {
			http.Error(w, "invalid request ID", http.StatusBadRequest)
			return
		}
		err = a.store.QueueScheduleNow(ctx, id, input.RequestID, now)
	case "stop":
		if _, err = uuid.Parse(input.OccurrenceID); err != nil {
			http.Error(w, "invalid occurrence ID", http.StatusBadRequest)
			return
		}
		err = a.store.StopScheduledRun(ctx, id, input.OccurrenceID, now)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		scheduleHTTPError(w, err)
		return
	}
	a.schedulesChanged()
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) schedulesChanged() {
	a.updates.notify("schedules")
	select {
	case a.scheduleWake <- struct{}{}:
	default:
	}
}

func (a *App) scheduleSessionChanged(id string) {
	if _, ok := a.scheduledSessions.Load(id); ok {
		a.schedulesChanged()
	}
}

func (a *App) runSchedules() {
	for a.ctx.Err() == nil {
		next, err := a.stepSchedules()
		if err != nil && a.ctx.Err() == nil {
			log.Printf("scheduled tasks: %v", err)
			next = time.Now().Add(time.Second).UnixMilli()
		}
		var timeout <-chan time.Time
		var timer *time.Timer
		if next != 0 {
			// Recheck the wall clock after host clock adjustments or sleep.
			timer = time.NewTimer(min(time.Until(time.UnixMilli(next)), 30*time.Second))
			timeout = timer.C
		}
		select {
		case <-a.ctx.Done():
		case <-a.scheduleWake:
		case <-timeout:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

func (a *App) stepSchedules() (int64, error) {
	ctx, now := a.ctx, time.Now().UnixMilli()
	due, runs, next, err := a.store.ScheduleWork(ctx, now)
	if err != nil {
		return 0, err
	}
	if len(due) > 0 {
		changed := false
		for _, id := range due {
			staged, err := a.store.StageSchedule(ctx, id, now)
			if err != nil {
				if changed {
					a.schedulesChanged()
				}
				return 0, err
			}
			changed = changed || staged
		}
		if changed {
			a.schedulesChanged()
		}
		_, runs, next, err = a.store.ScheduleWork(ctx, now)
		if err != nil {
			return 0, err
		}
	}
	for _, run := range runs {
		if run.State == "queued" {
			if err := a.tryScheduledRun(run); err != nil {
				return 0, err
			}
		} else if err := a.closeScheduledExecution(run); err != nil {
			return 0, err
		}
	}
	if len(runs) > 0 {
		retry := time.Now().Add(time.Second).UnixMilli()
		if next == 0 || next > retry {
			next = retry
		}
	}
	return next, nil
}

func (a *App) tryScheduledRun(occurrence store.ScheduleRun) error {
	ctx, now := a.ctx, time.Now().UnixMilli()
	item, err := a.store.Schedule(ctx, occurrence.ScheduleID)
	if err != nil {
		return err
	}
	_, selected, modelErr := a.modelsSnapshot().ResolveEnabled(item.ModelKey)
	expired := now > occurrence.DueAt+schedule.Grace.Milliseconds()
	if modelErr != nil {
		// Custom inventories load asynchronously after the service opens a Profile.
		// A disabled known connection is not an inventory delay.
		if !expired && time.Since(a.started) < 15*time.Second {
			catalog := a.modelsSnapshot()
			if _, _, err := catalog.Resolve(item.ModelKey); err != nil {
				return nil
			}
		}
		err = a.store.ExpireScheduleQueue(ctx, occurrence.ID, "model", now)
	} else if expired {
		err = a.store.ExpireScheduleQueue(ctx, occurrence.ID, "expired", now)
	}
	if modelErr != nil || expired {
		if err == nil {
			a.schedulesChanged()
		}
		return err
	}
	available, err := platform.AvailableMemory()
	if err != nil {
		return err
	}
	sessionID, runID := uuid.NewString(), uuid.NewString()
	a.admissionLock().Lock()
	counts, err := a.store.SessionCounts(ctx)
	if err != nil {
		a.admissionLock().Unlock()
		return err
	}
	if a.admitProfileRun(ctx, available, counts.Running) != nil {
		a.admissionLock().Unlock()
		return nil
	}
	message, err := a.store.BeginScheduledRun(ctx, item, occurrence, sessionID, runID, uuid.NewString(), selected, time.Now().UnixMilli())
	a.admissionLock().Unlock()
	if errors.Is(err, store.ErrScheduleChanged) {
		return nil
	}
	if err != nil {
		return err
	}
	a.scheduledSessions.Store(sessionID, struct{}{})
	a.schedulesChanged()
	a.notifySession(sessionID)
	a.background.Go(func() {
		// The Session is now durable and closed by the normal lifecycle on all
		// terminal paths. No automatic retry after this admission boundary.
		launchCtx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
		defer cancel()
		model, key, err := a.resolveForRun(launchCtx, selected)
		cause := "Scheduled model is unavailable"
		if err == nil {
			model.Key = key
			cause = "Could not start the scheduled execution Worker"
			err = a.startWorkerUsing(sessionID, a.launchOpenWorker)
		}
		if err == nil {
			_ = a.dispatchRun(a.ctx, sessionID, runID, message, model)
			return
		}
		if event, finishErr := a.store.FinishRun(context.Background(), sessionID, runID, "", "interrupted", cause); finishErr != nil {
			log.Printf("scheduled execution %s: %v", runID, finishErr)
		} else if event != nil {
			a.notifySession(sessionID)
			a.scheduleSessionChanged(sessionID)
		}
	})
	return nil
}

func (a *App) closeScheduledExecution(run store.ScheduleRun) error {
	unlock := a.lockSessionMutation(run.SessionID)
	defer unlock()
	a.mu.Lock()
	w := a.workers[run.SessionID]
	a.mu.Unlock()
	if w != nil {
		w.runMu.Lock()
	}
	close, err := a.store.BeginScheduleCleanup(a.ctx, run.ID, time.Now().UnixMilli())
	if w != nil {
		w.runMu.Unlock()
	}
	if err != nil || !close {
		return err
	}
	a.stopWorker(run.SessionID)
	if err := a.store.CompleteScheduleCleanup(a.ctx, run.ID); err != nil {
		return err
	}
	a.scheduledSessions.Delete(run.SessionID)
	a.notifySession(run.SessionID)
	a.schedulesChanged()
	return nil
}

func (p *profiles) restoreScheduleProfiles() {
	for _, item := range p.snapshot() {
		if item.ID == defaultProfile || p.device.ctx.Err() != nil {
			continue
		}
		if err := p.restoreScheduleProfile(item.ID); err != nil && p.device.ctx.Err() == nil {
			log.Printf("restore Profile schedules: %v", err)
		}
	}
}

func (p *profiles) restoreScheduleProfile(id string) error {
	path := filepath.Join(p.configDir(id), "state", "vista.db")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("invalid Profile scheduling database")
	}
	view, err := store.OpenReadOnly(path)
	if err != nil {
		return err
	}
	work, err := view.HasScheduleWork(p.device.ctx)
	_ = view.Close()
	if err != nil || !work {
		return err
	}
	p.op.Lock()
	defer p.op.Unlock()
	_, err = p.open(id)
	return err
}

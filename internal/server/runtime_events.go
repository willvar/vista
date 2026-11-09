package server

import (
	"context"
	"database/sql"
	"errors"
	"log"

	"github.com/google/uuid"
	"vista/internal/platform"
	"vista/internal/store"
)

// Meaningful completions request a continuation. This is event-driven, shares
// normal admission/selection, and yields to pending questions and user input.
// It is never called by startup/recovery or by output-progress notifications.
func (a *App) launchRuntimeContinuation(sessionID string) {
	unlock := a.lockSessionMutation(sessionID)
	defer unlock()
	a.admissionMu.Lock()
	defer a.admissionMu.Unlock()
	a.mu.Lock()
	ready := workerCanReceive(a.workers[sessionID])
	a.mu.Unlock()
	if !ready {
		return
	}
	if _, err := a.store.RunningRunID(a.ctx, sessionID); !errors.Is(err, sql.ErrNoRows) {
		return
	}
	pending, err := a.store.HasPendingRuntimeEvents(a.ctx, sessionID)
	if err != nil || !pending {
		return
	}
	selection, err := a.store.Get(a.ctx, sessionID)
	if err != nil || !selection.Open || selection.Pending != nil || selection.InputQueuePaused {
		return
	}
	input, err := a.store.NextQueuedInput(a.ctx, sessionID)
	if err != nil || input != nil {
		return
	}
	available, err := platform.AvailableMemory()
	if err != nil {
		return
	}
	counts, err := a.store.SessionCounts(a.ctx)
	if err != nil {
		return
	}
	if err := a.admitRun(available, counts.Running); err != nil {
		return
	}
	model, _, err := a.resolveSelection(a.ctx, sessionID, selectionInput{SelectionMode: selection.SelectionMode, SelectionTier: selection.SelectionTier, ModelKey: selection.ManualModelKey, ReasoningEffort: selection.ManualReasoningEffort})
	if err != nil {
		log.Printf("session %s completion model unavailable: %v", sessionID, err)
		return
	}
	dispatched := false
	defer func() {
		if !dispatched && selection.SelectionMode == "auto" {
			a.finishUnobservedSelection(context.Background(), model, "unavailable")
		}
	}()
	runID := uuid.NewString()
	event, err := a.store.BeginRuntimeRun(a.ctx, sessionID, runID, model.Key)
	if err != nil || event == nil {
		return
	}
	a.notifySession(sessionID)
	if err := a.dispatchRun(a.ctx, sessionID, runID, store.Message{Role: "runtime", Sequence: event.Sequence}, model); err != nil {
		log.Printf("session %s completion dispatch: %v", sessionID, err)
		return
	}
	dispatched = true
}

// Reconsider durable work after any run releases admission capacity.
func (a *App) launchReadyRuntimeContinuations() {
	ids, err := a.store.PendingRuntimeSessions(a.ctx)
	if err != nil {
		return
	}
	for _, id := range ids {
		a.launchRuntimeContinuation(id)
	}
}

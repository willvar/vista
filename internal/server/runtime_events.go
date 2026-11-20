package server

import (
	"errors"
	"log"

	"github.com/google/uuid"
	"vista/internal/platform"
	"vista/internal/store"
)

// Meaningful completions and persisted restart notices request a continuation. This is event-driven, shares
// normal admission/selection, and yields to pending questions and user input.
// Startup reconsiders retained work; output-progress notifications never do.
func (a *App) launchRuntimeContinuation(sessionID string) {
	unlock := a.lockSessionMutation(sessionID)
	defer unlock()
	a.admissionLock().Lock()
	defer a.admissionLock().Unlock()
	a.mu.Lock()
	ready := workerCanReceive(a.workers[sessionID])
	a.mu.Unlock()
	if !ready {
		return
	}
	if _, err := a.store.RunningRunID(a.ctx, sessionID); !errors.Is(err, store.ErrNotFound) {
		return
	}
	pending, err := a.store.HasPendingRuntimeEvents(a.ctx, sessionID)
	if err != nil || !pending {
		return
	}
	selection, err := a.store.Get(a.ctx, sessionID)
	if err != nil || !selection.Open || selection.Pending != nil {
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
	counts, err := a.store.RuntimeAdmissionCounts(a.ctx, sessionID)
	if err != nil {
		return
	}
	if err := a.admitProfileRun(a.ctx, available, counts.Running); err != nil {
		return
	}
	model, _, err := a.resolveSelection(a.ctx, sessionID, selectionInput{ModelKey: selection.ManualModelKey, ReasoningEffort: selection.ManualReasoningEffort})
	if err != nil {
		log.Printf("session %s completion model unavailable: %v", sessionID, err)
		return
	}
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

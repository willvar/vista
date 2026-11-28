package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/google/uuid"
	"vista/internal/modeltrace"
	"vista/internal/store"
	"vista/internal/workerproto"
)

// Approval belongs to the live call and Worker, never to a model-supplied
// boolean or a persisted chat message. A restart cannot replay an approval.
type pendingSessionAction struct {
	RequestID string                `json:"requestId"`
	Kind      string                `json:"kind"`
	Targets   []sessionActionTarget `json:"targets"`
	worker    *worker
	runID     string
	previews  map[string]store.SessionActionState
	result    workerproto.SessionActionResult
}

type sessionActionTarget struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func (a *App) sessionAction(id string) *pendingSessionAction {
	a.sessionActionMu.Lock()
	defer a.sessionActionMu.Unlock()
	return a.sessionActions[id]
}

func (a *App) withdrawSessionAction(id string, w *worker, requestID string) {
	a.sessionActionMu.Lock()
	pending := a.sessionActions[id]
	removed := pending != nil && pending.worker == w && (requestID == "" || pending.RequestID == requestID)
	if removed {
		delete(a.sessionActions, id)
	}
	a.sessionActionMu.Unlock()
	if removed {
		a.hub.notify(id)
	}
}

func (a *App) requestSessionAction(source string, w *worker, result workerproto.Result) error {
	if _, err := uuid.Parse(result.AckID); err != nil {
		return errors.New("invalid session action acknowledgment")
	}
	ack := workerproto.Command{Action: "ack", RunID: result.RunID, AckID: result.AckID}
	reject := func(message string) error { ack.Error = message; return w.send(ack) }
	action := result.SessionAction
	if result.Depth != 0 || result.ParentTaskID != "" || result.TaskID != "" && result.TaskID != "main" || action == nil {
		return reject("only the active main Agent can request a session action")
	}
	if err := action.Validate(source); err != nil {
		return reject(err.Error())
	}
	active, err := a.store.AgentRunActive(a.ctx, source, result.RunID)
	if err != nil || !active {
		return reject("session action requires an active main execution")
	}
	request := &pendingSessionAction{RequestID: result.AckID, Kind: action.Kind, worker: w, runID: result.RunID, previews: make(map[string]store.SessionActionState)}
	request.result.Kind = action.Kind
	for _, id := range action.SessionIDs {
		item := workerproto.SessionActionItem{ID: id, Status: "skipped"}
		state, err := a.store.SessionActionState(a.ctx, id)
		switch {
		case errors.Is(err, store.ErrNotFound):
			item.Reason = "not_found"
		case err != nil:
			item.Reason = "state_unavailable"
		default:
			item.Reason = state.ActionBlocker(action.Kind)
		}
		if item.Reason == "already_closed" {
			item.Status = "already_closed"
		}
		if item.Reason == "" {
			request.previews[id] = state
			request.Targets = append(request.Targets, sessionActionTarget{ID: id, Title: state.Title})
		}
		request.result.Items = append(request.result.Items, item)
	}
	if len(request.Targets) == 0 {
		ack.SessionActionResult = &request.result
		return w.send(ack)
	}
	a.sessionActionMu.Lock()
	if a.sessionActions == nil {
		a.sessionActions = make(map[string]*pendingSessionAction)
	}
	if a.sessionActions[source] != nil {
		a.sessionActionMu.Unlock()
		return reject("another session action is already awaiting confirmation")
	}
	a.sessionActions[source] = request
	a.sessionActionMu.Unlock()
	a.hub.notify(source)
	return nil
}

func (a *App) confirmSessionAction(w http.ResponseWriter, r *http.Request, source, requestID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method", 405)
		return
	}
	var input struct {
		SessionIDs []string `json:"sessionIds"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.SessionIDs == nil || len(input.SessionIDs) > workerproto.MaxSessionActionTargets || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "provide the selected session IDs", 400)
		return
	}
	a.sessionActionMu.Lock()
	pending := a.sessionActions[source]
	if pending == nil || pending.RequestID != requestID {
		a.sessionActionMu.Unlock()
		http.Error(w, "session action is no longer awaiting confirmation", 409)
		return
	}
	selected := make(map[string]bool, len(input.SessionIDs))
	for _, id := range input.SessionIDs {
		if _, ok := pending.previews[id]; !ok || selected[id] {
			a.sessionActionMu.Unlock()
			http.Error(w, "select only distinct sessions in the preview", 400)
			return
		}
		selected[id] = true
	}
	delete(a.sessionActions, source) // A valid decision consumes this request once.
	a.sessionActionMu.Unlock()
	a.hub.notify(source)

	result := pending.result
	changed := false
	for i, item := range result.Items {
		if item.Reason != "" {
			continue
		}
		if !selected[item.ID] {
			result.Items[i].Reason = "not_confirmed"
			continue
		}
		// Browser navigation/disconnect cannot retract an accepted decision.
		reason := a.applySessionAction(a.ctx, source, pending, pending.previews[item.ID])
		if reason != "" {
			result.Items[i].Reason = reason
			if reason == "already_closed" {
				result.Items[i].Status = "already_closed"
			}
			continue
		}
		changed = true
		result.Items[i].Status = "closed"
		if result.Kind == "delete" {
			result.Items[i].Status = "deleted"
		}
		a.hub.notify(item.ID)
	}
	if changed {
		if result.Kind == "delete" {
			a.invalidateResourceStorage()
		}
		a.updates.notify("sessions", "status")
	}
	if err := pending.worker.send(workerproto.Command{Action: "ack", RunID: pending.runID, AckID: pending.RequestID, SessionActionResult: &result}); err != nil {
		log.Printf("session action result could not reach its Worker: %v", err)
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonOut(w, result)
}

func (a *App) applySessionAction(ctx context.Context, source string, pending *pendingSessionAction, preview store.SessionActionState) string {
	// Sort the pair so mutually targeted actions cannot deadlock. Never hold
	// an HTTP source lock for the whole batch or acquire it in the IPC reader.
	first, second := source, preview.ID
	if first == second {
		return "invalid_target"
	}
	if first > second {
		first, second = second, first
	}
	unlockFirst := a.lockSessionMutation(first)
	defer unlockFirst()
	unlockSecond := a.lockSessionMutation(second)
	defer unlockSecond()
	if ctx.Err() != nil {
		return "state_unavailable"
	}
	a.mu.Lock()
	origin := a.workers[source]
	if origin != pending.worker || !workerCanReceive(origin) || a.workersStopping {
		a.mu.Unlock()
		return "source_run_ended"
	}
	w := a.workers[preview.ID]
	if a.workerOperations[preview.ID] != nil || w != nil && !workerCanReceive(w) {
		a.mu.Unlock()
		return "worker_transition"
	}
	if w != nil {
		w.rpcMu.Lock()
		busy := len(w.pendingRPC) != 0
		w.rpcMu.Unlock()
		if busy {
			a.mu.Unlock()
			return "active_work"
		}
	}
	release := a.reserveWorkerOperationLocked(preview.ID, nil)
	a.mu.Unlock()
	defer release()
	reason, paths, err := a.store.ApplyAgentSessionAction(ctx, source, pending.runID, pending.Kind, preview)
	if err != nil {
		log.Printf("could not verify/commit agent session action: %v", err)
		return "state_unavailable"
	}
	if reason != "" {
		return reason
	}
	a.stopReservedWorker(preview.ID)
	if pending.Kind == "delete" {
		a.removeSessionOutputs(preview.ID, paths)
	}
	return ""
}

func (a *App) removeSessionOutputs(id string, paths []string) {
	ctx := context.Background()
	removed := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if _, done := removed[path]; done {
			continue
		}
		used, err := a.store.BranchOutputReferenced(ctx, path)
		if err == nil && !used && a.removeOutput(path) == nil {
			removed[path] = struct{}{}
		}
	}
	_ = modeltrace.DeleteSession(a.dir, id)
}

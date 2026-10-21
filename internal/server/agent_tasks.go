package server

import (
	"encoding/json"
	"net/http"

	"vista/internal/workerproto"
)

func (a *App) stopAgent(w http.ResponseWriter, r *http.Request, sessionID, runID string) {
	var input struct {
		TaskID string `json:"taskId"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.TaskID == "" || input.TaskID == "main" || !validTaskIdentity(input.TaskID, "") {
		http.Error(w, "invalid delegated task ID", http.StatusBadRequest)
		return
	}
	active, err := a.store.ActiveAgentTask(r.Context(), sessionID, runID, input.TaskID)
	if err != nil {
		http.Error(w, "could not read task state", 500)
		return
	}
	if !active {
		http.Error(w, "task is no longer running", http.StatusConflict)
		return
	}
	if err := a.sendWorker(sessionID, workerproto.Command{Action: "stop_agent", RunID: runID, TaskID: input.TaskID}); err != nil {
		http.Error(w, "Worker is unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

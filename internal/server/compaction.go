package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"vista/internal/modelcatalog"
	"vista/internal/platform"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func (a *App) compactionStatus(w http.ResponseWriter, r *http.Request, sessionID string) {
	status, err := a.store.CompactionStatus(r.Context(), sessionID)
	if err != nil {
		http.Error(w, "could not load compaction status", 500)
		return
	}
	jsonOut(w, status)
}

func (a *App) compactSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	var input struct {
		ModelKey        string `json:"modelKey"`
		ReasoningEffort string `json:"reasoningEffort"`
	}
	if !decodeSelectionBody(w, r, &input) {
		return
	}
	session, err := a.store.Get(r.Context(), sessionID)
	if err != nil {
		http.Error(w, "could not load session", 500)
		return
	}
	if input.ModelKey == "" {
		input.ModelKey = session.ModelKey
	}
	model, key, err := a.resolveForRun(r.Context(), input.ModelKey)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if input.ReasoningEffort != "" {
		if err := modelcatalog.ApplyReasoningEffort(&model, input.ReasoningEffort); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	}
	if !a.saveReplyLanguage(w, r, sessionID) {
		return
	}
	language, err := a.store.ReplyLanguage(r.Context(), sessionID)
	if err != nil {
		http.Error(w, "could not load reply language", 500)
		return
	}
	model.Key, model.SessionID = key, sessionID
	runID := uuid.NewString()
	a.admissionMu.Lock()
	available, err := platform.AvailableMemory()
	if err == nil {
		counts, countErr := a.store.SessionCounts(r.Context())
		err = countErr
		if err == nil {
			err = admitRun(available, counts.Running)
		}
	}
	var through int64
	var event *store.Event
	if err == nil {
		through, event, err = a.store.BeginCompaction(r.Context(), sessionID, runID)
	}
	a.admissionMu.Unlock()
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, store.ErrBusy) || errors.Is(err, store.ErrClosed) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	a.notifySession(sessionID)
	command := workerproto.Command{Action: "compact", SessionID: sessionID, RunID: runID, MessageSequence: through, ReplyLanguage: language, Model: model, MCPServers: a.currentMCPServers()}
	command.Debug = requestDebugMode(r)
	if err := a.dispatchWorker(r.Context(), sessionID, command); err != nil {
		_, _ = a.store.FinishCompaction(context.Background(), sessionID, runID, "failed", "Compaction could not start: "+err.Error())
		a.notifySession(sessionID)
		http.Error(w, "compaction could not start", 503)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	jsonOut(w, map[string]any{"runId": runID, "event": event})
}

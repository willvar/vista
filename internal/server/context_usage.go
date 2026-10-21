package server

import (
	"database/sql"
	"errors"
	"net/http"

	"vista/internal/agent"
	"vista/internal/workerproto"
)

func (a *App) contextUsage(w http.ResponseWriter, r *http.Request, sessionID string) {
	ctx := r.Context()
	// Capture the watermark first so a slow read can never overwrite a newer
	// runtime update on the client.
	events, err := a.store.EventsBefore(ctx, sessionID, 0, 1)
	if err != nil {
		http.Error(w, "could not load context usage", 500)
		return
	}
	var sequence int64
	if len(events) > 0 {
		sequence = events[0].Sequence
	}
	runID, err := a.store.RunningRunID(ctx, sessionID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "could not load current execution", 500)
		return
	}
	if runID != "" {
		usage, err := a.store.ContextUsage(ctx, sessionID)
		if err != nil {
			http.Error(w, "could not load context usage", 500)
			return
		}
		if usage != nil && usage.RunID == runID {
			jsonOut(w, usage)
			return
		}
	}
	session, err := a.store.Get(ctx, sessionID)
	if err != nil {
		http.Error(w, "could not load session", 500)
		return
	}
	key := session.ModelKey
	if session.SelectionMode == "manual" && session.ManualModelKey != "" {
		key = session.ManualModelKey
	}
	model, key, err := a.modelsSnapshot().Resolve(key)
	if err != nil {
		jsonOut(w, nil)
		return
	}
	model.Key = key
	tokens, err := agent.EstimateSessionContext(ctx, workerproto.Command{
		SessionID: sessionID, MessageSequence: sequence, Model: model, MCPServers: a.currentMCPServers(),
	})
	if err != nil {
		http.Error(w, "could not estimate retained context", 500)
		return
	}
	jsonOut(w, workerproto.ContextUsage{RunID: runID, Tokens: tokens, Sequence: sequence})
}
